package agent

// Patroni beheren via zijn REST-API op deze node: overschakelen, een
// replica opnieuw opbouwen, herstarten en de instellingen van PostgreSQL in
// etcd. Lezen kan zonder wachtwoord; voor de rest leest de agent het
// wachtwoord uit /etc/patroni/clusterforge.env, dat alleen root leest. Het
// wachtwoord gaat nooit naar de server en staat niet in een foutmelding.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	// patroniEnvFile heeft de wachtwoorden van Patroni, zoals ClusterForge
	// het onderdeel postgresql-patroni uitrolt.
	patroniEnvFile = "/etc/patroni/clusterforge.env"
	// patroniRESTUser is de gebruiker van de REST-API in clusterforge.yml.
	patroniRESTUser = "clusterforge"
)

// patroniMember is een naam van een node in Patroni: zijn hostname.
var patroniMember = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// PatroniOp voert een DBPatroniOp uit.
func (a *Agent) PatroniOp(ctx context.Context, req *protocol.DBPatroniRequest) protocol.DBResponse {
	fail := func(format string, args ...any) protocol.DBResponse {
		return protocol.DBResponse{Error: fmt.Sprintf(format, args...), Servers: []protocol.DBServer{}}
	}
	if req == nil {
		return fail("verzoek zonder patroni")
	}
	var res protocol.DBPatroniResult
	var err error
	switch req.Action {
	case protocol.DBPatroniSwitchover:
		res, err = a.patroniSwitchover(ctx, req.Leader, req.Candidate)
	case protocol.DBPatroniReinit:
		res, err = a.patroniReinit(ctx)
	case protocol.DBPatroniRestart:
		res, err = a.patroniRestart(ctx, req.Pending)
	case protocol.DBPatroniConfig:
		res, err = a.patroniConfig(ctx, req.Parameters)
	default:
		return fail("onbekende actie voor Patroni: %q", req.Action)
	}
	if err != nil {
		return fail("%s", err.Error())
	}
	return protocol.DBResponse{Servers: []protocol.DBServer{}, Patroni: &res}
}

// patroniPassword leest het wachtwoord van de REST-API uit clusterforge.env.
func (a *Agent) patroniPassword() (user, password string, err error) {
	p, err := a.path(patroniEnvFile)
	if err != nil {
		return "", "", err
	}
	f, err := os.Open(p)
	if err != nil {
		return "", "", fmt.Errorf("het wachtwoord van Patroni staat niet in %s; rolde ClusterForge Patroni hier uit?", patroniEnvFile)
	}
	defer func() { _ = f.Close() }()
	user = patroniRESTUser
	sc := bufio.NewScanner(io.LimitReader(f, 64<<10))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		switch k {
		case "PATRONI_RESTAPI_USERNAME":
			user = v
		case "PATRONI_RESTAPI_PASSWORD":
			password = v
		}
	}
	if password == "" {
		return "", "", fmt.Errorf("PATRONI_RESTAPI_PASSWORD ontbreekt in %s", patroniEnvFile)
	}
	return user, password, nil
}

// patroniCall stuurt een verzoek met wachtwoord naar de REST-API van Patroni
// op deze node, met de statuscode en hoogstens 64 KiB van het antwoord.
func (a *Agent) patroniCall(ctx context.Context, method, path string, body any) (int, []byte, error) {
	user, password, err := a.patroniPassword()
	if err != nil {
		return 0, nil, err
	}
	base := a.PatroniURL
	if base == "" {
		base = DefaultPatroniURL
	}
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, r)
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth(user, password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, fmt.Errorf("Patroni antwoordde niet op tijd op %s %s", method, path) //nolint:staticcheck // zin voor de gebruiker
		}
		return 0, nil, errors.New("de REST-API van Patroni op " + base + " antwoordt niet")
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusUnauthorized {
		return resp.StatusCode, nil, fmt.Errorf("Patroni weigert het wachtwoord uit %s", patroniEnvFile) //nolint:staticcheck // zin voor de gebruiker
	}
	return resp.StatusCode, out, nil
}

// patroniText maakt van een antwoord van Patroni één regel.
func patroniText(b []byte) string {
	s := strings.TrimSpace(string(b))
	var v struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &v) == nil && v.Message != "" {
		s = v.Message
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// patroniSwitchover maakt candidate de primary in plaats van leader. Patroni
// antwoordt pas als de nieuwe primary er is, of het niet lukte.
func (a *Agent) patroniSwitchover(ctx context.Context, leader, candidate string) (protocol.DBPatroniResult, error) {
	if !patroniMember.MatchString(leader) || !patroniMember.MatchString(candidate) || leader == candidate {
		return protocol.DBPatroniResult{}, fmt.Errorf("ongeldige switchover van %q naar %q", leader, candidate)
	}
	code, body, err := a.patroniCall(ctx, http.MethodPost, "/switchover", map[string]string{"leader": leader, "candidate": candidate})
	if err != nil {
		return protocol.DBPatroniResult{}, err
	}
	msg := patroniText(body)
	if code != http.StatusOK {
		return protocol.DBPatroniResult{}, fmt.Errorf("Patroni schakelde niet over (HTTP %d): %s", code, msg) //nolint:staticcheck // zin voor de gebruiker
	}
	return protocol.DBPatroniResult{Message: msg, Changed: true}, nil
}

// patroniReinit bouwt deze replica opnieuw op. Patroni begint en antwoordt
// meteen; de kopie loopt daarna verder.
func (a *Agent) patroniReinit(ctx context.Context) (protocol.DBPatroniResult, error) {
	code, body, err := a.patroniCall(ctx, http.MethodPost, "/reinitialize", map[string]bool{"force": true})
	if err != nil {
		return protocol.DBPatroniResult{}, err
	}
	msg := patroniText(body)
	if code != http.StatusOK {
		return protocol.DBPatroniResult{}, fmt.Errorf("Patroni begon niet opnieuw (HTTP %d): %s", code, msg) //nolint:staticcheck // zin voor de gebruiker
	}
	return protocol.DBPatroniResult{Message: msg, Changed: true}, nil
}

// patroniRestart herstart PostgreSQL op deze node. Met pending alleen als
// Patroni zegt dat een instelling het nodig heeft.
func (a *Agent) patroniRestart(ctx context.Context, pending bool) (protocol.DBPatroniResult, error) {
	if pending {
		st := a.patroni(ctx)
		if st.Error != "" {
			return protocol.DBPatroniResult{}, errors.New(st.Error)
		}
		if !st.PendingRestart {
			return protocol.DBPatroniResult{Message: "geen herstart nodig"}, nil
		}
	}
	code, body, err := a.patroniCall(ctx, http.MethodPost, "/restart", map[string]bool{"restart_pending": pending})
	if err != nil {
		return protocol.DBPatroniResult{}, err
	}
	msg := patroniText(body)
	if code != http.StatusOK {
		return protocol.DBPatroniResult{}, fmt.Errorf("PostgreSQL herstartte niet (HTTP %d): %s", code, msg)
	}
	return protocol.DBPatroniResult{Message: msg, Changed: true}, nil
}

// patroniConfig zet parameters in de instellingen van het cluster in etcd,
// voor zover ze anders zijn, en geeft terug wat er daarna staat.
func (a *Agent) patroniConfig(ctx context.Context, want map[string]string) (protocol.DBPatroniResult, error) {
	for k, v := range want {
		if !slices.Contains(protocol.DBPatroniParameters, k) {
			return protocol.DBPatroniResult{}, fmt.Errorf("ClusterForge zet %q niet in Patroni", k)
		}
		if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 1_000_000 {
			return protocol.DBPatroniResult{}, fmt.Errorf("ongeldige waarde voor %s: %q", k, v)
		}
	}
	code, body, err := a.patroniCall(ctx, http.MethodGet, "/config", nil)
	if err != nil {
		return protocol.DBPatroniResult{}, err
	}
	if code != http.StatusOK {
		return protocol.DBPatroniResult{}, fmt.Errorf("de instellingen van Patroni lezen lukte niet (HTTP %d): %s", code, patroniText(body))
	}
	now, err := patroniParameters(body)
	if err != nil {
		return protocol.DBPatroniResult{}, err
	}
	patch := map[string]any{}
	for _, k := range slices.Sorted(maps.Keys(want)) {
		if now[k] != want[k] {
			n, _ := strconv.Atoi(want[k])
			patch[k] = n
		}
	}
	res := protocol.DBPatroniResult{Parameters: now}
	if len(patch) == 0 {
		res.Message = "de instellingen stonden al zo"
		return res, nil
	}
	code, body, err = a.patroniCall(ctx, http.MethodPatch, "/config", map[string]any{"postgresql": map[string]any{"parameters": patch}})
	if err != nil {
		return protocol.DBPatroniResult{}, err
	}
	if code != http.StatusOK {
		return protocol.DBPatroniResult{}, fmt.Errorf("Patroni nam de instellingen niet aan (HTTP %d): %s", code, patroniText(body)) //nolint:staticcheck // zin voor de gebruiker
	}
	if res.Parameters, err = patroniParameters(body); err != nil {
		return protocol.DBPatroniResult{}, err
	}
	var set []string
	for _, k := range slices.Sorted(maps.Keys(patch)) {
		set = append(set, fmt.Sprintf("%s = %s", k, res.Parameters[k]))
	}
	res.Message, res.Changed = "in etcd gezet: "+strings.Join(set, ", "), true
	return res, nil
}

// patroniParameters haalt de parameters uit DBPatroniParameters uit de
// instellingen van Patroni, als tekst.
func patroniParameters(body []byte) (map[string]string, error) {
	var v struct {
		PostgreSQL struct {
			Parameters map[string]any `json:"parameters"`
		} `json:"postgresql"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, errors.New("onverwachte instellingen van Patroni")
	}
	out := map[string]string{}
	for _, k := range protocol.DBPatroniParameters {
		switch x := v.PostgreSQL.Parameters[k].(type) {
		case float64:
			out[k] = strconv.FormatInt(int64(x), 10)
		case string:
			out[k] = x
		}
	}
	return out, nil
}

// patroniLag leest in /cluster hoeveel WAL deze node als replica nog niet
// heeft; nil als Patroni het niet zegt.
func (a *Agent) patroniLag(ctx context.Context, name string) *int64 {
	code, body, err := a.patroniGet(ctx, "/cluster", 3*time.Second)
	if err != nil || code != http.StatusOK {
		return nil
	}
	var v struct {
		Members []struct {
			Name string `json:"name"`
			Lag  any    `json:"lag"`
		} `json:"members"`
	}
	if json.Unmarshal(body, &v) != nil {
		return nil
	}
	for _, m := range v.Members {
		if m.Name != name {
			continue
		}
		if f, ok := m.Lag.(float64); ok && f >= 0 {
			n := int64(f)
			return &n
		}
	}
	return nil
}
