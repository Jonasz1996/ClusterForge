package pbs

// Fase 32 M5: een PBS met één klik als storage aan een Proxmox-cluster
// koppelen. ClusterForge maakt op de PBS een eigen gebruiker en token voor
// dat cluster, geeft het token alleen rechten op de gekozen datastore, en
// zet de storage in Proxmox met de fingerprint van de PBS. De tokenwaarde
// gaat versleuteld in de taak van Proxmox en komt nooit in een log.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// StorageAdder zet een storage in een Proxmox-cluster (internal/pvehost).
type StorageAdder interface {
	AddStorage(ctx context.Context, actor events.Actor, datacenter string, params map[string]string) (store.Job, error)
}

// LinkInput is wat de GUI vraagt.
type LinkInput struct {
	Datacenter string `json:"datacenter"`
	// DatacenterName is de naam van het cluster, voor de gebruiker op PBS.
	DatacenterName string `json:"datacenter_name"`
	Datastore      string `json:"datastore"`
	// Storage is de naam in Proxmox, zoals pbs-store1.
	Storage   string `json:"storage"`
	Namespace string `json:"namespace"`
	// Server is het adres waarop Proxmox de PBS bereikt.
	Server string `json:"server"`
	// Role is DatastoreBackup (alleen back-ups maken en terugzetten) of
	// DatastorePowerUser (ook prune vanuit Proxmox).
	Role string `json:"role"`
}

var (
	linkStorage = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{1,40}$`)
	linkServer  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:-]{0,252}$`)
	linkNS      = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9._-]{0,30})(/[A-Za-z0-9_][A-Za-z0-9._-]{0,30}){0,7}$`)
	slugRe      = regexp.MustCompile(`[^a-z0-9]+`)
)

// Link koppelt de PBS id aan een Proxmox-cluster; de taak is die van Proxmox.
func (s *Service) Link(ctx context.Context, actor events.Actor, id uuid.UUID, in LinkInput) (store.Job, error) {
	if s.Hosts == nil {
		return store.Job{}, ValidationError{"deze server kan geen Proxmox-storage zetten"}
	}
	v, err := s.Server(ctx, id)
	if err != nil {
		return store.Job{}, err
	}
	if v.Why != "" {
		return store.Job{}, ValidationError{v.Why}
	}
	switch {
	case in.Datacenter == "":
		return store.Job{}, ValidationError{"kies een Proxmox-cluster"}
	case !contains(v.Datastores, in.Datastore):
		return store.Job{}, ValidationError{fmt.Sprintf("datastore %q staat niet op %s", in.Datastore, v.Hostname)}
	case !linkStorage.MatchString(in.Storage):
		return store.Job{}, ValidationError{"de naam van de storage begint met een letter en bevat alleen letters, cijfers, . - en _"}
	case !linkServer.MatchString(in.Server):
		return store.Job{}, ValidationError{"ongeldig adres van de PBS"}
	case in.Namespace != "" && !linkNS.MatchString(in.Namespace):
		return store.Job{}, ValidationError{"ongeldige namespace"}
	}
	role := in.Role
	if role != "DatastorePowerUser" {
		role = "DatastoreBackup"
	}
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(firstNonEmpty(in.DatacenterName, in.Datacenter)), "-"), "-")
	if len(slug) > 24 {
		slug = slug[:24]
	}
	if slug == "" {
		slug = "pve"
	}
	user := "pve-" + slug + "@pbs"
	token := "clusterforge"

	// 1. De fingerprint van de PBS.
	var st NodeStatus
	if err := s.GetInto(ctx, id, "/nodes/localhost/status", nil, &st); err != nil {
		return store.Job{}, fmt.Errorf("de fingerprint van de PBS lezen mislukte: %w", err)
	}
	if st.Info.Fingerprint == "" {
		return store.Job{}, ValidationError{"PBS gaf geen fingerprint van zijn certificaat"}
	}
	// 2. Gebruiker en token.
	var users []struct {
		UserID string `json:"userid"`
		Tokens []struct {
			TokenID string `json:"tokenid"`
		} `json:"tokens"`
	}
	if err := s.GetInto(ctx, id, "/access/users", map[string]string{"include_tokens": "1"}, &users); err != nil {
		return store.Job{}, fmt.Errorf("de gebruikers van de PBS lezen mislukte: %w", err)
	}
	hasUser, hasToken := false, false
	for _, u := range users {
		if u.UserID == user {
			hasUser = true
			for _, t := range u.Tokens {
				if t.TokenID == user+"!"+token {
					hasToken = true
				}
			}
		}
	}
	if !hasUser {
		if _, err := s.quietCall(ctx, id, "create", "/access/users", map[string]string{"userid": user, "comment": "Proxmox-cluster " + firstNonEmpty(in.DatacenterName, in.Datacenter) + " (ClusterForge)"}); err != nil && !strings.Contains(err.Error(), "PBS gaf geen antwoord") {
			return store.Job{}, fmt.Errorf("gebruiker %s maken mislukte: %w", user, err)
		}
	}
	created, err := s.CreateToken(ctx, actor, id, NewToken{UserID: user, TokenID: token, Comment: "Proxmox-storage " + in.Storage, Regenerate: hasToken})
	if err != nil {
		return store.Job{}, fmt.Errorf("token maken mislukte: %w", err)
	}
	// 3. Rechten, alleen op de datastore (of de namespace).
	aclPath := "/datastore/" + in.Datastore
	if in.Namespace != "" {
		aclPath += "/" + in.Namespace
	}
	if _, err := s.quietCall(ctx, id, "set", "/access/acl", map[string]string{"path": aclPath, "role": role, "auth-id": created.FullTokenID, "propagate": "1"}); err != nil && !strings.Contains(err.Error(), "PBS gaf geen antwoord") {
		return store.Job{}, fmt.Errorf("rechten geven mislukte: %w", err)
	}
	// 4. De storage in Proxmox.
	params := map[string]string{
		"storage": in.Storage, "type": "pbs", "server": in.Server, "datastore": in.Datastore,
		"username": created.FullTokenID, "password": created.Value, "fingerprint": st.Info.Fingerprint,
		"content": "backup",
	}
	if in.Namespace != "" {
		params["namespace"] = in.Namespace
	}
	j, err := s.Hosts.AddStorage(ctx, actor, in.Datacenter, params)
	if err != nil {
		return store.Job{}, err
	}
	shown, _ := json.Marshal(map[string]string{"datacenter": in.Datacenter, "storage": in.Storage, "datastore": in.Datastore, "token": created.FullTokenID, "role": role})
	if err := s.ev.Write(ctx, nil, events.Event{
		Actor: actor, SubjectType: "node", SubjectID: id.String(), Action: "pbs.linked",
		Payload: map[string]any{"link": json.RawMessage(shown), "job_id": j.ID.String()},
	}); err != nil {
		s.log.Warn("event schrijven mislukt", "err", err)
	}
	return j, nil
}

func contains(list []string, x string) bool {
	for _, v := range list {
		if v == x {
			return true
		}
	}
	return false
}
