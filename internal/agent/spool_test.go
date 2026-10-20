package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// spoolData is SQL-achtige invoer die niet triviaal comprimeert.
func spoolData(n int) []byte {
	var b bytes.Buffer
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, "INSERT INTO `events` VALUES (%d,%d,'probleem %x');\n", i, 1760000000+i*7, i*2654435761)
	}
	return b.Bytes()[:n]
}

func testSpool(t *testing.T, limit int64, consumed func(int64)) *spool {
	t.Helper()
	s, err := newSpool(filepath.Join(t.TempDir(), "verhuizing"), limit, consumed)
	if err != nil {
		t.Fatal(err)
	}
	s.chunk, s.size = 1000, 5000
	return s
}

func TestSpoolRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var consumed int64
	s := testSpool(t, 1<<40, func(n int64) {
		mu.Lock()
		consumed += n
		mu.Unlock()
	})
	data := spoolData(200_000)
	go func() {
		p := data
		for i := 1; len(p) > 0; i++ {
			n := min(len(p), i*37%3001+1)
			if _, err := s.Write(p[:n]); err != nil {
				t.Errorf("schrijven: %v", err)
				return
			}
			p = p[n:]
		}
		if err := s.Close(); err != nil {
			t.Errorf("sluiten: %v", err)
		}
	}()
	var got bytes.Buffer
	buf := make([]byte, 777)
	for {
		n, err := s.Read(buf)
		got.Write(buf[:n])
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(got.Bytes(), data) {
		t.Fatalf("%d bytes terug, wil %d, of andere inhoud", got.Len(), len(data))
	}
	if consumed != int64(len(data)) {
		t.Errorf("gelezen %d, wil %d", consumed, len(data))
	}
	// Gelezen bestanden zijn al weg; alleen het laatste staat er nog.
	if files, _ := os.ReadDir(s.dir); len(files) != 1 {
		t.Errorf("bestanden na het lezen: %d", len(files))
	}
	s.remove()
	if _, err := os.Stat(s.dir); !os.IsNotExist(err) {
		t.Errorf("de map staat er nog: %v", err)
	}
}

// Boven de limiet wacht de schrijver op de lezer; er gaat niets verloren.
func TestSpoolLimit(t *testing.T) {
	s := testSpool(t, 1, nil)
	defer s.remove()
	data := spoolData(20_000)
	written := make(chan struct{})
	go func() {
		defer close(written)
		if _, err := s.Write(data); err != nil {
			t.Errorf("schrijven: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Errorf("sluiten: %v", err)
		}
	}()
	time.Sleep(100 * time.Millisecond)
	select {
	case <-written:
		t.Fatal("de schrijver wachtte niet op de lezer")
	default:
	}
	s.mu.Lock()
	frames := len(s.frames)
	s.mu.Unlock()
	if frames != 1 {
		t.Errorf("%d brokken op schijf, wil 1", frames)
	}
	got, err := io.ReadAll(s)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("lezen: %d bytes, %v", len(got), err)
	}
	<-written
}

// Een mislukte dump stopt de import meteen, ook met brokken op schijf.
func TestSpoolWriterError(t *testing.T) {
	s := testSpool(t, 1<<40, nil)
	defer s.remove()
	if _, err := s.Write(spoolData(10_000)); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("Error 2026: TLS/SSL error")
	s.CloseWithError(boom)
	if _, err := s.Read(make([]byte, 10)); !errors.Is(err, boom) {
		t.Fatalf("lezen: %v", err)
	}
}

// Stopt de import, dan krijgt een wachtende dump een fout.
func TestSpoolReaderClosed(t *testing.T) {
	s := testSpool(t, 1, nil)
	defer s.remove()
	done := make(chan error, 1)
	go func() {
		_, err := s.Write(spoolData(20_000))
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	s.CloseRead(errors.New("importeren mislukt"))
	select {
	case err := <-done:
		if err == nil || err.Error() != "importeren mislukt" {
			t.Fatalf("schrijven: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("de schrijver bleef wachten")
	}
}

// De fout van 2026-10-20: de import stond even stil, mariadb-dump las niet
// meer en MariaDB verbrak na net_write_timeout de verbinding. Met de spool
// is de dump klaar voor de import begint te lezen.
func TestPipeStalledImport(t *testing.T) {
	s, err := newSpool(filepath.Join(t.TempDir(), "verhuizing"), 1<<40, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.remove()
	data := append(spoolData(8<<20), "\n"+dumpEndMarker+" 2026-10-20 23:02:41\n"...)
	dumped := make(chan struct{})
	dump := func(_ context.Context, w io.Writer) error {
		defer close(dumped)
		for p := data; len(p) > 0; {
			n := min(len(p), 64<<10)
			if _, err := w.Write(p[:n]); err != nil {
				return err
			}
			p = p[n:]
		}
		return nil
	}
	var got []byte
	imp := func(_ context.Context, r io.Reader) error {
		select {
		case <-dumped:
		case <-time.After(10 * time.Second):
			return errors.New("de dump wachtte op de import")
		}
		var err error
		got, err = io.ReadAll(r)
		return err
	}
	if err := pipe(context.Background(), protocol.DBMariaDB, dump, imp, nil, s); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("%d bytes geïmporteerd, wil %d", len(got), len(data))
	}
}

// Mislukt de dump halverwege, dan meldt pipe de fout van de dump, ook als
// de import daardoor ook faalt.
func TestPipeSpoolDumpError(t *testing.T) {
	s, err := newSpool(filepath.Join(t.TempDir(), "verhuizing"), 1<<40, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.remove()
	dump := func(_ context.Context, w io.Writer) error {
		if _, err := w.Write(spoolData(10 << 20)); err != nil {
			return err
		}
		return errors.New("mariadb-dump: Error 2026: TLS/SSL error: Connection reset by peer (104)")
	}
	imp := func(_ context.Context, r io.Reader) error {
		_, err := io.Copy(io.Discard, r)
		return err
	}
	err = pipe(context.Background(), protocol.DBMariaDB, dump, imp, nil, s)
	if err == nil || err.Error() != "dump: mariadb-dump: Error 2026: TLS/SSL error: Connection reset by peer (104)" {
		t.Fatalf("fout: %v", err)
	}
}

// Lukt het schrijven naar de buffer niet, dan meldt pipe die fout en niet
// de gebroken pijp van mariadb-dump die erop volgt.
func TestPipeSpoolDiskError(t *testing.T) {
	s := testSpool(t, 1<<40, nil)
	defer s.remove()
	// Het tweede bestand bestaat al, dus het openen ervan mislukt.
	if err := os.WriteFile(s.path(1), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dump := func(_ context.Context, w io.Writer) error {
		if _, err := w.Write(spoolData(200_000)); err != nil {
			return errors.New("mariadb-dump: signal: broken pipe")
		}
		return nil
	}
	imp := func(_ context.Context, r io.Reader) error {
		_, err := io.Copy(io.Discard, r)
		return err
	}
	err := pipe(context.Background(), protocol.DBMariaDB, dump, imp, nil, s)
	if err == nil || !strings.HasPrefix(err.Error(), "buffer op schijf: ") || !errors.Is(err, os.ErrExist) {
		t.Fatalf("fout: %v", err)
	}
}
