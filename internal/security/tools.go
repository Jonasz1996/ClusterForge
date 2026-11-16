package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Beveiligingstools op de nodes (M7): installeren, draaien en plannen via
// de agent; vondsten worden bevindingen van soort KindTools.

// KindTools is de soort bevinding van de tools.
const KindTools = "tools"

// ToolsEvery is hoe vaak de server de stand van de tools vraagt; loopt er
// iets, dan elke ronde.
const ToolsEvery = 15 * time.Minute

// ToolInfo beschrijft een tool voor de GUI.
type ToolInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Run: de tool heeft een run (auditd niet: die draait altijd).
	Run      bool `json:"run"`
	Baseline bool `json:"baseline"`
	// NoLXC: kan niet in een LXC-container; Docker: alleen nuttig met Docker.
	NoLXC  bool `json:"no_lxc"`
	Docker bool `json:"docker"`
}

// ToolInfos zijn de tools in de volgorde van protocol.SecurityTools.
var ToolInfos = []ToolInfo{
	{ID: protocol.ToolClamAV, Name: "ClamAV", Run: true,
		Description: "Virusscanner: zoekt bekende malware en webshells in alle bestanden. Een scan vraagt ongeveer 1,5 GB geheugen; plan hem 's nachts."},
	{ID: protocol.ToolAIDE, Name: "AIDE", Run: true, Baseline: true,
		Description: "Bestandsintegriteit: onthoudt hoe de systeembestanden eruitzien en meldt wat er sindsdien veranderde."},
	{ID: protocol.ToolRkhunter, Name: "rkhunter", Run: true, Baseline: true,
		Description: "Zoekt rootkits, verdachte bestanden en gewijzigde systeemprogramma's."},
	{ID: protocol.ToolChkrootkit, Name: "chkrootkit", Run: true,
		Description: "Tweede rootkitzoeker, met andere controles dan rkhunter."},
	{ID: protocol.ToolLynis, Name: "Lynis", Run: true,
		Description: "Volledige verhardingsaudit; het rapport verschijnt onder Verharding bij de node."},
	{ID: protocol.ToolAuditd, Name: "auditd", NoLXC: true,
		Description: "Kernel-audit: logt wijzigingen aan accounts, sudo, SSH, cron en systemd en commando's als root. Te lezen onder Logs (/var/log/audit/audit.log)."},
	{ID: protocol.ToolTrivy, Name: "Trivy", Run: true, Docker: true,
		Description: "Scant de images van de draaiende containers op hoge en kritieke kwetsbaarheden (apt-bron van Aqua Security)."},
}

// ToolRow is één tool op één node.
type ToolRow struct {
	Tool      string             `json:"tool"`
	Installed bool               `json:"installed"`
	Version   string             `json:"version"`
	Running   string             `json:"running"`
	State     protocol.ToolState `json:"state"`
	Schedule  string             `json:"schedule"`
	Weekday   int                `json:"weekday"`
	Hour      int                `json:"hour"`
	NextRunAt *time.Time         `json:"next_run_at"`
	UpdatedAt time.Time          `json:"updated_at"`
}

// ToolsNode is een node op de pagina Tools.
type ToolsNode struct {
	NodeID         uuid.UUID  `json:"node_id"`
	Hostname       string     `json:"hostname"`
	ClusterName    string     `json:"cluster_name"`
	Online         bool       `json:"online"`
	Protocol       int        `json:"protocol"`
	Virtualization string     `json:"virtualization"`
	Docker         bool       `json:"docker"`
	FetchedAt      *time.Time `json:"fetched_at"`
	Error          string     `json:"error"`
	Tools          []ToolRow  `json:"tools"`
}

// ToolsOverview is de pagina Tools.
type ToolsOverview struct {
	Protocol int         `json:"protocol"`
	Tools    []ToolInfo  `json:"tools"`
	Nodes    []ToolsNode `json:"nodes"`
}

// ErrToolBusy: de tool is al bezig op die node.
var ErrToolBusy = errors.New("deze tool is al bezig op die node")

func toolInfo(id string) (ToolInfo, bool) {
	for _, t := range ToolInfos {
		if t.ID == id {
			return t, true
		}
	}
	return ToolInfo{}, false
}

// Tools geeft per node de stand van de tools.
func (s *Service) Tools(ctx context.Context) (ToolsOverview, error) {
	o := ToolsOverview{Protocol: protocol.SecurityToolsSince, Tools: ToolInfos, Nodes: []ToolsNode{}}
	nodes, err := s.nodes(ctx, "")
	if err != nil {
		return o, err
	}
	rows, err := s.toolRows(ctx, nil)
	if err != nil {
		return o, err
	}
	meta := map[uuid.UUID]struct {
		at  *time.Time
		err string
	}{}
	r, err := s.pool.Query(ctx, `SELECT node_id, fetched_at, error FROM security_tool_nodes`)
	if err != nil {
		return o, err
	}
	for r.Next() {
		var id uuid.UUID
		var m struct {
			at  *time.Time
			err string
		}
		if err := r.Scan(&id, &m.at, &m.err); err != nil {
			r.Close()
			return o, err
		}
		meta[id] = m
	}
	r.Close()
	for _, n := range nodes {
		tn := ToolsNode{NodeID: n.NodeID, Hostname: n.Hostname, ClusterName: n.ClusterName, Online: n.Online, Protocol: n.Protocol,
			Virtualization: n.Virtualization, Docker: n.docker, FetchedAt: meta[n.NodeID].at, Error: meta[n.NodeID].err, Tools: []ToolRow{}}
		for _, t := range protocol.SecurityTools {
			if row, ok := rows[n.NodeID][t]; ok {
				tn.Tools = append(tn.Tools, row)
			} else {
				tn.Tools = append(tn.Tools, ToolRow{Tool: t, Schedule: ScheduleOff, Hour: 3})
			}
		}
		o.Nodes = append(o.Nodes, tn)
	}
	return o, nil
}

func (s *Service) toolRows(ctx context.Context, nodeID *uuid.UUID) (map[uuid.UUID]map[string]ToolRow, error) {
	q := `SELECT node_id, tool, installed, version, running, state, schedule, weekday, hour, next_run_at, updated_at FROM security_tools`
	var args []any
	if nodeID != nil {
		q += ` WHERE node_id = $1`
		args = append(args, *nodeID)
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]map[string]ToolRow{}
	for rows.Next() {
		var id uuid.UUID
		var r ToolRow
		var state []byte
		if err := rows.Scan(&id, &r.Tool, &r.Installed, &r.Version, &r.Running, &state, &r.Schedule, &r.Weekday, &r.Hour, &r.NextRunAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(state, &r.State)
		if out[id] == nil {
			out[id] = map[string]ToolRow{}
		}
		out[id][r.Tool] = r
	}
	return out, rows.Err()
}

func (s *Service) toolNode(ctx context.Context, nodeID uuid.UUID) (Node, error) {
	list, err := s.nodes(ctx, "WHERE n.id = $1", nodeID)
	if err != nil {
		return Node{}, err
	}
	if len(list) == 0 {
		return Node{}, ErrNotFound
	}
	n := list[0]
	if !n.Online {
		return n, &FieldError{n.Hostname + " is offline."}
	}
	if n.Protocol < protocol.SecurityToolsSince {
		return n, &FieldError{fmt.Sprintf("De agent van %s is ouder dan protocol %d; werk hem eerst bij.", n.Hostname, protocol.SecurityToolsSince)}
	}
	return n, nil
}

// ToolAction installeert, draait, verwijdert een tool of maakt de
// basislijn opnieuw; het werk loopt op de node op de achtergrond.
func (s *Service) ToolAction(ctx context.Context, actor events.Actor, nodeID uuid.UUID, tool, action string) error {
	info, ok := toolInfo(tool)
	if !ok {
		return &FieldError{"Onbekende tool."}
	}
	switch action {
	case protocol.ToolInstall, protocol.ToolRemove:
	case protocol.ToolRun:
		if !info.Run {
			return &FieldError{info.Name + " heeft geen run; hij draait altijd."}
		}
	case protocol.ToolBaseline:
		if !info.Baseline {
			return &FieldError{info.Name + " heeft geen basislijn."}
		}
	default:
		return &FieldError{"Onbekende actie."}
	}
	n, err := s.toolNode(ctx, nodeID)
	if err != nil {
		return err
	}
	if info.NoLXC && n.Virtualization == "lxc" && action == protocol.ToolInstall {
		return &FieldError{info.Name + " kan niet in een LXC-container draaien; zet het op de Proxmox-host."}
	}
	if info.Docker && !n.docker && action != protocol.ToolRemove {
		return &FieldError{info.Name + " heeft alleen zin op een node met Docker."}
	}
	if err := s.toolRequest(ctx, n, &protocol.ToolsSpec{Action: action, Tool: tool}); err != nil {
		if strings.Contains(err.Error(), "al bezig") {
			return ErrToolBusy
		}
		return &FieldError{err.Error()}
	}
	s.event(ctx, actor, "node", n.NodeID.String(), n.ClusterID, "security.tool_"+action, map[string]any{"hostname": n.Hostname, "tool": tool})
	return nil
}

// SetToolSchedule plant de run van een tool op een node.
func (s *Service) SetToolSchedule(ctx context.Context, actor events.Actor, nodeID uuid.UUID, tool, schedule string, weekday, hour int) error {
	info, ok := toolInfo(tool)
	if !ok || !info.Run {
		return &FieldError{"Deze tool kan je niet plannen."}
	}
	if !slices.Contains([]string{ScheduleOff, ScheduleDaily, ScheduleWeekly}, schedule) {
		return &FieldError{"Kies uit, dagelijks of wekelijks."}
	}
	if weekday < 0 || weekday > 6 || hour < 0 || hour > 23 {
		return &FieldError{"Ongeldige dag of uur."}
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM nodes WHERE id = $1)`, nodeID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	next := nextRun(schedule, weekday, hour, s.Now())
	if _, err := s.pool.Exec(ctx, `INSERT INTO security_tools (node_id, tool, schedule, weekday, hour, next_run_at) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (node_id, tool) DO UPDATE SET schedule = $3, weekday = $4, hour = $5, next_run_at = $6`, nodeID, tool, schedule, weekday, hour, next); err != nil {
		return err
	}
	s.event(ctx, actor, "node", nodeID.String(), nil, "security.tool_scheduled", map[string]any{"tool": tool, "schedule": schedule, "hour": hour})
	return nil
}

// toolRequest stuurt een toolverzoek en bewaart de stand die terugkomt.
func (s *Service) toolRequest(ctx context.Context, n Node, spec *protocol.ToolsSpec) error {
	rctx, cancel := context.WithTimeout(ctx, protocol.SecurityRequestTimeout)
	defer cancel()
	res, err := s.bus.Security(rctx, n.NodeID, protocol.SecurityRequest{Op: protocol.SecurityOpTools, Tools: spec})
	if err == nil && res.Error != "" {
		err = errors.New(res.Error)
	}
	if err == nil && res.Tools == nil {
		err = errors.New("de agent stuurde geen stand van de tools")
	}
	if err != nil {
		if spec.Action == protocol.ToolStatus {
			_, _ = s.pool.Exec(ctx, `INSERT INTO security_tool_nodes (node_id, fetched_at, error) VALUES ($1, now(), $2)
ON CONFLICT (node_id) DO UPDATE SET fetched_at = now(), error = $2`, n.NodeID, err.Error())
		}
		return err
	}
	return s.saveTools(ctx, n, res.Tools)
}

func (s *Service) saveTools(ctx context.Context, n Node, res *protocol.ToolsResult) error {
	before, err := s.toolRows(ctx, &n.NodeID)
	if err != nil {
		return err
	}
	for _, st := range res.Tools {
		if _, ok := toolInfo(st.Tool); !ok {
			continue
		}
		b, _ := json.Marshal(st)
		if _, err := s.pool.Exec(ctx, `INSERT INTO security_tools (node_id, tool, installed, version, running, state, updated_at) VALUES ($1, $2, $3, $4, $5, $6, now())
ON CONFLICT (node_id, tool) DO UPDATE SET installed = $3, version = $4, running = $5, state = $6, updated_at = now()`,
			n.NodeID, st.Tool, st.Installed, st.Version, st.Running, b); err != nil {
			return err
		}
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO security_tool_nodes (node_id, fetched_at, error) VALUES ($1, now(), '')
ON CONFLICT (node_id) DO UPDATE SET fetched_at = now(), error = ''`, n.NodeID); err != nil {
		return err
	}
	// Een nieuwe Lynis-run: de verharding opnieuw uitlezen (het rapport).
	for _, st := range res.Tools {
		if st.Tool != protocol.ToolLynis {
			continue
		}
		now, old := st.Last[protocol.ToolRun].Ended, before[n.NodeID][protocol.ToolLynis].State.Last[protocol.ToolRun].Ended
		if now != nil && (old == nil || now.After(*old)) {
			s.Refresh(false, true, &n.NodeID)
		}
	}
	return s.sync(ctx, n, KindTools, ToolDrafts(res.Tools))
}

// ToolDrafts maakt bevindingen van de laatste runs.
func ToolDrafts(states []protocol.ToolState) []Draft {
	var out []Draft
	for _, st := range states {
		run, ok := st.Last[protocol.ToolRun]
		if !ok || run.Ended == nil || !st.Installed {
			continue
		}
		detail := map[string]any{"tool": st.Tool, "items": run.Items, "ended": run.Ended}
		n := len(run.Items)
		switch st.Tool {
		case protocol.ToolClamAV:
			if n > 0 {
				out = append(out, Draft{Kind: KindTools, Key: "clamav", Severity: SevCritical, Detail: detail,
					Title:   "ClamAV vond malware in " + plural(n, "bestand", "bestanden"),
					Summary: strings.Join(firstN(run.Items, 3), " · "),
					Fix:     "Haal het bestand weg of zet het in quarantaine, zoek uit hoe het er kwam (logs, webapp, upload) en scan opnieuw. Op een webserver: kijk ook naar de andere sites."})
			}
		case protocol.ToolAIDE:
			if n > 0 {
				out = append(out, Draft{Kind: KindTools, Key: "aide", Severity: SevMedium, Detail: detail,
					Title:   "AIDE: " + plural(n, "bestand", "bestanden") + " veranderd sinds de basislijn",
					Summary: strings.Join(firstN(run.Items, 3), " · "),
					Fix:     "Kwam dit door een update of een wijziging die je zelf deed? Klik dan bij AIDE op Basislijn bijwerken. Zo niet: onderzoek de node."})
			}
		case protocol.ToolRkhunter:
			if n > 0 {
				out = append(out, Draft{Kind: KindTools, Key: "rkhunter", Severity: SevHigh, Detail: detail,
					Title:   "rkhunter geeft " + plural(n, "waarschuwing", "waarschuwingen"),
					Summary: strings.Join(firstN(run.Items, 3), " · "),
					Fix:     "Lees de waarschuwingen na. Na een pakketupdate zijn ze vaak vals alarm: klik dan bij rkhunter op Basislijn bijwerken."})
			}
		case protocol.ToolChkrootkit:
			var infected []string
			for _, it := range run.Items {
				if strings.Contains(it, "INFECTED") {
					infected = append(infected, it)
				}
			}
			if len(infected) > 0 {
				out = append(out, Draft{Kind: KindTools, Key: "chkrootkit", Severity: SevCritical, Detail: detail,
					Title:   "chkrootkit meldt een besmetting",
					Summary: strings.Join(firstN(infected, 3), " · "),
					Fix:     "Neem de node van het netwerk, controleer met rkhunter en ClamAV en herinstalleer bij twijfel uit een schone back-up."})
			} else if n > 0 {
				out = append(out, Draft{Kind: KindTools, Key: "chkrootkit", Severity: SevLow, Detail: detail,
					Title:   "chkrootkit maakt " + plural(n, "opmerking", "opmerkingen"),
					Summary: strings.Join(firstN(run.Items, 3), " · "),
					Fix:     "Meestal verborgen bestanden van gewone programma's; kijk ze één keer na."})
			}
		case protocol.ToolTrivy:
			for _, img := range run.Images {
				crit, high := img.Counts["critical"], img.Counts["high"]
				if crit+high == 0 {
					continue
				}
				sev := SevHigh
				if crit > 0 {
					sev = SevCritical
				}
				ids := make([]string, 0, 3)
				for _, v := range firstNVulns(img.Vulns, 3) {
					ids = append(ids, v.ID+" ("+v.Pkg+")")
				}
				out = append(out, Draft{Kind: KindTools, Key: "image:" + img.Image, Severity: sev,
					Detail:  map[string]any{"tool": st.Tool, "image": img.Image, "counts": img.Counts, "vulns": img.Vulns, "ended": run.Ended},
					Title:   fmt.Sprintf("Image %s: %d kritieke en %d hoge kwetsbaarheden", img.Image, crit, high),
					Summary: strings.Join(ids, " · "),
					Fix:     "Werk de image bij (nieuwere tag, `docker compose pull` en opnieuw starten). Zit de fout in een basisimage dat niet meer bijgewerkt wordt, kies een ander."})
			}
		}
	}
	return out
}

func firstN(list []string, n int) []string {
	if len(list) > n {
		return list[:n]
	}
	return list
}

func firstNVulns(list []protocol.TrivyVuln, n int) []protocol.TrivyVuln {
	if len(list) > n {
		return list[:n]
	}
	return list
}

// tickTools vraagt de stand van de tools en start geplande runs.
func (s *Service) tickTools(ctx context.Context, nodes []Node) error {
	now := s.Now()
	type meta struct {
		at      *time.Time
		running bool
	}
	m := map[uuid.UUID]meta{}
	rows, err := s.pool.Query(ctx, `SELECT t.node_id, t.fetched_at, EXISTS (SELECT 1 FROM security_tools x WHERE x.node_id = t.node_id AND x.running <> '')
FROM security_tool_nodes t`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id uuid.UUID
		var x meta
		if err := rows.Scan(&id, &x.at, &x.running); err != nil {
			rows.Close()
			return err
		}
		m[id] = x
	}
	rows.Close()
	for _, n := range nodes {
		if !n.Online || n.Protocol < protocol.SecurityToolsSince {
			continue
		}
		x := m[n.NodeID]
		if x.at != nil && now.Sub(*x.at) < ToolsEvery && !x.running {
			continue
		}
		if err := s.toolRequest(ctx, n, &protocol.ToolsSpec{Action: protocol.ToolStatus}); err != nil {
			s.log.Debug("stand van de tools ophalen mislukt", "node", n.Hostname, "err", err)
		}
	}
	// Geplande runs.
	type due struct {
		node              uuid.UUID
		tool, schedule    string
		weekday, hour     int
		installed, active bool
	}
	var list []due
	rows, err = s.pool.Query(ctx, `SELECT node_id, tool, schedule, weekday, hour, installed, running <> '' FROM security_tools
WHERE schedule <> 'uit' AND next_run_at IS NOT NULL AND next_run_at <= $1`, now)
	if err != nil {
		return err
	}
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.node, &d.tool, &d.schedule, &d.weekday, &d.hour, &d.installed, &d.active); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	for _, d := range list {
		if _, err := s.pool.Exec(ctx, `UPDATE security_tools SET next_run_at = $3 WHERE node_id = $1 AND tool = $2`, d.node, d.tool,
			nextRun(d.schedule, d.weekday, d.hour, now)); err != nil {
			return err
		}
		if !d.installed || d.active {
			continue
		}
		if err := s.ToolAction(ctx, events.System(), d.node, d.tool, protocol.ToolRun); err != nil {
			s.log.Info("geplande run van een tool niet gestart", "tool", d.tool, "node", d.node, "err", err)
		}
	}
	return nil
}
