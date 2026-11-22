package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/pvehost"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Console van fase 26 M7: openen is een gewone aanroep, het verkeer gaat
// daarna over een websocket naar /api/v1/pve/console/{session}.

func (s *Server) OpenPveConsole(w http.ResponseWriter, r *http.Request, dcId string) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in gen.PveConsoleInput
	if !decode(w, r, &in) {
		return
	}
	ci := pvehost.ConsoleInput{Datacenter: dcId, Node: in.Node, Kind: string(in.Kind)}
	if in.Vmid != nil {
		ci.VMID = *in.Vmid
	}
	if in.Cols != nil {
		ci.Cols = *in.Cols
	}
	if in.Rows != nil {
		ci.Rows = *in.Rows
	}
	c, err := s.pvehosts.OpenConsole(r.Context(), events.User(p.User.ID), p.User.ID, ci)
	if err != nil {
		s.pveHostError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, c)
}

// consoleMessage is een tekstbericht van de browser; data gaat binair.
type consoleMessage struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// pveConsoleSocket verbindt de websocket met de sessie. Alleen de
// beheerder die hem opende, en alleen één keer. websocket.Accept weigert
// een andere Origin dan deze host, zodat een vreemde site de cookie niet
// kan gebruiken.
func (s *Server) pveConsoleSocket(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	c, err := s.pvehosts.AttachConsole(chi.URLParam(r, "session"), p.User.ID)
	if err != nil {
		s.pveHostError(w, r, err)
		return
	}
	defer c.Close()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		return
	}
	ws.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		defer cancel()
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageText {
				var m consoleMessage
				if json.Unmarshal(data, &m) == nil && m.Type == "resize" && m.Cols > 0 && m.Rows > 0 && m.Cols <= 1000 && m.Rows <= 1000 {
					_ = c.Resize(m.Cols, m.Rows)
				}
				continue
			}
			if c.Write(data) != nil {
				return
			}
		}
	}()
	ping := time.NewTicker(protocol.ConsolePing)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = ws.Close(websocket.StatusNormalClosure, "")
			return
		case f := <-c.Frames():
			if ws.Write(ctx, websocket.MessageBinary, f) != nil {
				return
			}
		case <-c.Done():
			// Eerst wat er nog wacht, dan de reden.
			for {
				select {
				case f := <-c.Frames():
					if ws.Write(ctx, websocket.MessageBinary, f) != nil {
						return
					}
					continue
				default:
				}
				break
			}
			_ = ws.Close(websocket.StatusNormalClosure, closeReason(c.Reason()))
			return
		case <-ping.C:
			_ = c.Ping()
		}
	}
}

// closeReason past in een closeframe (hoogstens 123 bytes).
func closeReason(s string) string {
	if len(s) <= 120 {
		return s
	}
	s = s[:120]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
