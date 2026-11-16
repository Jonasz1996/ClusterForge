package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Console (fase 26 M7). De agent op een Proxmox-host start per sessie één
// programma: qm vncproxy voor het scherm van een VM (RFB over stdin en
// stdout, met een eenmalig wachtwoord dat QEMU na 30 seconden laat
// vervallen), en in een pseudo-terminal qm terminal (seriële poort), pct
// enter (CT) of login -f root (de host zelf). De bytes gaan ongewijzigd heen
// en weer; de agent bewaart en logt er niets van.

func (a *Agent) onConsole(m *nats.Msg) {
	go func() {
		res := a.openConsole(m.Data)
		data, err := envelope(protocol.TypeConsoleResult, res)
		if err != nil {
			return
		}
		if err := m.Respond(data); err != nil {
			a.Log.Warn("antwoord op consoleverzoek sturen mislukt", "err", err)
		}
	}()
}

func (a *Agent) openConsole(data []byte) protocol.ConsoleResponse {
	var env protocol.Envelope
	var req protocol.ConsoleRequest
	if err := json.Unmarshal(data, &env); err != nil || env.Type != protocol.TypeConsoleRequest {
		return protocol.ConsoleResponse{Error: "ongeldig consoleverzoek"}
	}
	if err := json.Unmarshal(env.Body, &req); err != nil {
		return protocol.ConsoleResponse{Error: "ongeldig consoleverzoek"}
	}
	if err := req.Validate(); err != nil {
		return protocol.ConsoleResponse{Error: err.Error()}
	}
	if !req.Deadline.IsZero() && time.Now().After(req.Deadline) {
		return protocol.ConsoleResponse{Error: "consoleverzoek verlopen; klopt de klok van deze host?"}
	}
	if n := a.consoles.Add(1); n > protocol.MaxConsoleSessions {
		a.consoles.Add(-1)
		return protocol.ConsoleResponse{Error: fmt.Sprintf("er staan al %d consoles open op deze host; sluit er eerst een", protocol.MaxConsoleSessions)}
	}
	s, pw, err := a.startConsole(req)
	if err != nil {
		a.consoles.Add(-1)
		return protocol.ConsoleResponse{Error: err.Error()}
	}
	sub, err := a.nc.Subscribe(protocol.ConsoleIn(a.Config.NodeID, req.Session), s.onIn)
	if err != nil {
		s.stop()
		_ = s.cmd.Wait()
		a.consoles.Add(-1)
		return protocol.ConsoleResponse{Error: "console openen mislukt: " + err.Error()}
	}
	s.sub = sub
	a.Log.Info("console geopend", "soort", req.Kind, "vmid", req.VMID)
	go s.pump()
	go s.watch()
	return protocol.ConsoleResponse{Password: pw}
}

// startConsole start het programma. pw is het VNC-wachtwoord (alleen vnc).
func (a *Agent) startConsole(req protocol.ConsoleRequest) (*consoleSession, string, error) {
	pw := ""
	if req.Kind == protocol.ConsoleVNC {
		pw = vncPassword()
	}
	var cmd *exec.Cmd
	if a.ConsoleCommand != nil {
		cmd = a.ConsoleCommand(req, pw)
	} else {
		var err error
		if cmd, err = a.pveHost().consoleCommand(req, pw); err != nil {
			return nil, "", err
		}
	}
	s := &consoleSession{a: a, req: req, out: protocol.ConsoleOut(a.Config.NodeID, req.Session), cmd: cmd, done: make(chan struct{})}
	s.touch()
	if req.Terminal() {
		cols, rows := req.Cols, req.Rows
		if cols == 0 || rows == 0 {
			cols, rows = 80, 24
		}
		pty, err := startPTY(cmd, cols, rows)
		if err != nil {
			return nil, "", fmt.Errorf("terminal openen mislukt: %w", err)
		}
		s.pty, s.r, s.w = pty, pty, pty
		return s, pw, nil
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, "", err
	}
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", err
	}
	cmd.Stderr = &s.stderr
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("console starten mislukt: %w", err)
	}
	s.r, s.w = outPipe, in
	return s, pw, nil
}

// consoleCommand maakt het programma op een echte Proxmox-host, na een
// controle dat de gast hier draait.
func (h pveHost) consoleCommand(req protocol.ConsoleRequest, pw string) (*exec.Cmd, error) {
	if !h.isHost() {
		return nil, errors.New("dit is geen Proxmox-host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	id := strconv.Itoa(req.VMID)
	term := []string{"TERM=xterm-256color", "LANG=C.UTF-8"}
	switch req.Kind {
	case protocol.ConsoleVNC, protocol.ConsoleSerial:
		if err := h.guestRunning(ctx, "qm", id); err != nil {
			return nil, err
		}
		if req.Kind == protocol.ConsoleVNC {
			cmd := exec.Command("qm", "vncproxy", id)
			// qm vncproxy zet dit als VNC-wachtwoord en weigert zonder.
			cmd.Env = append(os.Environ(), "LC_PVE_TICKET="+pw)
			return cmd, nil
		}
		cfg, err := h.exec(ctx, "qm", "config", id)
		if err != nil {
			return nil, execError("VM-config lezen", err)
		}
		if !bytes.Contains(cfg, []byte("\nserial0:")) && !bytes.HasPrefix(cfg, []byte("serial0:")) {
			return nil, errors.New("deze VM heeft geen seriële poort (serial0); voeg er een toe onder Hardware of gebruik het scherm")
		}
		cmd := exec.Command("qm", "terminal", id, "-iface", "serial0")
		cmd.Env = append(os.Environ(), term...)
		return cmd, nil
	case protocol.ConsoleCT:
		if err := h.guestRunning(ctx, "pct", id); err != nil {
			return nil, err
		}
		cmd := exec.Command("pct", "enter", id)
		cmd.Env = append(os.Environ(), term...)
		return cmd, nil
	case protocol.ConsoleHost:
		cmd := exec.Command("/bin/login", "-f", "root")
		cmd.Env = append([]string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}, term...)
		return cmd, nil
	}
	return nil, errors.New("onbekende soort console")
}

// guestRunning kijkt met qm of pct status of de gast op deze host draait.
func (h pveHost) guestRunning(ctx context.Context, tool, id string) error {
	out, err := h.exec(ctx, tool, "status", id)
	if err != nil {
		return execError("status van "+id+" lezen", err)
	}
	if !strings.Contains(string(out), "status: running") {
		return fmt.Errorf("%s draait niet (%s); start hem eerst", id, strings.TrimSpace(string(out)))
	}
	return nil
}

func execError(what string, err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if msg := lastLine(string(ee.Stderr)); msg != "" {
			return fmt.Errorf("%s: %s", what, msg)
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

// vncPassword is een willekeurig wachtwoord van 8 tekens: VNC gebruikt er
// niet meer.
func vncPassword() string {
	const chars = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

type consoleSession struct {
	a      *Agent
	req    protocol.ConsoleRequest
	out    string
	cmd    *exec.Cmd
	pty    *os.File
	r      io.Reader
	w      io.Writer
	stderr lockedBuffer
	sub    *nats.Subscription
	last   atomic.Int64
	once   sync.Once
	done   chan struct{}
}

func (s *consoleSession) touch() { s.last.Store(time.Now().UnixNano()) }

func (s *consoleSession) onIn(m *nats.Msg) {
	if len(m.Data) == 0 {
		return
	}
	s.touch()
	switch m.Data[0] {
	case protocol.FrameData:
		if _, err := s.w.Write(m.Data[1:]); err != nil {
			s.close("schrijven naar de console mislukt")
		}
	case protocol.FrameResize:
		var r protocol.ConsoleResize
		if s.pty != nil && json.Unmarshal(m.Data[1:], &r) == nil && r.Cols > 0 && r.Rows > 0 && r.Cols <= 1000 && r.Rows <= 1000 {
			_ = resizePTY(s.pty, r.Cols, r.Rows)
		}
	case protocol.FrameClose:
		s.close("")
	}
}

// pump stuurt de uitvoer van het programma naar de server.
func (s *consoleSession) pump() {
	buf := make([]byte, protocol.ConsoleChunk)
	for {
		n, err := s.r.Read(buf[1:])
		if n > 0 {
			buf[0] = protocol.FrameData
			if perr := s.a.nc.Publish(s.out, buf[:n+1]); perr != nil {
				s.close("versturen mislukt")
				return
			}
		}
		if err != nil {
			reason := "het programma is gestopt"
			if werr := s.cmd.Wait(); werr != nil {
				if msg := lastLine(s.stderr.String()); msg != "" {
					reason = msg
				} else {
					reason = werr.Error()
				}
			}
			s.close(reason)
			return
		}
	}
}

// watch sluit de sessie als de server zwijgt.
func (s *consoleSession) watch() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			if time.Since(time.Unix(0, s.last.Load())) > protocol.ConsoleIdle {
				s.close("geen verkeer meer van ClusterForge")
				return
			}
		}
	}
}

func (s *consoleSession) close(reason string) {
	s.once.Do(func() {
		if s.sub != nil {
			_ = s.sub.Unsubscribe()
		}
		s.stop()
		_ = s.a.nc.Publish(s.out, append([]byte{protocol.FrameClose}, reason...))
		s.a.consoles.Add(-1)
		close(s.done)
		s.a.Log.Info("console gesloten", "soort", s.req.Kind, "vmid", s.req.VMID)
	})
}

// stop beëindigt het programma: de pseudo-terminal dicht geeft de shell een
// SIGHUP, een kill ruimt de rest op.
func (s *consoleSession) stop() {
	if s.pty != nil {
		_ = s.pty.Close()
	}
	if c, ok := s.w.(io.Closer); ok && s.pty == nil {
		_ = c.Close()
	}
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

// lockedBuffer is een bytes.Buffer voor stderr; exec schrijft erin vanuit
// een eigen goroutine.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.b.Len() > 8<<10 {
		l.b.Reset()
	}
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
