package pbs

// API-tokens van PBS (fase 32 M4). PBS toont de waarde van een token maar
// één keer; ClusterForge geeft hem meteen terug en bewaart hem nergens:
// niet in een taak, niet in een logboek. De koppeling met Proxmox (M5)
// gebruikt hetzelfde.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"sync"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

type NewToken struct {
	UserID  string `json:"userid"`
	TokenID string `json:"tokenid"`
	Comment string `json:"comment"`
	// Expire is een Unix-tijd; 0 is nooit.
	Expire int64 `json:"expire"`
	// Regenerate maakt een nieuw geheim voor een bestaand token.
	Regenerate bool `json:"regenerate"`
}

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
		return ValidationError{"ongeldige gebruiker; die ziet eruit als naam@pbs of naam@pam"}
	}
	if !tokenID.MatchString(t.TokenID) {
		return ValidationError{"de naam van een token begint met een letter en bevat alleen letters, cijfers, . - en _"}
	}
	if len(t.Comment) > 200 || t.Expire < 0 {
		return ValidationError{"ongeldige opmerking of vervaldatum"}
	}
	return nil
}

// CreateToken maakt (of vernieuwt) een token en wacht op het antwoord.
func (s *Service) CreateToken(ctx context.Context, actor events.Actor, id uuid.UUID, t NewToken) (CreatedToken, error) {
	if err := t.validate(); err != nil {
		return CreatedToken{}, err
	}
	params := map[string]string{}
	if t.Comment != "" {
		params["comment"] = t.Comment
	}
	if t.Expire > 0 {
		params["expire"] = strconv.FormatInt(t.Expire, 10)
	}
	method := "create"
	if t.Regenerate {
		method, params = "set", map[string]string{"regenerate": "1"}
	}
	path := "/access/users/" + t.UserID + "/token/" + t.TokenID
	data, err := s.quietCall(ctx, id, method, path, params)
	if err != nil {
		return CreatedToken{}, err
	}
	var out struct {
		Full   string `json:"tokenid"`
		Full2  string `json:"full-tokenid"`
		Value  string `json:"value"`
		Secret string `json:"secret"`
	}
	if json.Unmarshal(data, &out) != nil || (out.Value == "" && out.Secret == "") {
		return CreatedToken{}, errors.New("geen tokenwaarde van PBS; kijk of het token toch bestaat en vernieuw het anders")
	}
	full := firstNonEmpty(out.Full, out.Full2, t.UserID+"!"+t.TokenID)
	if err := s.ev.Write(ctx, nil, events.Event{
		Actor: actor, SubjectType: "node", SubjectID: id.String(), Action: "pbs.token_created",
		Payload: map[string]any{"token": full, "regenerate": t.Regenerate, "expire": t.Expire},
	}); err != nil {
		s.log.Warn("event schrijven mislukt", "err", err)
	}
	return CreatedToken{FullTokenID: full, Value: firstNonEmpty(out.Value, out.Secret)}, nil
}

// quietCall doet een aanroep zonder taak en geeft het antwoord terug; voor
// antwoorden met een geheim.
func (s *Service) quietCall(ctx context.Context, id uuid.UUID, method, path string, params map[string]string) (json.RawMessage, error) {
	v, err := s.Server(ctx, id)
	if err != nil {
		return nil, err
	}
	if v.Why != "" {
		return nil, ValidationError{v.Why}
	}
	call := protocol.PVEAPICall{Method: method, Path: path, Params: params, Quiet: true}
	if err := call.ValidatePBS(); err != nil {
		return nil, ValidationError{err.Error()}
	}
	if _, err := check(method, path, params); err != nil {
		return nil, ValidationError{err.Error()}
	}
	res, err := s.Do(ctx, &memLog{}, &memState{}, id, call, path)
	if err != nil {
		return nil, err
	}
	s.cache.clear()
	if res.State != protocol.RunOK {
		return nil, errors.New(firstNonEmpty(res.Error, "PBS weigerde de aanroep"))
	}
	if res.PVE == nil || len(res.PVE.Data) == 0 {
		return nil, errors.New("PBS gaf geen antwoord")
	}
	return res.PVE.Data, nil
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
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
