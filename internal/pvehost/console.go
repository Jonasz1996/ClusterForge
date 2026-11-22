package pvehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Console (fase 26 M7): een sessie loopt van de browser (websocket) via de
// server en NATS naar de agent op de Proxmox-host waar de gast draait. De
// server opent de sessie met een gewone aanroep (zodat een fout als "VM
// draait niet" een gewone foutmelding is) en de browser hangt er binnen
// consoleAttach een websocket aan. Niets van het verkeer komt in een taak of
// logboek; alleen het openen staat in het auditlog.

// ConsoleBus is het deel van de bus dat een console nodig heeft.
type ConsoleBus interface {
	OpenConsole(ctx context.Context, nodeID uuid.UUID, req protocol.ConsoleRequest) (protocol.ConsoleResponse, error)
	SubscribeConsole(nodeID uuid.UUID, session string, fn func([]byte)) (func(), error)
	SendConsole(nodeID uuid.UUID, session string, frame []byte) error
}

// consoleAttach is hoe lang de browser heeft om de websocket te openen.
const consoleAttach = 30 * time.Second

// consoleBuffer is hoeveel frames van de agent er mogen wachten op de
// browser; daarboven sluit de sessie.
const consoleBuffer = 2048

// ConsoleInput is een vraag om een console.
type ConsoleInput struct {
	Datacenter string
	// Node is de Proxmox-host waarop de gast draait, of de host zelf.
	Node string
	Kind string
	VMID int
	Cols int
	Rows int
}

// ConsoleOpened is wat de browser nodig heeft om de websocket te openen.
type ConsoleOpened struct {
	Session string `json:"session"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	// Password is het eenmalige VNC-wachtwoord; QEMU laat het na 30
	// seconden vervallen.
	Password string `json:"password,omitempty"`
}

// Console is een open sessie.
type Console struct {
	ID    string
	Owner uuid.UUID
	Kind  string
	node  uuid.UUID
	bus   ConsoleBus
	svc   *Service

	frames   chan []byte
	done     chan struct{}
	once     sync.Once
	mu       sync.Mutex
	reason   string
	attached bool
	unsub    func()
	timer    *time.Timer
}

type consoleHub struct {
	mu sync.Mutex
	m  map[string]*Console
}

// OpenConsole opent een console op de host in.Node.
func (s *Service) OpenConsole(ctx context.Context, actor events.Actor, owner uuid.UUID, in ConsoleInput) (ConsoleOpened, error) {
	dc, err := s.Datacenter(ctx, in.Datacenter)
	if err != nil {
		return ConsoleOpened{}, err
	}
	var h *Host
	for i := range dc.Hosts {
		if dc.Hosts[i].PVENode == in.Node {
			h = &dc.Hosts[i]
		}
	}
	if h == nil {
		return ConsoleOpened{}, ValidationError{"onbekende Proxmox-host " + in.Node}
	}
	if why := h.Ready(); why != "" {
		return ConsoleOpened{}, ValidationError{why}
	}
	if h.Protocol < protocol.ConsoleSince {
		return ConsoleOpened{}, ValidationError{fmt.Sprintf("een console vraagt de agent met protocol %d op %s (nu %d); werk hem bij via Nodes → Agents bijwerken", protocol.ConsoleSince, h.PVENode, h.Protocol)}
	}
	req := protocol.ConsoleRequest{Session: uuid.NewString(), Kind: in.Kind, VMID: in.VMID, Cols: in.Cols, Rows: in.Rows}
	c, res, err := s.StartConsole(ctx, h.NodeID, owner, req)
	if err != nil {
		return ConsoleOpened{}, err
	}
	title := consoleTitle(in.Kind, in.VMID, h.PVENode)
	s.audit(ctx, actor, *h, "pvehost.console_opened", map[string]any{"datacenter": in.Datacenter, "kind": in.Kind, "vmid": in.VMID})
	return ConsoleOpened{Session: c.ID, Kind: in.Kind, Title: title, Password: res.Password}, nil
}

// StartConsole opent een sessie op een node met een agent en zet hem klaar
// voor de websocket van owner. Ook Docker (fase 28) opent zo zijn console,
// zodat er één plek is voor sessies en hun websocket.
func (s *Service) StartConsole(ctx context.Context, nodeID, owner uuid.UUID, req protocol.ConsoleRequest) (*Console, protocol.ConsoleResponse, error) {
	bus, ok := s.bus.(ConsoleBus)
	if !ok {
		return nil, protocol.ConsoleResponse{}, errors.New("deze server kan geen console openen")
	}
	if err := req.Validate(); err != nil {
		return nil, protocol.ConsoleResponse{}, ValidationError{err.Error()}
	}
	c := &Console{
		ID: req.Session, Owner: owner, Kind: req.Kind, node: nodeID, bus: bus, svc: s,
		frames: make(chan []byte, consoleBuffer), done: make(chan struct{}),
	}
	unsub, err := bus.SubscribeConsole(nodeID, req.Session, c.onFrame)
	if err != nil {
		return nil, protocol.ConsoleResponse{}, err
	}
	c.unsub = unsub
	octx, cancel := context.WithTimeout(ctx, protocol.ConsoleRequestTimeout)
	defer cancel()
	res, err := bus.OpenConsole(octx, nodeID, req)
	if err == nil && res.Error != "" {
		err = ValidationError{res.Error}
	}
	if err != nil {
		unsub()
		return nil, res, err
	}
	c.timer = time.AfterFunc(consoleAttach, func() {
		c.mu.Lock()
		attached := c.attached
		c.mu.Unlock()
		if !attached {
			c.end("de browser opende de console niet", true)
		}
	})
	s.consoles.mu.Lock()
	if s.consoles.m == nil {
		s.consoles.m = map[string]*Console{}
	}
	s.consoles.m[c.ID] = c
	s.consoles.mu.Unlock()
	return c, res, nil
}

func consoleTitle(kind string, vmid int, node string) string {
	id := strconv.Itoa(vmid)
	switch kind {
	case protocol.ConsoleVNC:
		return "VM " + id + " op " + node
	case protocol.ConsoleSerial:
		return "VM " + id + " (seriële poort) op " + node
	case protocol.ConsoleCT:
		return "CT " + id + " op " + node
	}
	return "Shell op " + node
}

// AttachConsole geeft de sessie aan de websocket van de eigenaar, één keer.
func (s *Service) AttachConsole(id string, user uuid.UUID) (*Console, error) {
	s.consoles.mu.Lock()
	c := s.consoles.m[id]
	s.consoles.mu.Unlock()
	if c == nil || c.Owner != user {
		return nil, ErrNotFound
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.attached {
		return nil, ValidationError{"deze console is al open in een ander venster"}
	}
	c.attached = true
	c.timer.Stop()
	return c, nil
}

// onFrame krijgt de frames van de agent, in volgorde.
func (c *Console) onFrame(f []byte) {
	if len(f) == 0 {
		return
	}
	select {
	case <-c.done:
		return
	default:
	}
	switch f[0] {
	case protocol.FrameData:
		select {
		case c.frames <- f[1:]:
		default:
			c.end("de browser leest de console te traag", true)
		}
	case protocol.FrameClose:
		c.end(string(f[1:]), false)
	}
}

// Frames geeft de uitvoer van het programma.
func (c *Console) Frames() <-chan []byte { return c.frames }

// Done sluit als de sessie voorbij is.
func (c *Console) Done() <-chan struct{} { return c.done }

// Reason zegt waarom de sessie voorbij is; leeg als de browser hem sloot.
func (c *Console) Reason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reason
}

// Write stuurt invoer naar het programma.
func (c *Console) Write(p []byte) error {
	for len(p) > 0 {
		n := min(len(p), protocol.ConsoleChunk)
		if err := c.bus.SendConsole(c.node, c.ID, append([]byte{protocol.FrameData}, p[:n]...)); err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}

// Resize past de grootte van een terminal aan.
func (c *Console) Resize(cols, rows int) error {
	b, err := json.Marshal(protocol.ConsoleResize{Cols: cols, Rows: rows})
	if err != nil {
		return err
	}
	return c.bus.SendConsole(c.node, c.ID, append([]byte{protocol.FrameResize}, b...))
}

// Ping houdt de sessie bij de agent open.
func (c *Console) Ping() error { return c.bus.SendConsole(c.node, c.ID, []byte{protocol.FramePing}) }

// Close sluit de sessie vanuit de browser.
func (c *Console) Close() { c.end("", true) }

func (c *Console) end(reason string, tellAgent bool) {
	c.once.Do(func() {
		c.mu.Lock()
		c.reason = reason
		c.mu.Unlock()
		if c.timer != nil {
			c.timer.Stop()
		}
		close(c.done)
		if c.unsub != nil {
			c.unsub()
		}
		if tellAgent {
			_ = c.bus.SendConsole(c.node, c.ID, []byte{protocol.FrameClose})
		}
		c.svc.consoles.mu.Lock()
		delete(c.svc.consoles.m, c.ID)
		c.svc.consoles.mu.Unlock()
	})
}
