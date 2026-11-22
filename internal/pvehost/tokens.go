package pvehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"sync"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// NewToken is een nieuw API-token van Proxmox (fase 26 M6). De waarde
// toont Proxmox maar één keer; ClusterForge geeft hem meteen terug en
// bewaart hem nergens: niet in een taak, niet in een logboek.
type NewToken struct {
	Datacenter string
	UserID     string
	TokenID    string
	Comment    string
	// Expire is een Unix-tijd; 0 is nooit.
	Expire int64
	// Privsep: het token heeft eigen rechten (aanbevolen) in plaats van
	// die van de gebruiker.
	Privsep bool
}

// CreatedToken is wat Jonas één keer te zien krijgt.
type CreatedToken struct {
	FullTokenID string `json:"full_tokenid"`
	Value       string `json:"value"`
}

var (
	tokenUser = regexp.MustCompile(`^` + segUser + `$`)
	tokenID   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{1,63}$`)
)

func (t NewToken) validate() error {
	if !tokenUser.MatchString(t.UserID) {
		return ValidationError{"ongeldige gebruiker; die ziet eruit als naam@pve of naam@pam"}
	}
	if !tokenID.MatchString(t.TokenID) {
		return ValidationError{"de naam van een token begint met een letter en bevat alleen letters, cijfers, . - en _"}
	}
	if len(t.Comment) > 200 {
		return ValidationError{"de opmerking is te lang"}
	}
	if t.Expire < 0 {
		return ValidationError{"ongeldige vervaldatum"}
	}
	return nil
}

// CreateToken maakt het token op een host met een agent die het antwoord
// stil kan houden, en wacht op het antwoord (meestal een seconde).
func (s *Service) CreateToken(ctx context.Context, actor events.Actor, t NewToken) (CreatedToken, error) {
	if err := t.validate(); err != nil {
		return CreatedToken{}, err
	}
	dc, err := s.Datacenter(ctx, t.Datacenter)
	if err != nil {
		return CreatedToken{}, err
	}
	var h *Host
	for i := range dc.Hosts {
		if dc.Hosts[i].APIReady() == "" && dc.Hosts[i].Protocol >= protocol.PVEQuietSince {
			h = &dc.Hosts[i]
			break
		}
	}
	if h == nil {
		return CreatedToken{}, ValidationError{fmt.Sprintf("een API-token maken vraagt een agent met protocol %d op minstens één host; werk de agents bij via Nodes → Agents bijwerken", protocol.PVEQuietSince)}
	}
	params := map[string]string{"privsep": "0"}
	if t.Privsep {
		params["privsep"] = "1"
	}
	if t.Comment != "" {
		params["comment"] = t.Comment
	}
	if t.Expire > 0 {
		params["expire"] = strconv.FormatInt(t.Expire, 10)
	}
	call := protocol.PVEAPICall{Method: "create", Path: "/access/users/" + t.UserID + "/token/" + t.TokenID, Params: params, Quiet: true}
	if err := call.Validate(); err != nil {
		return CreatedToken{}, ValidationError{err.Error()}
	}
	title := "API-token " + t.UserID + "!" + t.TokenID + " maken"
	log := &memLog{}
	res, err := s.Do(ctx, log, &memState{}, h.NodeID, protocol.PVERun{Action: protocol.PVEAPI, API: &call}, title)
	if err != nil {
		return CreatedToken{}, err
	}
	if res.State != protocol.RunOK {
		return CreatedToken{}, errors.New(firstNonEmpty(res.Error, "het token is niet gemaakt"))
	}
	var out struct {
		Full  string `json:"full-tokenid"`
		Value string `json:"value"`
	}
	if res.PVE == nil || json.Unmarshal(res.PVE.Data, &out) != nil || out.Value == "" {
		return CreatedToken{}, errors.New("geen tokenwaarde van Proxmox; kijk of het token toch bestaat en maak het anders opnieuw")
	}
	s.audit(ctx, actor, *h, "pvehost.token_created", map[string]any{
		"datacenter": t.Datacenter, "token": out.Full, "privsep": t.Privsep, "expire": t.Expire,
	})
	s.cache.clear()
	return CreatedToken{FullTokenID: firstNonEmpty(out.Full, t.UserID+"!"+t.TokenID), Value: out.Value}, nil
}

// memLog en memState laten een run lopen zonder taak: niets komt in de
// database.
type memLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *memLog) Logf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *memLog) Flush(context.Context) error { return nil }

type memState struct{ data []byte }

func (m *memState) State(v any) bool {
	return m.data != nil && json.Unmarshal(m.data, v) == nil
}

func (m *memState) SetState(_ context.Context, v any) error {
	b, err := json.Marshal(v)
	m.data = b
	return err
}
