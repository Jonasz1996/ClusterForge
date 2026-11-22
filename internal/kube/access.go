package kube

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Een kubeconfig voor kubectl, k9s, Headlamp of Lens op je eigen pc. Elke
// kubeconfig heeft een eigen ServiceAccount in de namespace clusterforge,
// met de ClusterRole cluster-admin of view, en een token dat verloopt.
// Intrekken haalt het ServiceAccount weg; dan weigert de API het token
// meteen. ClusterForge bewaart het token niet.

// AccessDefaultTTL is hoe lang een kubeconfig standaard geldt, en
// AccessMaxDays hoeveel dagen hoogstens.
const (
	AccessDefaultTTL = 7 * 24 * time.Hour
	AccessMaxDays    = 30
)

// AccessInput is een vraag om een kubeconfig.
type AccessInput struct {
	UserID   uuid.UUID
	Username string
	// Name is waarvoor de kubeconfig is, zoals laptop.
	Name string
	// Role is protocol.KubeAccessAdmin of protocol.KubeAccessView.
	Role string
	TTL  time.Duration
}

// Issued is een uitgegeven kubeconfig. Kubeconfig is er alleen nu: het
// token erin bewaart ClusterForge niet.
type Issued struct {
	Access     store.KubeAccess
	Server     string
	Kubeconfig []byte
}

// ValidateAccess controleert de naam, de rol en de geldigheid.
func ValidateAccess(in AccessInput) error {
	name := strings.TrimSpace(in.Name)
	switch {
	case name == "":
		return &InputError{Msg: "geef de kubeconfig een naam, zoals laptop"}
	case len([]rune(name)) > 40:
		return &InputError{Msg: "de naam is hoogstens 40 tekens"}
	case strings.IndexFunc(name, unicode.IsControl) >= 0:
		return &InputError{Msg: "de naam bevat een ongeldig teken"}
	}
	if _, ok := protocol.KubeAccessRoles[in.Role]; !ok {
		return &InputError{Msg: "de rechten zijn admin of view"}
	}
	if in.TTL < 24*time.Hour || in.TTL > AccessMaxDays*24*time.Hour {
		return &InputError{Msg: fmt.Sprintf("een kubeconfig geldt 1 tot %d dagen", AccessMaxDays)}
	}
	return nil
}

// InputError is een fout in wat de gebruiker invulde.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// AccountName maakt de naam van het ServiceAccount: cf-, de gebruiker in
// kleine letters, en zes tekens van de id, zodat kubectl get
// serviceaccounts -n clusterforge toont van wie elk is.
func AccountName(username string, id uuid.UUID) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(username) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	user := strings.TrimRight(b.String(), "-")
	if len(user) > 40 {
		user = strings.TrimRight(user[:40], "-")
	}
	if user == "" {
		user = "gebruiker"
	}
	return "cf-" + user + "-" + strings.ReplaceAll(id.String(), "-", "")[:6]
}

// Issue geeft een kubeconfig. Eerst komt de kubeconfig in kube_access, dan
// vraagt de agent het token; lukt dat niet, dan haalt hij weg wat er al
// stond, of doet de sync dat later.
func (s *Service) Issue(ctx context.Context, c store.Cluster, in AccessInput) (Issued, error) {
	if err := ValidateAccess(in); err != nil {
		return Issued{}, err
	}
	// Zonder agent die het kan vragen, hoeft er niets in kube_access.
	if _, err := s.candidates(ctx, c.ID); err != nil {
		return Issued{}, err
	}
	id := uuid.New()
	account := AccountName(in.Username, id)
	row, err := s.q.InsertKubeAccess(ctx, store.InsertKubeAccessParams{
		ID: id, ClusterID: c.ID, UserID: in.UserID, Name: strings.TrimSpace(in.Name), Role: in.Role, Account: account,
		ExpiresAt: s.Now().Add(in.TTL),
	})
	if err != nil {
		return Issued{}, err
	}
	res, via, err := s.sendVia(ctx, c.ID, protocol.KubeRequest{Op: protocol.KubeToken, Token: &protocol.KubeTokenRequest{
		Account: account, Role: in.Role, Seconds: int64(in.TTL / time.Second), Owner: in.Username,
	}})
	var tr protocol.KubeTokenResult
	if err == nil && (json.Unmarshal(res.Body, &tr) != nil || tr.Token == "") {
		err = &APIError{Status: res.Status, Message: "de agent gaf geen token terug"}
	}
	if err != nil {
		// Het ServiceAccount kan er al staan zonder dat iemand het token
		// kreeg; behalve als geen agent het verzoek kende.
		cctx := context.WithoutCancel(ctx)
		var ue *UnavailableError
		if errors.As(err, &ue) && (ue.TooOld || ue.NoAgent) || s.removeAccount(cctx, c.ID, id, account) {
			_ = s.q.DeleteKubeAccess(cctx, id)
		} else if _, rerr := s.q.RevokeKubeAccess(cctx, store.RevokeKubeAccessParams{ID: id, RevokeReason: "uitgeven mislukt"}); rerr != nil {
			s.log.Warn("mislukte kubeconfig intrekken mislukt", "id", id, "err", rerr)
		}
		return Issued{}, err
	}
	if !tr.Expires.IsZero() {
		row.ExpiresAt = tr.Expires
		if err := s.q.SetKubeAccessExpires(ctx, store.SetKubeAccessExpiresParams{ID: id, ExpiresAt: tr.Expires}); err != nil {
			return Issued{}, err
		}
	}
	server, err := s.apiServer(ctx, c.ID, via)
	if err != nil {
		return Issued{}, err
	}
	if s.ev != nil {
		if err := s.ev.Write(context.WithoutCancel(ctx), nil, events.Event{
			Actor: events.User(in.UserID), SubjectType: "cluster", SubjectID: c.ID.String(), ClusterID: &c.ID,
			Action: "kube.access_issued", Payload: map[string]any{
				"access_id": id.String(), "name": row.Name, "role": in.Role, "account": account, "user": in.Username,
				"expires_at": row.ExpiresAt.UTC().Format(time.RFC3339),
			},
		}); err != nil {
			return Issued{}, err
		}
	}
	return Issued{
		Access: row, Server: server,
		Kubeconfig: Kubeconfig(server, tr.CA, c.Slug, in.Username+"@"+c.Slug, tr.Token),
	}, nil
}

// apiServer is het adres van de API voor kubectl: het VIP op poort 6443,
// of zonder VIP de server-node die het token gaf. k3s zet beide in zijn
// certificaat.
func (s *Service) apiServer(ctx context.Context, clusterID uuid.UUID, via bridgeNode) (string, error) {
	vips, err := s.q.ListClusterVIPOwners(ctx, clusterID)
	if err != nil {
		return "", err
	}
	host := via.Hostname
	switch {
	case len(vips) > 0:
		host = vips[0].Address.String()
	case via.IP != nil:
		host = via.IP.String()
	}
	return "https://" + net.JoinHostPort(host, "6443"), nil
}

// Kubeconfig schrijft een kubeconfig met één cluster, één gebruiker met een
// token, en een context die de standaard is.
func Kubeconfig(server string, ca []byte, cluster, user, token string) []byte {
	q := func(v string) string {
		// Een JSON-string is ook een geldige YAML-string.
		b, _ := json.Marshal(v)
		return string(b)
	}
	return []byte(`apiVersion: v1
kind: Config
clusters:
- name: ` + q(cluster) + `
  cluster:
    server: ` + q(server) + `
    certificate-authority-data: ` + base64.StdEncoding.EncodeToString(ca) + `
users:
- name: ` + q(user) + `
  user:
    token: ` + q(token) + `
contexts:
- name: ` + q(cluster) + `
  context:
    cluster: ` + q(cluster) + `
    user: ` + q(user) + `
    namespace: default
current-context: ` + q(cluster) + `
`)
}

// AccessRow is een kubeconfig met de namen erbij.
type AccessRow struct {
	store.KubeAccess
	Username      string
	RevokedByName string
}

// Access geeft één kubeconfig.
func (s *Service) Access(ctx context.Context, id uuid.UUID) (AccessRow, error) {
	r, err := s.q.GetKubeAccess(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccessRow{}, ErrNotFound
	}
	return AccessRow{r.KubeAccess, r.Username, r.RevokedByName}, err
}

// ListAccess geeft de kubeconfigs van een cluster; met user alleen die van
// die gebruiker.
func (s *Service) ListAccess(ctx context.Context, clusterID uuid.UUID, user *uuid.UUID) ([]AccessRow, error) {
	rows, err := s.q.ListKubeAccess(ctx, store.ListKubeAccessParams{ClusterID: clusterID, UserID: user})
	if err != nil {
		return nil, err
	}
	out := make([]AccessRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, AccessRow{r.KubeAccess, r.Username, r.RevokedByName})
	}
	return out, nil
}

// Revoke trekt een kubeconfig in en haalt het ServiceAccount weg. Lukt dat
// nu niet, omdat de API niet antwoordt, dan staat de fout bij de
// kubeconfig en probeert de sync het elke 30 seconden opnieuw. removed zegt
// of het ServiceAccount al weg is.
func (s *Service) Revoke(ctx context.Context, actor events.Actor, by *uuid.UUID, id uuid.UUID, reason string) (removed bool, err error) {
	a, err := s.Access(ctx, id)
	if err != nil {
		return false, err
	}
	if a.RevokedAt == nil {
		if _, err := s.q.RevokeKubeAccess(ctx, store.RevokeKubeAccessParams{ID: id, RevokedBy: by, RevokeReason: reason}); err != nil &&
			!errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
		if err := s.revoked(context.WithoutCancel(ctx), actor, a, reason); err != nil {
			return false, err
		}
	}
	if a.RemovedAt != nil {
		return true, nil
	}
	return s.removeAccount(context.WithoutCancel(ctx), a.ClusterID, id, a.Account), nil
}

func (s *Service) revoked(ctx context.Context, actor events.Actor, a AccessRow, reason string) error {
	if s.ev == nil {
		return nil
	}
	payload := map[string]any{"access_id": a.ID.String(), "name": a.Name, "role": a.Role, "account": a.Account, "user": a.Username}
	if reason != "" {
		payload["reason"] = reason
	}
	return s.ev.Write(ctx, nil, events.Event{
		Actor: actor, SubjectType: "cluster", SubjectID: a.ClusterID.String(), ClusterID: &a.ClusterID,
		Action: "kube.access_revoked", Payload: payload,
	})
}

// removeAccount haalt het ServiceAccount van een kubeconfig weg, en zegt
// of dat lukte.
func (s *Service) removeAccount(ctx context.Context, clusterID, id uuid.UUID, account string) bool {
	_, err := s.send(ctx, clusterID, protocol.KubeRequest{Op: protocol.KubeToken, Token: &protocol.KubeTokenRequest{Account: account, Revoke: true}})
	if err != nil {
		if serr := s.q.SetKubeAccessRemoveError(ctx, store.SetKubeAccessRemoveErrorParams{ID: id, RemoveError: clip(err.Error(), 300)}); serr != nil {
			s.log.Warn("fout bij intrekken bewaren mislukt", "id", id, "err", serr)
		}
		return false
	}
	if err := s.q.SetKubeAccessRemoved(ctx, id); err != nil {
		s.log.Warn("ingetrokken kubeconfig bewaren mislukt", "id", id, "err", err)
	}
	return true
}

// cleanAccess haalt de ServiceAccounts weg van kubeconfigs die verlopen of
// ingetrokken zijn, en trekt die in van een gebruiker die uitgeschakeld is
// of bij een admin-kubeconfig geen admin meer is. De sync doet dit na elke
// geslaagde ronde.
func (s *Service) cleanAccess(ctx context.Context, clusterID uuid.UUID) {
	rows, err := s.q.ListKubeAccessToRemove(ctx, clusterID)
	if err != nil {
		s.log.Warn("kubeconfigs om in te trekken lezen mislukt", "cluster", clusterID, "err", err)
		return
	}
	for _, r := range rows {
		if r.Reason != "" {
			if _, err := s.Revoke(ctx, events.System(), nil, r.ID, r.Reason); err != nil {
				s.log.Warn("kubeconfig intrekken mislukt", "id", r.ID, "err", err)
			}
			continue
		}
		s.removeAccount(ctx, clusterID, r.ID, r.Account)
	}
}
