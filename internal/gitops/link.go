package gitops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.yaml.in/yaml/v3"

	"github.com/Jonasz1996/clusterforge/internal/drift"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// exportOf leest een cluster uit een template voor de export.
func (s *Service) exportOf(ctx context.Context, clusterID uuid.UUID) (store.Cluster, *Cluster, *templates.Template, error) {
	c, err := s.q.GetCluster(ctx, clusterID)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, nil, nil, ErrNotFound
	}
	if err != nil {
		return c, nil, nil, err
	}
	cl := s.clusterOf(ctx, store.ListGitClustersRow{
		ID: c.ID, Slug: c.Slug, Name: c.Name, Description: c.Description, Environment: c.Environment, Tags: c.Tags, Type: c.Type,
		Spec: c.Spec, SpecRevision: c.SpecRevision, TemplateName: c.TemplateName, TemplateVersion: c.TemplateVersion, GitRepoID: c.GitRepoID,
	})
	if cl.Spec == nil {
		return c, nil, nil, &ConflictError{Code: "not_template", Msg: "alleen een cluster uit een ingebouwde template kan in Git"}
	}
	tpl, ok := s.reg().Get(cl.Spec.Template.Name, cl.Spec.Template.Version)
	if !ok {
		return c, nil, nil, &ConflictError{Code: "not_template",
			Msg: fmt.Sprintf("%s %s zit niet in deze server", cl.Spec.Template.Name, cl.Spec.Template.Version)}
	}
	return c, cl, tpl, nil
}

// Export geeft cluster.yaml van een cluster uit een template: met de naam
// van de Proxmox-koppeling en de templateversie, en zonder geheimen.
func (s *Service) Export(ctx context.Context, clusterID uuid.UUID) (string, []byte, error) {
	_, cl, tpl, err := s.exportOf(ctx, clusterID)
	if err != nil {
		return "", nil, err
	}
	proxmox := ""
	if id := cl.Spec.Target.ProxmoxID; id != uuid.Nil {
		if conn, err := s.q.GetProxmoxConnection(ctx, id); err == nil {
			proxmox = conn.Name
		}
	}
	data, err := exportYAML(cl, tpl, proxmox)
	if err != nil {
		return "", nil, err
	}
	return cl.Slug + "-cluster.yaml", data, nil
}

// exportYAML schrijft het bestand in een vaste volgorde, met de parameters
// in de volgorde van de template.
func exportYAML(c *Cluster, tpl *templates.Template, proxmox string) ([]byte, error) {
	str := func(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: v, Tag: "!!str"} }
	num := func(n int) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: strconv.Itoa(n), Tag: "!!int"} }
	list := func(vs []string) *yaml.Node {
		n := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, v := range vs {
			n.Content = append(n.Content, str(v))
		}
		return n
	}
	mapping := func(pairs ...any) *yaml.Node {
		n := &yaml.Node{Kind: yaml.MappingNode}
		for i := 0; i+1 < len(pairs); i += 2 {
			n.Content = append(n.Content, str(pairs[i].(string)), pairs[i+1].(*yaml.Node))
		}
		return n
	}
	cluster := mapping("name", str(c.Name), "slug", str(c.Slug), "environment", str(c.Environment))
	if c.Description != "" {
		cluster.Content = append(cluster.Content, str("description"), str(c.Description))
	}
	cluster.Content = append(cluster.Content, str("tags"), list(c.Tags))

	params := &yaml.Node{Kind: yaml.MappingNode}
	for _, p := range tpl.Params {
		if p.Type == "secret" {
			continue
		}
		v := c.Spec.Params[p.Name]
		var val *yaml.Node
		switch x := v.(type) {
		case nil:
			val = &yaml.Node{Kind: yaml.ScalarNode, Value: "null", Tag: "!!null"}
		case bool:
			val = &yaml.Node{Kind: yaml.ScalarNode, Value: strconv.FormatBool(x), Tag: "!!bool"}
		case float64:
			val = num(int(x))
		case int:
			val = num(x)
		default:
			val = str(fmt.Sprint(x))
		}
		if p.Immutable {
			val.LineComment = "ligt vast"
		}
		params.Content = append(params.Content, str(p.Name), val)
	}

	t := c.Spec.Target
	target := mapping("proxmox", str(proxmox), "image_vmid", num(t.ImageVMID))
	add := func(k string, v *yaml.Node) { target.Content = append(target.Content, str(k), v) }
	if t.Storage != "" {
		add("storage", str(t.Storage))
	}
	add("bridge", str(or(t.Bridge, "vmbr0")))
	if t.VLAN != nil {
		add("vlan", num(*t.VLAN))
	}
	if t.Network == "static" {
		add("first_ip", str(t.FirstIP))
		add("gateway", str(t.Gateway))
		if len(t.DNS) > 0 {
			add("dns", list(t.DNS))
		}
	}
	if t.SSHKeys != "" {
		seq := &yaml.Node{Kind: yaml.SequenceNode}
		for _, k := range strings.Split(t.SSHKeys, "\n") {
			seq.Content = append(seq.Content, str(k))
		}
		add("ssh_keys", seq)
	}

	root := mapping(
		"clusterforge", num(Format),
		"cluster", cluster,
		"template", mapping("name", str(tpl.Name), "version", str(tpl.Version)),
		"params", params,
		"target", target,
	)
	root.HeadComment = "Beheerd door ClusterForge GitOps. Geheimen staan niet in dit bestand; target geldt alleen bij een nieuw cluster."
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// fileFor geeft de inhoud van het bestand van een slug uit de laatste
// scan. Is de cache leeg, zoals na een herstart, dan synchroniseert het
// eerst.
func (s *Service) fileFor(ctx context.Context, repo store.GitRepo, slug string) ([]byte, string, error) {
	p := filePath(repo.Path, slug)
	lookup := func() ([]byte, string, bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, e := range s.tree {
			if e.Path == p && e.Type == "blob" {
				b, ok := s.blobs[e.SHA]
				return b, e.SHA, ok
			}
		}
		return nil, "", s.treeOf != ""
	}
	b, sha, ok := lookup()
	if !ok {
		if err := s.Sync(ctx); err != nil {
			return nil, "", &ConflictError{Code: "git_unreachable", Msg: "de repository is niet te lezen: " + err.Error()}
		}
		b, sha, _ = lookup()
	}
	if b == nil {
		return nil, "", &ConflictError{Code: "git_missing",
			Msg: fmt.Sprintf("%s staat niet in de laatste commit van %s; commit de export en synchroniseer", p, repo.Branch)}
	}
	return b, sha, nil
}

// Link koppelt een cluster aan zijn bestand. Dat lukt alleen als het
// bestand precies gelijk is aan de export; daarna is Git de bron van
// waarheid.
func (s *Service) Link(ctx context.Context, actor events.Actor, clusterID uuid.UUID) error {
	repo, err := s.current(ctx)
	if err != nil {
		return err
	}
	if repo == nil {
		return &ConflictError{Code: "no_repo", Msg: "koppel eerst een repository bij GitOps"}
	}
	c, cl, tpl, err := s.exportOf(ctx, clusterID)
	if err != nil {
		return err
	}
	if c.GitRepoID != nil {
		return &ConflictError{Code: "already_linked", Msg: "dit cluster is al aan Git gekoppeld"}
	}
	data, _, err := s.fileFor(ctx, *repo, c.Slug)
	if err != nil {
		return err
	}
	_, export, err := s.Export(ctx, clusterID)
	if err != nil {
		return err
	}
	if !sameAsExport(data, export, cl, tpl, s.reg()) {
		d, _ := Diff(string(export), string(data), "export van ClusterForge", filePath(repo.Path, c.Slug)+" in Git")
		return &ConflictError{Code: "git_mismatch", Diff: d,
			Msg: "het bestand in Git is niet gelijk aan de export; commit de export ongewijzigd, koppel, en wijzig daarna in Git"}
	}
	path := filePath(repo.Path, c.Slug)
	url := FileURL(repo.ApiUrl, repo.Owner, repo.Name, repo.Branch, path)
	head := HeadCommit(*repo)
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.LockCluster(ctx, clusterID)
		if err != nil {
			return err
		}
		if cur.GitRepoID != nil {
			return &ConflictError{Code: "already_linked", Msg: "dit cluster is al aan Git gekoppeld"}
		}
		if cur.SpecRevision != c.SpecRevision {
			return &ConflictError{Code: "changed", Msg: "het cluster veranderde intussen; exporteer opnieuw"}
		}
		if err := q.SetClusterGit(ctx, store.SetClusterGitParams{ID: clusterID, GitRepoID: &repo.ID, GitRepoUrl: url}); err != nil {
			return err
		}
		payload := map[string]any{"name": c.Name, "path": path, "repo": repo.Owner + "/" + repo.Name, "url": url}
		if head != nil {
			payload["commit"] = head.SHA
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID,
			Action: "cluster.git_linked", Payload: payload,
		})
	})
	if err != nil {
		return err
	}
	s.Kick(false)
	return nil
}

// sameAsExport vergelijkt het bestand met de export op inhoud: dezelfde
// velden en waarden, ongeacht volgorde, aanhalingstekens of commentaar.
func sameAsExport(data, export []byte, c *Cluster, tpl *templates.Template, reg *templates.Registry) bool {
	got, errs := Parse(data)
	if len(errs) > 0 {
		return false
	}
	want, errs := Parse(export)
	if len(errs) > 0 {
		return false
	}
	unlinked := *c
	unlinked.Linked = false
	a, errs, _ := Check(got, c.Slug, &unlinked, reg)
	if len(errs) > 0 {
		return false
	}
	b, errs, _ := Check(want, c.Slug, &unlinked, reg)
	if len(errs) > 0 || a.Template.Version != tpl.Version {
		return false
	}
	if a.Metadata.Name != b.Metadata.Name || a.Metadata.Description != b.Metadata.Description ||
		a.Metadata.Environment != b.Metadata.Environment || !slices.Equal(a.Metadata.Tags, b.Metadata.Tags) ||
		a.Template.Version != b.Template.Version || !sameParams(a.Params, b.Params) {
		return false
	}
	// Elke parameter moet er staan, ook een lege.
	for _, p := range tpl.Params {
		if p.Type != "secret" && got.HasParam(p.Name) != want.HasParam(p.Name) {
			return false
		}
	}
	return sameTarget(got.Target, want.Target)
}

func sameTarget(a, b *FileTarget) bool {
	if a == nil || b == nil {
		return a == b
	}
	vlan := func(v *int) int {
		if v == nil {
			return 0
		}
		return *v
	}
	return a.Proxmox == b.Proxmox && a.ImageVMID == b.ImageVMID && a.Storage == b.Storage && or(a.Bridge, "vmbr0") == or(b.Bridge, "vmbr0") &&
		vlan(a.VLAN) == vlan(b.VLAN) && a.FirstIP == b.FirstIP && a.Gateway == b.Gateway &&
		slices.Equal(a.DNS, b.DNS) && slices.Equal(a.SSHKeys, b.SSHKeys)
}

// Unlink maakt het cluster weer los van Git. ClusterForge negeert het
// bestand daarna, en een wachtende wijziging vervalt.
func (s *Service) Unlink(ctx context.Context, actor events.Actor, clusterID uuid.UUID) error {
	repo, err := s.current(ctx)
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		c, err := q.LockCluster(ctx, clusterID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if c.GitRepoID == nil {
			return &ConflictError{Code: "not_linked", Msg: "dit cluster is niet aan Git gekoppeld"}
		}
		if err := q.SetClusterGit(ctx, store.SetClusterGitParams{ID: clusterID, GitRepoID: nil, GitRepoUrl: c.GitRepoUrl}); err != nil {
			return err
		}
		if p, err := q.GetPendingGitChange(ctx, c.Slug); err == nil {
			old, err := q.SupersedeGitChange(ctx, store.SupersedeGitChangeParams{ID: p.ID, Reason: "het cluster is ontkoppeld"})
			if err != nil {
				return err
			}
			err = s.ev.Write(ctx, q, events.Event{
				Actor: actor, SubjectType: "git_change", SubjectID: old.ID.String(), ClusterID: old.ClusterID, Action: "gitops.change_superseded",
				Payload: map[string]any{"slug": old.Slug, "path": old.Path, "commit": old.CommitSha, "author": old.CommitAuthor, "reason": "het cluster is ontkoppeld"},
			})
			if err != nil {
				return err
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		path := c.Slug
		if repo != nil {
			path = filePath(repo.Path, c.Slug)
		}
		return s.ev.Write(ctx, q, events.Event{
			Actor: actor, SubjectType: "cluster", SubjectID: clusterID.String(), ClusterID: &clusterID,
			Action: "cluster.git_unlinked", Payload: map[string]any{"name": c.Name, "path": path},
		})
	})
	if err != nil {
		return err
	}
	s.Kick(false)
	return nil
}

// Local zegt per node welke stappen van het plan op de node al afwijken
// van de spec, uit de laatste driftcontrole. Toepassen overschrijft ze.
func (s *Service) Local(ctx context.Context, ch store.GitChange, p Plan) ([]string, error) {
	out := []string{}
	if ch.ClusterID == nil || ch.Status != "pending" {
		return out, nil
	}
	checks, err := s.q.ListClusterDriftChecks(ctx, ch.ClusterID)
	if err != nil {
		return nil, err
	}
	byNode := map[uuid.UUID]store.DriftCheck{}
	for _, c := range checks {
		byNode[c.NodeID] = c
	}
	for _, n := range p.Nodes {
		if n.New {
			continue
		}
		c, ok := byNode[n.NodeID]
		if !ok || c.Status == "none" {
			out = append(out, n.Hostname+" is nog niet op drift gecontroleerd; wat daar al afwijkt, staat hier niet.")
			continue
		}
		if int(c.SpecRevision) != p.BaseRevision {
			out = append(out, fmt.Sprintf("De laatste driftcontrole van %s hoort bij revisie %d, niet bij %d; controleer opnieuw voor een volledig beeld.",
				n.Hostname, c.SpecRevision, p.BaseRevision))
		}
		var findings []drift.Finding
		_ = json.Unmarshal(c.Findings, &findings)
		for _, st := range n.Steps {
			if st.Change == "removed" {
				continue
			}
			ignored, drifted := false, false
			for _, f := range findings {
				if slices.Contains(st.IDs, f.Step) {
					drifted = true
					ignored = ignored || f.Ignored
				}
			}
			switch {
			case drifted && ignored:
				out = append(out, fmt.Sprintf("%s: %s wijkt af en die afwijking wordt genegeerd, maar het toepassen overschrijft haar toch.", n.Hostname, lower(st.Title)))
			case drifted:
				out = append(out, fmt.Sprintf("%s: %s wijkt op de node al af van de spec en wordt overschreven.", n.Hostname, lower(st.Title)))
			}
		}
	}
	return out, nil
}

func lower(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
