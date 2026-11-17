package zabbixmon

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
)

// --- schrijven naar Zabbix (M6) ----------------------------------------------------------

// Bits van event.acknowledge (Zabbix 7.0+).
const (
	ackClose          = 1
	ackAcknowledge    = 2
	ackMessage        = 4
	ackChangeSeverity = 8
)

// maxAckMessage: Zabbix bewaart hoogstens 2048 tekens per bericht.
const maxAckMessage = 2048

// AckInput is een bevestiging van een probleem.
type AckInput struct {
	Message string
	// Close sluit het probleem; dat kan alleen als de trigger handmatig
	// sluiten toelaat (anders weigert Zabbix).
	Close bool
	// Severity zet een nieuwe ernst (0-5); nil laat hem staan.
	Severity *int
}

// AckPlan zoekt het open probleem en zegt of het een prod-node raakt (dan
// vraagt de server een bevestiging met tweestap of wachtwoord).
func (s *Service) AckPlan(ctx context.Context, eventID string) (Problem, bool, error) {
	s.mu.RLock()
	var p *Problem
	if s.snap != nil {
		for i := range s.snap.Problems {
			if s.snap.Problems[i].EventID == eventID {
				x := s.snap.Problems[i]
				p = &x
				break
			}
		}
	}
	s.mu.RUnlock()
	if p == nil {
		return Problem{}, false, ErrNotFound
	}
	var clusters []uuid.UUID
	for _, h := range p.Hosts {
		if h.Node != nil && h.Node.ClusterID != nil {
			clusters = append(clusters, *h.Node.ClusterID)
		}
	}
	if len(clusters) == 0 {
		return *p, false, nil
	}
	var prod bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM clusters WHERE id = ANY($1) AND environment = 'prod')`, clusters).Scan(&prod); err != nil {
		return Problem{}, false, err
	}
	return *p, prod, nil
}

// Acknowledge bevestigt een probleem in Zabbix, met een bericht, en eventueel
// een nieuwe ernst of sluiten. Het komt in het logboek van ClusterForge; in
// Zabbix staat het op naam van het API-token.
func (s *Service) Acknowledge(ctx context.Context, actor events.Actor, who string, p Problem, in AckInput) error {
	msg := strings.TrimSpace(in.Message)
	if utf8.RuneCountInString(msg) > maxAckMessage-40 {
		return &FieldError{"het bericht is te lang"}
	}
	action := ackAcknowledge
	params := map[string]any{"eventids": []string{p.EventID}}
	// Zabbix toont het bericht bij de gebruiker van het token; daarom de
	// naam van wie het in ClusterForge deed erbij.
	if msg != "" || who != "" {
		action |= ackMessage
		params["message"] = strings.TrimSpace(msg + " (via ClusterForge, " + who + ")")
	}
	if in.Severity != nil {
		if *in.Severity < 0 || *in.Severity > 5 {
			return &FieldError{"ernst moet tussen 0 en 5 liggen"}
		}
		if *in.Severity != p.Severity {
			action |= ackChangeSeverity
			params["severity"] = *in.Severity
		}
	}
	if in.Close {
		action |= ackClose
	}
	params["action"] = action
	api, _, err := s.Client(ctx)
	if err != nil {
		return err
	}
	var out struct {
		EventIDs []zint `json:"eventids"`
	}
	if err := api.Call(ctx, "event.acknowledge", params, &out); err != nil {
		return friendlyWrite(err)
	}
	payload := map[string]any{"eventid": p.EventID, "name": p.Name, "close": in.Close, "message": msg}
	if action&ackChangeSeverity != 0 {
		payload["from"], payload["to"] = severityName[min(max(p.Severity, 0), 5)], severityName[*in.Severity]
	}
	// Op elke node van het probleem, zodat het in hun tijdlijn staat.
	wrote := false
	for _, h := range p.Hosts {
		if h.Node != nil {
			payload["host"] = h.Node.Hostname
			s.writeEvent(ctx, events.Event{Actor: actor, SubjectType: "node", SubjectID: h.Node.NodeID.String(), ClusterID: h.Node.ClusterID,
				Action: "zabbix.problem_acknowledged", Payload: payload})
			wrote = true
		}
	}
	if !wrote {
		s.writeEvent(ctx, events.Event{Actor: actor, SubjectType: "zabbix_problem", SubjectID: p.EventID, Action: "zabbix.problem_acknowledged", Payload: payload})
	}
	// Meteen opnieuw lezen, zodat het overzicht klopt.
	s.mu.Lock()
	if s.snap != nil {
		for i := range s.snap.Problems {
			if s.snap.Problems[i].EventID == p.EventID {
				s.snap.Problems[i].Acknowledged = true
			}
		}
	}
	s.mu.Unlock()
	return nil
}

func (s *Service) writeEvent(ctx context.Context, e events.Event) {
	if s.ev == nil {
		return
	}
	if err := s.ev.Write(ctx, nil, e); err != nil {
		s.log.Warn("event schrijven mislukt", "action", e.Action, "err", err)
	}
}

// friendlyWrite maakt van een weigering van Zabbix een uitleg.
func friendlyWrite(err error) error {
	m := err.Error()
	switch {
	case strings.Contains(m, "No permissions") || strings.Contains(m, "no permissions"):
		return &FieldError{"het API-token mag dit niet in Zabbix: geef de gebruiker van het token schrijfrechten op de hostgroep en de rol-rechten om problemen te bevestigen, te sluiten en onderhoud te zetten"}
	case strings.Contains(m, "cannot be closed") || strings.Contains(m, "manual close") || strings.Contains(m, "Cannot close problem"):
		return &FieldError{"deze trigger laat handmatig sluiten niet toe; bevestig zonder te sluiten, of zet in Zabbix bij de trigger 'Allow manual close' aan"}
	}
	return err
}
