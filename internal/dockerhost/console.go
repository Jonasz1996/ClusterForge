package dockerhost

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Console in een container (fase 28 M5): docker exec -it in een PTY op de
// node, met dezelfde sessies en websocket als de console van Proxmox. Hier
// staat alleen wat Docker eigen is: welke container, of hij draait, en of
// het een prodcluster is (dan vraagt de API eerst een bevestiging).

// ConsoleInput is een vraag om een shell in een container.
type ConsoleInput struct {
	Container string
	Shell     string
	User      string
	Cols      int
	Rows      int
}

// ConsolePlan is een gecontroleerde vraag: klaar om te openen.
type ConsolePlan struct {
	Host    Host
	Name    string
	Request protocol.ConsoleRequest
}

// Prod zegt of de container op een prodcluster draait.
func (p ConsolePlan) Prod() bool { return p.Host.Environment == "prod" }

// Title is de kop boven de terminal.
func (p ConsolePlan) Title() string { return p.Name + " op " + p.Host.Hostname }

// PrepareConsole controleert node en container.
func (s *Service) PrepareConsole(ctx context.Context, nodeID uuid.UUID, in ConsoleInput) (ConsolePlan, error) {
	if in.Shell == "" {
		in.Shell = protocol.DockerShellAuto
	}
	in.Container = strings.TrimSpace(in.Container)
	if !protocol.ValidContainerName(in.Container) {
		return ConsolePlan{}, ValidationError{"ongeldige container"}
	}
	h, err := s.ready(ctx, nodeID)
	if err != nil {
		return ConsolePlan{}, err
	}
	// Vers lezen: of hij nu draait, telt.
	s.mu.Lock()
	delete(s.cache, nodeID.String()+"/containers/"+in.Container+"/json?")
	s.mu.Unlock()
	raw, err := s.get(ctx, nodeID, "/containers/"+in.Container+"/json", nil)
	if err != nil {
		return ConsolePlan{}, err
	}
	var c struct {
		ID    string `json:"Id"`
		Name  string `json:"Name"`
		State struct {
			Running bool `json:"Running"`
			Paused  bool `json:"Paused"`
		} `json:"State"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return ConsolePlan{}, err
	}
	name := strings.TrimPrefix(c.Name, "/")
	if !c.State.Running || c.State.Paused {
		return ConsolePlan{}, ValidationError{"container " + name + " draait niet; start hem eerst"}
	}
	req := protocol.ConsoleRequest{
		Session: uuid.NewString(), Kind: protocol.ConsoleDocker, Container: c.ID,
		Shell: in.Shell, User: strings.TrimSpace(in.User), Cols: in.Cols, Rows: in.Rows,
	}
	if err := req.Validate(); err != nil {
		return ConsolePlan{}, ValidationError{err.Error()}
	}
	return ConsolePlan{Host: h, Name: name, Request: req}, nil
}

// ConsoleOpened zet het openen in het logboek.
func (s *Service) ConsoleOpened(ctx context.Context, actor events.Actor, p ConsolePlan) {
	payload := map[string]any{"host": p.Host.Hostname, "container": p.Name, "shell": p.Request.Shell}
	if p.Request.User != "" {
		payload["user"] = p.Request.User
	}
	if err := s.ev.Write(ctx, nil, events.Event{
		Actor: actor, SubjectType: "node", SubjectID: p.Host.NodeID.String(), Action: "docker.console_opened", Payload: payload,
	}); err != nil {
		s.log.Warn("console in logboek zetten mislukt", "err", err)
	}
}
