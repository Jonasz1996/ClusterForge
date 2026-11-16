package pvehost

// Fase 26 M3: een terugzettest. ClusterForge zet een back-up terug als een
// nieuwe gast met een vrij ID, koppelt het netwerk en doorgegeven hardware
// los (zodat hij niet botst met het origineel), start hem, wacht tot hij
// draait (bij een VM met guest agent tot die antwoordt) en ruimt hem daarna
// altijd weer op. Zo weet Jonas dat een back-up echt terug te zetten is.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// RestoreTest is een gevraagde terugzettest.
type RestoreTest struct {
	Datacenter string `json:"datacenter"`
	// Node is de Proxmox-host waarop de test draait.
	Node string `json:"node"`
	// Archive is het volume van de back-up, zoals pbs:backup/vm/100/2026-11-16T01:00:00Z.
	Archive string `json:"archive"`
	// Type is qemu of lxc.
	Type string `json:"type"`
	// Storage krijgt de schijven van de test.
	Storage string `json:"storage"`
}

// restoreMarker staat in de beschrijving van een testgast; alleen een gast
// met die markering ruimt ClusterForge weer op.
const restoreMarker = "clusterforge-terugzettest"

var (
	archiveRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]*:[^\s,;]{1,400}$`)
	storageRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)
	nodeRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,62}$`)
)

// Validate controleert de vorm.
func (t RestoreTest) Validate() error {
	switch {
	case t.Type != "qemu" && t.Type != "lxc":
		return ValidationError{"type is qemu of lxc"}
	case !archiveRe.MatchString(t.Archive) || strings.Contains(t.Archive, ".."):
		return ValidationError{"ongeldige back-up"}
	case !storageRe.MatchString(t.Storage):
		return ValidationError{"ongeldige storage"}
	case !nodeRe.MatchString(t.Node):
		return ValidationError{"ongeldige host"}
	}
	return nil
}

// StartRestoreTest zet een terugzettest in de wachtrij.
func (s *Service) StartRestoreTest(ctx context.Context, actor events.Actor, t RestoreTest) (store.Job, error) {
	if err := t.Validate(); err != nil {
		return store.Job{}, err
	}
	h, err := s.route(ctx, t.Datacenter, "/nodes/"+t.Node+"/"+t.Type)
	if err != nil {
		return store.Job{}, err
	}
	nodeID := h.NodeID
	title := fmt.Sprintf("Terugzettest van %s op %s", archiveName(t.Archive), t.Node)
	spec := jobs.Spec{
		Kind: Kind, Title: title, Actor: actor, NodeID: &nodeID, ProxmoxID: h.ConnectionID,
		Params: params{NodeID: nodeID, PVENode: h.PVENode, ConnID: h.ConnectionID, RestoreTest: &t},
	}
	j, err := s.jobs.Enqueue(ctx, spec)
	if err != nil {
		return store.Job{}, err
	}
	s.audit(ctx, actor, h, "pvehost.restore_test_requested", map[string]any{
		"datacenter": t.Datacenter, "node": t.Node, "archive": t.Archive, "storage": t.Storage, "job_id": j.ID.String(),
	})
	s.jobs.Kick()
	return j, nil
}

// archiveName maakt een back-upvolume korter voor een titel.
func archiveName(v string) string {
	_, rest, _ := strings.Cut(v, ":")
	rest = strings.TrimPrefix(rest, "backup/")
	if len(rest) > 60 {
		rest = "…" + rest[len(rest)-59:]
	}
	return rest
}

// nodeGet leest een pad rechtstreeks bij één agent, zonder cache.
func (s *Service) nodeGet(ctx context.Context, nodeID uuid.UUID, path string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, protocol.PVERequestTimeout)
	defer cancel()
	res, err := s.bus.PVE(ctx, nodeID, protocol.PVERequest{Op: protocol.PVEOpGet, Path: path})
	if err != nil {
		return err
	}
	if res.Error != "" {
		return errors.New(res.Error)
	}
	return json.Unmarshal(res.Data, v)
}

// apiStep voert één Proxmox-aanroep uit als stap.
func (s *Service) apiStep(ctx context.Context, st *jobs.Step, nodeID uuid.UUID, method, path string, p map[string]string, title string) error {
	op := protocol.PVERun{Action: protocol.PVEAPI, API: &protocol.PVEAPICall{Method: method, Path: path, Params: p}}
	res, err := s.Do(ctx, st, st, nodeID, op, title)
	if err != nil {
		return err
	}
	if res.State != protocol.RunOK {
		return errors.New(noderun.Describe(res))
	}
	return nil
}

type restoreState struct {
	VMID int `json:"vmid"`
}

// runRestoreTest doorloopt de stappen. Stappen worden altijd in dezelfde
// volgorde aangeroepen (zo hervat de runner ze); na een fout slaan de
// volgende stappen hun werk over, behalve het opruimen.
func (s *Service) runRestoreTest(ctx context.Context, j *jobs.Job, p params) error {
	t := *p.RestoreTest
	base := "/nodes/" + t.Node + "/" + t.Type
	var vmid int
	var failed error

	_ = j.Step(ctx, "Vrij ID kiezen", func(ctx context.Context, st *jobs.Step) error {
		var raw json.RawMessage
		if err := s.nodeGet(ctx, p.NodeID, "/cluster/nextid", &raw); err != nil {
			failed = err
			return err
		}
		id, err := strconv.Atoi(strings.Trim(string(raw), `"`))
		if err != nil || id < 100 {
			failed = fmt.Errorf("geen vrij ID: %s", raw)
			return failed
		}
		vmid = id
		st.Logf("De test krijgt ID %d.", id)
		return st.SetState(ctx, restoreState{VMID: id})
	})
	var rs restoreState
	if vmid == 0 && j.Done("Vrij ID kiezen", &rs) {
		vmid = rs.VMID
	}
	if vmid == 0 && failed == nil {
		failed = errors.New("geen ID voor de test")
	}
	path := fmt.Sprintf("%s/%d", base, vmid)
	started := time.Now()

	restored := false
	_ = j.Step(ctx, "Terugzetten", func(ctx context.Context, st *jobs.Step) error {
		if failed != nil {
			st.Logf("Overgeslagen: %v", failed)
			return nil
		}
		prm := map[string]string{"vmid": strconv.Itoa(vmid), "storage": t.Storage, "unique": "1"}
		if t.Type == "qemu" {
			prm["archive"] = t.Archive
		} else {
			prm["ostemplate"], prm["restore"] = t.Archive, "1"
		}
		if err := s.apiStep(ctx, st, p.NodeID, "create", base, prm, fmt.Sprintf("back-up terugzetten als %d", vmid)); err != nil {
			failed = err
			return err
		}
		restored = true
		st.Logf("Teruggezet in %s.", time.Since(started).Round(time.Second))
		return nil
	})
	if !restored && failed == nil {
		// Hervat na een herstart: bestaat de testgast al?
		var cfg map[string]any
		restored = s.nodeGet(ctx, p.NodeID, path+"/config", &cfg) == nil
	}

	_ = j.Step(ctx, "Netwerk en hardware loskoppelen", func(ctx context.Context, st *jobs.Step) error {
		if failed != nil {
			st.Logf("Overgeslagen.")
			return nil
		}
		var cfg map[string]any
		if err := s.nodeGet(ctx, p.NodeID, path+"/config", &cfg); err != nil {
			failed = err
			return err
		}
		prm, del := isolate(t.Type, cfg)
		prm["description"] = restoreMarker + " van " + t.Archive
		prm["onboot"] = "0"
		if t.Type == "qemu" {
			prm["name"] = fmt.Sprintf("terugzettest-%d", vmid)
		} else {
			prm["hostname"] = fmt.Sprintf("terugzettest-%d", vmid)
		}
		if len(del) > 0 {
			prm["delete"] = strings.Join(del, ",")
			st.Logf("Weg: %s", strings.Join(del, ", "))
		}
		if err := s.apiStep(ctx, st, p.NodeID, "set", path+"/config", prm, "testgast losmaken"); err != nil {
			failed = err
			return err
		}
		return nil
	})

	_ = j.Step(ctx, "Starten", func(ctx context.Context, st *jobs.Step) error {
		if failed != nil {
			st.Logf("Overgeslagen.")
			return nil
		}
		if err := s.apiStep(ctx, st, p.NodeID, "create", path+"/status/start", nil, "testgast starten"); err != nil {
			failed = err
			return err
		}
		return nil
	})

	_ = j.Step(ctx, "Controleren", func(ctx context.Context, st *jobs.Step) error {
		if failed != nil {
			st.Logf("Overgeslagen.")
			return nil
		}
		if err := s.checkRunning(ctx, st, p.NodeID, t.Type, path); err != nil {
			failed = err
			return err
		}
		return nil
	})

	// Opruimen gebeurt altijd als er iets teruggezet is.
	cleanCtx := context.WithoutCancel(ctx)
	_ = j.Step(cleanCtx, "Stoppen", func(ctx context.Context, st *jobs.Step) error {
		if !restored {
			st.Logf("Niets op te ruimen.")
			return nil
		}
		var cur struct {
			Status string `json:"status"`
		}
		if err := s.nodeGet(ctx, p.NodeID, path+"/status/current", &cur); err == nil && cur.Status != "running" {
			return nil
		}
		return s.apiStep(ctx, st, p.NodeID, "create", path+"/status/stop", nil, "testgast stoppen")
	})
	cleanErr := j.Step(cleanCtx, "Opruimen", func(ctx context.Context, st *jobs.Step) error {
		if !restored {
			st.Logf("Niets op te ruimen.")
			return nil
		}
		var cfg map[string]any
		if err := s.nodeGet(ctx, p.NodeID, path+"/config", &cfg); err != nil {
			return fmt.Errorf("testgast %d niet te lezen; ruim hem zelf op als hij bestaat: %w", vmid, err)
		}
		if d, _ := cfg["description"].(string); !strings.Contains(d, restoreMarker) {
			return fmt.Errorf("gast %d heeft de markering van de test niet; ClusterForge laat hem staan", vmid)
		}
		return s.apiStep(ctx, st, p.NodeID, "delete", path, map[string]string{"purge": "1", "destroy-unreferenced-disks": "1"}, "testgast verwijderen")
	})
	if failed != nil {
		return fmt.Errorf("terugzettest mislukt: %w", failed)
	}
	return cleanErr
}

// isolate zegt wat er aan een teruggezette gast verandert: netwerkkaarten
// los (VM: link_down, CT: weg) en doorgegeven hardware weg.
func isolate(typ string, cfg map[string]any) (map[string]string, []string) {
	prm := map[string]string{}
	var del []string
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := fmt.Sprint(cfg[k])
		switch {
		case typ == "qemu" && strings.HasPrefix(k, "net") && isDigits(k[3:]):
			if !strings.Contains(v, "link_down=1") {
				prm[k] = v + ",link_down=1"
			}
		case typ == "lxc" && strings.HasPrefix(k, "net") && isDigits(k[3:]):
			del = append(del, k)
		case typ == "qemu" && (strings.HasPrefix(k, "hostpci") && isDigits(k[7:]) || strings.HasPrefix(k, "usb") && isDigits(k[3:])):
			del = append(del, k)
		case typ == "lxc" && strings.HasPrefix(k, "dev") && isDigits(k[3:]):
			del = append(del, k)
		}
	}
	return prm, del
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// checkRunning wacht tot de testgast draait; bij een VM met guest agent
// ook tot die antwoordt (het besturingssysteem is dan echt opgestart).
func (s *Service) checkRunning(ctx context.Context, st *jobs.Step, nodeID uuid.UUID, typ, path string) error {
	deadline := time.Now().Add(3 * time.Minute)
	for {
		var cur struct {
			Status string `json:"status"`
			Agent  any    `json:"agent"`
		}
		if err := s.nodeGet(ctx, nodeID, path+"/status/current", &cur); err == nil && cur.Status == "running" {
			st.Logf("De testgast draait.")
			if typ != "qemu" || fmt.Sprint(cur.Agent) != "1" {
				return nil
			}
			break
		}
		if time.Now().After(deadline) {
			return errors.New("de testgast draaide na 3 minuten nog niet")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.pollEvery()):
		}
	}
	_ = st.Flush(ctx)
	deadline = time.Now().Add(4 * time.Minute)
	for {
		var info map[string]any
		if err := s.nodeGet(ctx, nodeID, path+"/agent/info", &info); err == nil {
			st.Logf("De guest agent antwoordt: het systeem is opgestart.")
			return nil
		}
		if time.Now().After(deadline) {
			st.Logf("De guest agent antwoordde niet binnen 4 minuten. De VM draait wel; controleer of hij opstart (console) of de agent mist.")
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.pollEvery()):
		}
	}
}

func (s *Service) pollEvery() time.Duration {
	if s.RestorePoll > 0 {
		return s.RestorePoll
	}
	return 5 * time.Second
}
