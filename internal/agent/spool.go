package agent

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// De spool zit bij een verhuizing tussen mariadb-dump en de import. MariaDB
// verbreekt een verbinding als de client net_write_timeout lang (standaard
// 60 s) niets leest. Stond de import even stil, bijvoorbeeld omdat Galera op
// de andere node wachtte, dan las mariadb-dump ook niet meer en brak de dump
// af met "Connection reset by peer … when dumping table … at row". De spool
// leest de dump daarom altijd door en zet wat de import nog niet las in
// brokken met zstd op schijf; de import leest ze in volgorde terug. Een
// bestand gaat weg zodra de import het uit heeft.

const (
	// spoolChunk is hoeveel SQL er in één zstd-frame gaat.
	spoolChunk = 4 << 20
	// spoolFile is ongeveer hoe groot een bestand van de spool wordt.
	spoolFile = 256 << 20
	// spoolReserve blijft er altijd vrij naast de database zelf.
	spoolReserve = 2 << 30
	// mariaDataDir is waar de nieuwe database terechtkomt.
	mariaDataDir = "/var/lib/mysql"
	// defaultSpoolDir is de map van de spool; leeg Agent.SpoolDir.
	defaultSpoolDir = "/var/lib/clusterforge/verhuizing"
)

// errSpoolClosed krijgt de dump als de import al gestopt is.
var errSpoolClosed = errors.New("de import leest niet meer")

type spoolFrame struct {
	file      int
	off, size int64
}

// spool is een buffer op schijf met één schrijver en één lezer.
type spool struct {
	dir string
	// limit is hoeveel gecomprimeerde bytes er tegelijk op schijf mogen
	// staan; daarboven wacht de schrijver op de lezer.
	limit       int64
	chunk, size int64
	// consumed krijgt de bytes SQL die de import las.
	consumed func(int64)

	enc *zstd.Encoder
	dec *zstd.Decoder

	// Van de schrijver.
	buf  []byte
	w    *os.File
	file int
	off  int64

	mu     sync.Mutex
	cond   sync.Cond
	frames []spoolFrame
	disk   int64
	done   bool
	werr   error
	rerr   error

	// Van de lezer.
	cur   []byte
	r     *os.File
	rfile int
}

// newSpool maakt een lege spool in een nieuwe map dir.
func newSpool(dir string, limit int64, consumed func(int64)) (*spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		_ = enc.Close()
		return nil, err
	}
	s := &spool{dir: dir, limit: limit, chunk: spoolChunk, size: spoolFile, consumed: consumed, enc: enc, dec: dec, file: -1, rfile: -1}
	s.cond.L = &s.mu
	return s, nil
}

func (s *spool) path(file int) string { return filepath.Join(s.dir, fmt.Sprintf("%06d.zst", file)) }

// Write neemt p over; een vol brok gaat naar schijf.
func (s *spool) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		take := min(int64(len(p)), s.chunk-int64(len(s.buf)))
		s.buf = append(s.buf, p[:take]...)
		p = p[take:]
		if int64(len(s.buf)) == s.chunk {
			if err := s.flush(); err != nil {
				return n - len(p), err
			}
		}
	}
	return n, nil
}

// flush zet het brok in buf als zstd-frame op schijf, als de lezer
// ver genoeg is.
func (s *spool) flush() error {
	if len(s.buf) == 0 {
		return nil
	}
	s.mu.Lock()
	for s.rerr == nil && s.disk > 0 && s.disk >= s.limit {
		s.cond.Wait()
	}
	rerr := s.rerr
	s.mu.Unlock()
	if rerr != nil {
		return rerr
	}
	comp := s.enc.EncodeAll(s.buf, nil)
	s.buf = s.buf[:0]
	if s.w == nil || s.off >= s.size {
		if s.w != nil {
			if err := s.w.Close(); err != nil {
				return err
			}
		}
		f, err := os.OpenFile(s.path(s.file+1), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		s.w, s.file, s.off = f, s.file+1, 0
	}
	if _, err := s.w.Write(comp); err != nil {
		return fmt.Errorf("buffer op schijf: %w", err)
	}
	s.mu.Lock()
	s.frames = append(s.frames, spoolFrame{file: s.file, off: s.off, size: int64(len(comp))})
	s.disk += int64(len(comp))
	s.cond.Broadcast()
	s.mu.Unlock()
	s.off += int64(len(comp))
	return nil
}

// Close zegt dat de dump compleet is: de lezer krijgt de rest en dan EOF.
// Lukt het laatste brok niet, dan moet de schrijver CloseWithError doen.
func (s *spool) Close() error {
	err := s.flush()
	if s.w != nil {
		if cerr := s.w.Close(); err == nil {
			err = cerr
		}
		s.w = nil
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.done = true
	s.cond.Broadcast()
	s.mu.Unlock()
	return nil
}

// CloseWithError zegt dat de dump mislukte: de lezer krijgt meteen err,
// ook als er nog brokken klaarstaan, zodat de import stopt.
func (s *spool) CloseWithError(err error) {
	s.mu.Lock()
	if s.werr == nil {
		s.werr = err
	}
	s.done = true
	s.cond.Broadcast()
	s.mu.Unlock()
}

// CloseRead zegt dat de import niet meer leest: de schrijver krijgt een
// fout en wacht niet langer.
func (s *spool) CloseRead(err error) {
	if err == nil {
		err = errSpoolClosed
	}
	s.mu.Lock()
	if s.rerr == nil {
		s.rerr = err
	}
	s.cond.Broadcast()
	s.mu.Unlock()
}

// Read geeft de dump in volgorde terug; wacht als de import de dump
// heeft ingehaald.
func (s *spool) Read(p []byte) (int, error) {
	for len(s.cur) == 0 {
		s.mu.Lock()
		for len(s.frames) == 0 && !s.done && s.rerr == nil {
			s.cond.Wait()
		}
		switch {
		case s.rerr != nil:
			err := s.rerr
			s.mu.Unlock()
			return 0, err
		case s.werr != nil:
			err := s.werr
			s.mu.Unlock()
			return 0, err
		case len(s.frames) == 0:
			s.mu.Unlock()
			return 0, io.EOF
		}
		fr := s.frames[0]
		s.mu.Unlock()
		raw, err := s.readFrame(fr)
		if err != nil {
			return 0, fmt.Errorf("buffer op schijf: %w", err)
		}
		s.mu.Lock()
		s.frames = s.frames[1:]
		s.disk -= fr.size
		s.cond.Broadcast()
		s.mu.Unlock()
		s.cur = raw
		if s.consumed != nil {
			s.consumed(int64(len(raw)))
		}
	}
	n := copy(p, s.cur)
	s.cur = s.cur[n:]
	return n, nil
}

// readFrame leest en pakt een frame uit; een bestand dat de lezer uit
// heeft, gaat weg.
func (s *spool) readFrame(fr spoolFrame) ([]byte, error) {
	if s.r == nil || s.rfile != fr.file {
		if s.r != nil {
			_ = s.r.Close()
			_ = os.Remove(s.path(s.rfile))
		}
		f, err := os.Open(s.path(fr.file))
		if err != nil {
			return nil, err
		}
		s.r, s.rfile = f, fr.file
	}
	comp := make([]byte, fr.size)
	if _, err := s.r.ReadAt(comp, fr.off); err != nil {
		return nil, err
	}
	return s.dec.DecodeAll(comp, make([]byte, 0, s.chunk))
}

// remove ruimt de spool op, als schrijver en lezer klaar zijn.
func (s *spool) remove() {
	if s.w != nil {
		_ = s.w.Close()
	}
	if s.r != nil {
		_ = s.r.Close()
	}
	_ = s.enc.Close()
	s.dec.Close()
	_ = os.RemoveAll(s.dir)
}

// spoolLimit is hoeveel de spool gecomprimeerd op schijf mag zetten: wat
// er vrij is in dir, min de database van ongeveer total bytes als die op
// hetzelfde bestandssysteem komt, min een reserve. Weet de agent niets, dan
// 0: de spool houdt dan één brok vast, zoals een gewone pijp.
func spoolLimit(dir, dataDir string, total int64) int64 {
	st, ok := statfs(dir)
	if !ok {
		return 0
	}
	free := int64(min(st.avail, 1<<62)) //nolint:gosec // past altijd
	if total <= 0 {
		total = free / 2
	}
	if sameDevice(dir, dataDir) {
		free -= total
	}
	return max(free-spoolReserve, 0)
}
