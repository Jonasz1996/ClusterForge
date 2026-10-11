package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/assistant"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
)

// De tools van de assistent lezen via de GET-handlers van de API, in het
// proces zelf en met de rechten van wie de vraag stelt. Ze zien dus precies
// wat die gebruiker in de webinterface ziet, met dezelfde weglatingen per
// rol. Geen tool roept een handler aan die iets wijzigt; TestAssistantToolsReadOnly
// controleert dat.

// viewRecorder vangt het antwoord van een handler op.
type viewRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *viewRecorder) Header() http.Header { return r.header }

func (r *viewRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
}

func (r *viewRecorder) Write(b []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	return r.body.Write(b)
}

// view roept een GET-handler aan als c en leest het antwoord in out. Een
// fout die de gebruiker ook zou zien, zoals 'niet gevonden', krijgt het
// model als ToolError.
func (s *Server) view(ctx context.Context, c assistant.Caller, h http.HandlerFunc, out any) error {
	req, err := http.NewRequestWithContext(context.WithValue(ctx, principalKey, c.Principal), http.MethodGet, "/api/v1/assistent/tool", nil)
	if err != nil {
		return err
	}
	rec := &viewRecorder{header: http.Header{}}
	h(rec, req)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	if rec.status >= 300 {
		if rec.status == http.StatusInternalServerError {
			return fmt.Errorf("weergave voor de assistent gaf %d", rec.status)
		}
		var e gen.Error
		_ = json.Unmarshal(rec.body.Bytes(), &e)
		return assistant.ToolError{Msg: cmpOr(e.Message, http.StatusText(rec.status))}
	}
	return json.Unmarshal(rec.body.Bytes(), out)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// hidden zijn velden die nooit naar de Anthropic API gaan, ook niet als een
// weergave ze heeft: vingerafdrukken, sleutels, tokens, de herkomst van een
// event en ruwe payloads (de zin uit het logboek staat ernaast).
var hidden = map[string]bool{
	"fingerprint": true, "fingerprints": true, "tls_fingerprint": true, "nats_cert_sha256": true, "nkey_public": true,
	"token": true, "token_id": true, "token_secret": true, "secret": true, "password": true, "api_key": true,
	"csrf_token": true, "ssh_keys": true, "machine_id": true, "mac": true, "ip": true, "session": true, "payload": true,
}

func hiddenKey(k string) bool {
	return hidden[k] || strings.HasSuffix(k, "_enc") || strings.HasSuffix(k, "_secret") || strings.HasSuffix(k, "_password")
}

// compact maakt van een weergave compacte JSON voor het model: zonder lege
// velden, zonder hidden en drop, en met tijden in UTC op de seconde.
func compact(v any, drop ...string) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var x any
	if err := d.Decode(&x); err != nil {
		return nil
	}
	skip := map[string]bool{}
	for _, k := range drop {
		skip[k] = true
	}
	return clean(x, skip)
}

func clean(x any, skip map[string]bool) any {
	switch v := x.(type) {
	case map[string]any:
		for k, e := range v {
			if hiddenKey(k) || skip[k] {
				delete(v, k)
				continue
			}
			if e = clean(e, skip); empty(e) {
				delete(v, k)
				continue
			}
			v[k] = e
		}
		return v
	case []any:
		out := v[:0]
		for _, e := range v {
			if e = clean(e, skip); !empty(e) {
				out = append(out, e)
			}
		}
		return out
	case string:
		return shortTime(v)
	}
	return x
}

func empty(x any) bool {
	switch v := x.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	return false
}

// shortTime schrijft een tijdstip als 2026-10-11T01:05:00Z.
func shortTime(s string) string {
	if len(s) < 20 || len(s) > 40 || s[4] != '-' || s[10] != 'T' {
		return s
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// orNote geeft een zin in plaats van een leeg resultaat, zodat het model
// weet dat er niets is en niet denkt dat de tool faalde.
func orNote(v any, note string) any {
	if empty(v) {
		return map[string]any{"opmerking": note}
	}
	return v
}

// compactAll maakt elk item compact.
func compactAll[T any](items []T, drop ...string) []any {
	out := make([]any, 0, len(items))
	for _, it := range items {
		if c := compact(it, drop...); !empty(c) {
			out = append(out, c)
		}
	}
	return out
}

// fit houdt zoveel items als er in budget bytes passen, vooraan of
// achteraan, en zegt hoeveel er wegvielen.
func fit(items []any, budget int, tail bool) ([]any, int) {
	size, n := 0, 0
	for i := range items {
		idx := i
		if tail {
			idx = len(items) - 1 - i
		}
		b, _ := json.Marshal(items[idx])
		if size += len(b) + 1; size > budget {
			break
		}
		n++
	}
	if tail {
		return items[len(items)-n:], len(items) - n
	}
	return items[:n], len(items) - n
}

// listBudget is wat een lijst van een tool mag innemen; de rest van de 20
// KB is voor de velden eromheen.
const listBudget = 16 * 1024

func withList(out map[string]any, key string, items []any, tail, more bool) map[string]any {
	kept, dropped := fit(items, listBudget, tail)
	out[key] = kept
	if dropped > 0 || more {
		which := "oudste"
		if !tail {
			which = "laatste"
		}
		out["let_op"] = fmt.Sprintf("er zijn meer regels; de %s ontbreken. Vraag een kortere periode of gebruik een filter.", which)
	}
	return out
}

// parseWhen leest een tijdstip van het model; leeg is de standaard.
func parseWhen(field, s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return &t, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return &t, nil
		}
	}
	return nil, assistant.ToolError{Msg: fmt.Sprintf("%s %q is geen tijdstip; gebruik ISO 8601 met tijdzone, zoals 2026-10-11T03:00:00+02:00", field, s)}
}

func (s *Server) findCluster(ctx context.Context, c assistant.Caller, name string) (uuid.UUID, error) {
	name = strings.TrimSpace(name)
	var l list[gen.ClusterListItem]
	if err := s.view(ctx, c, s.ListClusters, &l); err != nil {
		return uuid.Nil, err
	}
	id, idErr := uuid.Parse(name)
	names := make([]string, 0, len(l.Items))
	for _, cl := range l.Items {
		if (idErr == nil && cl.Id == id) || strings.EqualFold(cl.Name, name) || strings.EqualFold(cl.Slug, name) {
			return cl.Id, nil
		}
		names = append(names, cl.Name)
	}
	return uuid.Nil, assistant.ToolError{Msg: fmt.Sprintf("geen cluster %q; de clusters zijn: %s", name, known(names))}
}

func (s *Server) findNode(ctx context.Context, c assistant.Caller, name string) (uuid.UUID, error) {
	name = strings.TrimSpace(name)
	var l list[gen.Node]
	if err := s.view(ctx, c, s.ListNodes, &l); err != nil {
		return uuid.Nil, err
	}
	id, idErr := uuid.Parse(name)
	names := make([]string, 0, len(l.Items))
	var short []uuid.UUID
	for _, n := range l.Items {
		if (idErr == nil && n.Id == id) || strings.EqualFold(n.Hostname, name) {
			return n.Id, nil
		}
		if h, _, _ := strings.Cut(n.Hostname, "."); strings.EqualFold(h, name) {
			short = append(short, n.Id)
		}
		names = append(names, n.Hostname)
	}
	if len(short) == 1 {
		return short[0], nil
	}
	return uuid.Nil, assistant.ToolError{Msg: fmt.Sprintf("geen node %q; de nodes zijn: %s", name, known(names))}
}

func (s *Server) findService(ctx context.Context, name string) (uuid.UUID, error) {
	name = strings.TrimSpace(name)
	rows, err := s.q.ListServices(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	id, idErr := uuid.Parse(name)
	var hits []string
	var hit uuid.UUID
	for _, r := range rows {
		if idErr == nil && r.ID == id {
			return r.ID, nil
		}
		if strings.EqualFold(r.Name, name) {
			hit = r.ID
			hits = append(hits, r.ID.String())
		}
	}
	switch len(hits) {
	case 0:
		return uuid.Nil, assistant.ToolError{Msg: fmt.Sprintf("geen dienst %q; zoek de naam op met impact van een cluster of node", name)}
	case 1:
		return hit, nil
	}
	return uuid.Nil, assistant.ToolError{Msg: fmt.Sprintf("er zijn %d diensten %q; geef het id: %s", len(hits), name, strings.Join(hits, ", "))}
}

func known(names []string) string {
	if len(names) == 0 {
		return "(geen)"
	}
	if len(names) > 40 {
		return strings.Join(names[:40], ", ") + fmt.Sprintf(" en %d meer", len(names)-40)
	}
	return strings.Join(names, ", ")
}

// target is een cluster of een node uit de invoer: precies één van de twee.
type target struct {
	Cluster string `json:"cluster"`
	Node    string `json:"node"`
}

func (s *Server) target(ctx context.Context, c assistant.Caller, t target) (cluster, node *uuid.UUID, err error) {
	switch cl, n := strings.TrimSpace(t.Cluster), strings.TrimSpace(t.Node); {
	case cl != "" && n != "":
		return nil, nil, assistant.ToolError{Msg: "vul cluster of node in, niet allebei"}
	case cl != "":
		id, err := s.findCluster(ctx, c, cl)
		return &id, nil, err
	case n != "":
		id, err := s.findNode(ctx, c, n)
		return nil, &id, err
	}
	return nil, nil, assistant.ToolError{Msg: "vul een cluster of een node in"}
}

// Bouwstenen voor de schema's. Elk veld is verplicht, want strikte schema's
// laten maar weinig optionele velden toe; leeg betekent 'de standaard'.
func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func enum(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values, "description": desc}
}

const (
	clusterDesc = "Naam of id van het cluster, of leeg"
	nodeDesc    = "Hostname of id van de node, of leeg"
	fromDesc    = "Begin in ISO 8601 met tijdzone, zoals 2026-10-11T03:00:00+02:00, of leeg voor de standaard"
	toDesc      = "Einde (niet inbegrepen) in ISO 8601 met tijdzone, of leeg voor nu"
)

func targetProps(extra map[string]any) map[string]any {
	p := map[string]any{"cluster": strProp(clusterDesc), "node": strProp(nodeDesc)}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

// arg is een tekst uit de invoer voor een label, kort en zonder regels.
func arg(in map[string]any, k string) string {
	v, _ := in[k].(string)
	v = strings.Join(strings.Fields(v), " ")
	if r := []rune(v); len(r) > 40 {
		v = string(r[:39]) + "…"
	}
	return v
}

func of(in map[string]any) string {
	if v := arg(in, "cluster"); v != "" {
		return " van " + v
	}
	if v := arg(in, "node"); v != "" {
		return " van " + v
	}
	return ""
}

// Projecties van wat het model het vaakst nodig heeft.
func toolNode(n gen.Node) any {
	return compact(n, "created_at", "updated_at", "cluster_slug", "enrolled_at", "commands", "connection_id")
}

func toolTimeline(entries []gen.TimelineEntry) []any {
	out := make([]any, 0, len(entries))
	for _, e := range entries {
		m, _ := compact(e, "node_id", "cluster_id", "job_id").(map[string]any)
		if m == nil {
			continue
		}
		// Een venster alleen als het er is.
		if m["from"] == m["ts"] && m["to"] == m["ts"] {
			delete(m, "from")
			delete(m, "to")
		}
		out = append(out, m)
	}
	return out
}

// metricSummary vat een lijn samen: laagste, gemiddelde, hoogste en laatste
// waarde, en wanneer het hoogste was.
func metricSummary(ts []int64, values []gen.MetricSeries) []any {
	out := make([]any, 0, len(values))
	for _, s := range values {
		lo, hi, sum, n := math.Inf(1), math.Inf(-1), 0.0, 0
		var last *float64
		var hiAt int64
		for i, v := range s.Values {
			f, err := v.Get()
			if err != nil {
				continue
			}
			if f < lo {
				lo = f
			}
			if f > hi {
				hi = f
				if i < len(ts) {
					hiAt = ts[i]
				}
			}
			sum += f
			n++
			last = &f
		}
		if n == 0 {
			out = append(out, map[string]any{"label": s.Label, "metingen": 0})
			continue
		}
		out = append(out, map[string]any{
			"label": s.Label, "min": round(lo), "gem": round(sum / float64(n)), "max": round(hi), "laatste": round(*last),
			"max_om": time.Unix(hiAt, 0).UTC().Format("2006-01-02T15:04:05Z"), "metingen": n,
		})
	}
	return out
}

func round(f float64) float64 { return math.Round(f*100) / 100 }

// assistantTools zijn de tools van de assistent.
func (s *Server) assistantTools() []assistant.Tool {
	return []assistant.Tool{
		{
			Name:        "overzicht",
			Description: "Alle clusters en nodes met hun status en de reden ervan, en de open incidenten. Begin hiermee als de vraag niet over één cluster of node gaat, of om namen en ids op te zoeken.",
			Label:       func(map[string]any) string { return "Overzicht van de fleet lezen" },
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				if err := assistant.Decode(raw, &struct{}{}); err != nil {
					return nil, err
				}
				var cl list[gen.ClusterListItem]
				if err := s.view(ctx, c, s.ListClusters, &cl); err != nil {
					return nil, err
				}
				var nl list[gen.Node]
				if err := s.view(ctx, c, s.ListNodes, &nl); err != nil {
					return nil, err
				}
				var inc gen.IncidentList
				open, all, lim := gen.ListIncidentsParamsStatus("open"), true, 50
				if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) {
					from := time.Now().AddDate(-1, 0, 0)
					s.ListIncidents(w, r, gen.ListIncidentsParams{Status: &open, IncludeTests: &all, Limit: &lim, From: &from})
				}, &inc); err != nil {
					return nil, err
				}
				clusters := make([]any, len(cl.Items))
				for i, x := range cl.Items {
					clusters[i] = compact(map[string]any{
						"id": x.Id, "name": x.Name, "type": x.Type, "environment": x.Environment, "status": x.Status,
						"status_reason": x.StatusReason, "status_since": x.StatusSince, "node_count": x.NodeCount,
						"drift": x.Drift.Status, "impact": x.Impact, "impacted_by": x.ImpactedBy, "git_managed": x.GitManaged,
					})
				}
				nodes := make([]any, len(nl.Items))
				for i, n := range nl.Items {
					nodes[i] = compact(map[string]any{
						"id": n.Id, "hostname": n.Hostname, "cluster": n.ClusterName, "cluster_id": n.ClusterId, "role": n.Role,
						"lifecycle": n.Lifecycle, "status": n.Status, "status_reason": n.StatusReason, "status_since": n.StatusSince,
						"primary_ip": n.PrimaryIp, "drift": n.DriftStatus,
					})
				}
				incidents := compactAll(inc.Items, "updated_at", "detected_at", "note", "first_status", "last_status", "present", "cluster_id")
				out := map[string]any{"clusters": clusters, "open_incidenten": incidents}
				return withList(out, "nodes", nodes, false, false), nil
			},
		},
		{
			Name:        "cluster",
			Description: "Een cluster in detail: status en reden, nodes met hun status, VIP's met hun eigenaar, template en revisie.",
			Properties:  map[string]any{"cluster": strProp("Naam of id van het cluster")},
			Required:    []string{"cluster"},
			Label:       func(in map[string]any) string { return "Cluster " + arg(in, "cluster") + " bekijken" },
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in struct {
					Cluster string `json:"cluster"`
				}
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				id, err := s.findCluster(ctx, c, in.Cluster)
				if err != nil {
					return nil, err
				}
				var d gen.ClusterDetail
				if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.GetCluster(w, r, id) }, &d); err != nil {
					return nil, err
				}
				nodes := d.Nodes
				d.Nodes = nil
				out, _ := compact(d, "created_at", "updated_at", "slug").(map[string]any)
				ns := make([]any, len(nodes))
				for i, n := range nodes {
					ns[i] = toolNode(n)
				}
				return withList(out, "nodes", ns, false, false), nil
			},
		},
		{
			Name: "node",
			Description: "Een node in detail: status en reden, agent, Proxmox-VM, en de facts van de agent: OS, kernel, " +
				"openstaande updates, schijven, netwerk, keepalived, certificaten en de diensten die niet actief zijn.",
			Properties: map[string]any{"node": strProp("Hostname of id van de node")},
			Required:   []string{"node"},
			Label:      func(in map[string]any) string { return "Node " + arg(in, "node") + " bekijken" },
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in struct {
					Node string `json:"node"`
				}
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				id, err := s.findNode(ctx, c, in.Node)
				if err != nil {
					return nil, err
				}
				var n gen.Node
				if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.GetNode(w, r, id) }, &n); err != nil {
					return nil, err
				}
				var rt gen.NodeRuntime
				if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.GetNodeFacts(w, r, id) }, &rt); err != nil {
					return nil, err
				}
				out := map[string]any{"node": toolNode(n)}
				if hb, err := rt.Heartbeat.Get(); err == nil {
					out["heartbeat"] = compact(map[string]any{"received_at": hb.ReceivedAt, "load": hb.Load, "uptime_seconds": hb.UptimeSeconds})
				}
				if f, err := rt.Facts.Get(); err == nil {
					// Alleen de diensten die niet draaien; de rest is een telling.
					var services []any
					active := 0
					if list, err := f.Services.Get(); err == nil {
						for _, sv := range list {
							if sv.Active == "active" {
								active++
								continue
							}
							services = append(services, compact(sv))
						}
					}
					f.Services.SetNull()
					f.Routes = nil
					facts, _ := compact(f).(map[string]any)
					if facts != nil {
						facts["diensten_actief"] = active
						facts["diensten_niet_actief"] = services
					}
					out["facts"] = facts
					out["facts_van"] = compact(rt.CollectedAt)
				}
				return out, nil
			},
		},
		{
			Name: "tijdlijn",
			Description: "De tijdlijn van What Broke voor een cluster of een node, oudste eerst: wijzigingen uit het journaal " +
				"(pakketten, diensten, configuratie, reboots), symptomen (status, journaalfouten, afwijkingen in de metrics) en " +
				"acties van ClusterForge. Standaard de laatste 24 uur; hoogstens 7 dagen per keer.",
			Properties: targetProps(map[string]any{
				"van": strProp(fromDesc), "tot": strProp(toDesc),
				"klasse": enum("Alleen wijzigingen, alleen symptomen, of alles", "alles", "wijzigingen", "symptomen", "wijzigingen_en_symptomen"),
			}),
			Required: []string{"cluster", "node", "van", "tot", "klasse"},
			Label:    func(in map[string]any) string { return "Tijdlijn" + of(in) + " lezen" },
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in struct {
					target
					Van    string `json:"van"`
					Tot    string `json:"tot"`
					Klasse string `json:"klasse"`
				}
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				from, err := parseWhen("van", in.Van)
				if err != nil {
					return nil, err
				}
				to, err := parseWhen("tot", in.Tot)
				if err != nil {
					return nil, err
				}
				var class *string
				switch in.Klasse {
				case "wijzigingen":
					class = ptr("change")
				case "symptomen":
					class = ptr("symptom")
				case "wijzigingen_en_symptomen":
					class = ptr("change,symptom")
				}
				cluster, node, err := s.target(ctx, c, in.target)
				if err != nil {
					return nil, err
				}
				lim := 300
				var tl gen.Timeline
				h := func(w http.ResponseWriter, r *http.Request) {
					if cluster != nil {
						s.GetClusterTimeline(w, r, *cluster, gen.GetClusterTimelineParams{From: from, To: to, Class: class, Limit: &lim})
					} else {
						s.GetNodeTimeline(w, r, *node, gen.GetNodeTimelineParams{From: from, To: to, Class: class, Limit: &lim})
					}
				}
				if err := s.view(ctx, c, h, &tl); err != nil {
					return nil, err
				}
				return withList(map[string]any{}, "regels", toolTimeline(tl.Entries), true, tl.HasMore), nil
			},
		},
		{
			Name: "wijzigingen",
			Description: "Het wijzigingsjournaal van een cluster of node, nieuwste eerst: pakketten, diensten, configuratiebestanden, " +
				"kernel, reboots, gebruikers en firewall, met wie of wat het deed. Standaard de laatste 7 dagen.",
			Properties: targetProps(map[string]any{
				"van": strProp(fromDesc), "tot": strProp(toDesc),
				"soort": strProp("Alleen deze soorten met komma's, zoals package,service,config,kernel,reboot,user,firewall; of leeg voor alles"),
			}),
			Required: []string{"cluster", "node", "van", "tot", "soort"},
			Label:    func(in map[string]any) string { return "Wijzigingen" + of(in) + " lezen" },
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in struct {
					target
					Van   string `json:"van"`
					Tot   string `json:"tot"`
					Soort string `json:"soort"`
				}
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				from, err := parseWhen("van", in.Van)
				if err != nil {
					return nil, err
				}
				to, err := parseWhen("tot", in.Tot)
				if err != nil {
					return nil, err
				}
				var kind *string
				if k := strings.ReplaceAll(strings.TrimSpace(in.Soort), " ", ""); k != "" {
					kind = &k
				}
				cluster, node, err := s.target(ctx, c, in.target)
				if err != nil {
					return nil, err
				}
				lim := 300
				var ch gen.NodeChanges
				h := func(w http.ResponseWriter, r *http.Request) {
					if cluster != nil {
						s.ListClusterChanges(w, r, *cluster, gen.ListClusterChangesParams{From: from, To: to, Kind: kind, Limit: &lim})
					} else {
						s.ListNodeChanges(w, r, *node, gen.ListNodeChangesParams{From: from, To: to, Kind: kind, Limit: &lim})
					}
				}
				if err := s.view(ctx, c, h, &ch); err != nil {
					return nil, err
				}
				out := map[string]any{}
				if len(ch.AgentsTooOld) > 0 {
					out["agents_te_oud"] = compactAll(ch.AgentsTooOld)
				}
				return withList(out, "wijzigingen", compactAll(ch.Changes, "id", "node_id", "observed_at"), false, ch.HasMore), nil
			},
		},
		{
			Name: "incidenten",
			Description: "Incidenten van What Broke, nieuwste eerst, met ernst, wat geraakt was en de waarschijnlijke oorzaak. " +
				"Standaard de laatste 30 dagen, zonder incidenten van failovertests.",
			Properties: map[string]any{
				"status":  enum("Open, opgelost of allebei", "alle", "open", "resolved"),
				"cluster": strProp(clusterDesc), "van": strProp(fromDesc), "tot": strProp(toDesc),
				"tests": boolean("Ook incidenten die een failovertest veroorzaakte"),
			},
			Required: []string{"status", "cluster", "van", "tot", "tests"},
			Label:    func(in map[string]any) string { return "Incidenten" + of(in) + " zoeken" },
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in struct {
					Status  string `json:"status"`
					Cluster string `json:"cluster"`
					Van     string `json:"van"`
					Tot     string `json:"tot"`
					Tests   bool   `json:"tests"`
				}
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				p := gen.ListIncidentsParams{IncludeTests: &in.Tests, Limit: ptr(100)}
				var err error
				if p.From, err = parseWhen("van", in.Van); err != nil {
					return nil, err
				}
				if p.To, err = parseWhen("tot", in.Tot); err != nil {
					return nil, err
				}
				if in.Status == "open" || in.Status == "resolved" {
					p.Status = ptr(gen.ListIncidentsParamsStatus(in.Status))
				}
				if strings.TrimSpace(in.Cluster) != "" {
					id, err := s.findCluster(ctx, c, in.Cluster)
					if err != nil {
						return nil, err
					}
					p.Cluster = &id
				}
				var l gen.IncidentList
				if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.ListIncidents(w, r, p) }, &l); err != nil {
					return nil, err
				}
				items := compactAll(l.Items, "updated_at", "detected_at", "first_status", "last_status", "cluster_id")
				return withList(map[string]any{}, "incidenten", items, false, l.HasMore), nil
			},
		},
		{
			Name: "incident",
			Description: "Eén incident met de analyse van What Broke (eerste symptoom, waarschijnlijke oorzaken met hun redenen, " +
				"afwijkingen in de metrics, geraakte diensten), de tijdlijn eromheen en de geschiedenis.",
			Properties: map[string]any{"incident": strProp("Het nummer, zoals 14, of het id")},
			Required:   []string{"incident"},
			Label: func(in map[string]any) string {
				return "Incident " + strings.TrimPrefix(arg(in, "incident"), "#") + " lezen"
			},
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in struct {
					Incident string `json:"incident"`
				}
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				ref := strings.TrimPrefix(strings.TrimSpace(in.Incident), "#")
				id, err := uuid.Parse(ref)
				if err != nil {
					n, nerr := strconv.Atoi(ref)
					if nerr != nil {
						return nil, assistant.ToolError{Msg: fmt.Sprintf("%q is geen incidentnummer of id", in.Incident)}
					}
					if id, err = s.q.GetIncidentIDByNumber(ctx, int32(n)); err != nil {
						return nil, assistant.ToolError{Msg: fmt.Sprintf("incident #%d bestaat niet", n)}
					}
				}
				var d gen.IncidentDetail
				if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.GetIncident(w, r, id) }, &d); err != nil {
					return nil, err
				}
				out := map[string]any{
					"incident": compact(d.Incident, "updated_at"),
					"analyse":  compact(d.Analysis, "version"),
				}
				var events []gen.TimelineEntry
				for _, e := range d.Timeline {
					if e.Class != gen.Info {
						events = append(events, e)
					}
				}
				tl, dropped := fit(toolTimeline(events), 9*1024, true)
				out["tijdlijn"] = tl
				history := make([]any, 0, len(d.History))
				for _, h := range d.History {
					history = append(history, compact(map[string]any{"ts": h.Ts, "zin": h.Summary, "door": h.Actor.Name}))
				}
				hist, hdropped := fit(history, 3*1024, true)
				out["geschiedenis"] = hist
				if dropped > 0 || d.TimelineMore || hdropped > 0 || d.HistoryMore {
					out["let_op"] = "de oudste regels van de tijdlijn of de geschiedenis ontbreken; lees ze met de tool tijdlijn"
				}
				return out, nil
			},
		},
		{
			Name: "metrics",
			Description: "De vaste panelen met metrics van een cluster of node (CPU, geheugen, load, schijf, netwerk, temperatuur), " +
				"per lijn samengevat als laagste, gemiddelde, hoogste en laatste waarde en wanneer het hoogste was. " +
				"Een venster, of van en tot voor een vast venster van hoogstens 7 dagen.",
			Properties: targetProps(map[string]any{
				"venster": enum("Het venster tot nu; genegeerd als van en tot ingevuld zijn", "1h", "6h", "24h", "7d"),
				"van":     strProp(fromDesc), "tot": strProp(toDesc),
			}),
			Required: []string{"cluster", "node", "venster", "van", "tot"},
			Label:    func(in map[string]any) string { return "Metrics" + of(in) + " bekijken" },
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in struct {
					target
					Venster string `json:"venster"`
					Van     string `json:"van"`
					Tot     string `json:"tot"`
				}
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				from, err := parseWhen("van", in.Van)
				if err != nil {
					return nil, err
				}
				to, err := parseWhen("tot", in.Tot)
				if err != nil {
					return nil, err
				}
				var rng *gen.MetricsRange
				if from == nil || to == nil {
					from, to = nil, nil
					if in.Venster != "" {
						rng = ptr(gen.MetricsRange(in.Venster))
					}
				}
				cluster, node, err := s.target(ctx, c, in.target)
				if err != nil {
					return nil, err
				}
				var m gen.Metrics
				h := func(w http.ResponseWriter, r *http.Request) {
					if cluster != nil {
						s.GetClusterMetrics(w, r, *cluster, gen.GetClusterMetricsParams{Range: rng, From: from, To: to})
					} else {
						s.GetNodeMetrics(w, r, *node, gen.GetNodeMetricsParams{Range: rng, From: from, To: to})
					}
				}
				if err := s.view(ctx, c, h, &m); err != nil {
					return nil, err
				}
				panels := make([]any, len(m.Panels))
				for i, p := range m.Panels {
					panels[i] = map[string]any{"titel": p.Title, "eenheid": p.Unit, "lijnen": metricSummary(m.Timestamps, p.Series)}
				}
				return map[string]any{
					"van": shortTime(m.Start.Format(time.RFC3339)), "tot": shortTime(m.End.Format(time.RFC3339)),
					"stap_seconden": m.StepSeconds, "panelen": panels,
				}, nil
			},
		},
		{
			Name: "drift",
			Description: "De drift van een cluster: per node wat afwijkt van de gewenste staat (pakketten, diensten, bestanden, " +
				"sysctl) en sinds wanneer, volgens de laatste controle.",
			Properties: map[string]any{"cluster": strProp("Naam of id van het cluster")},
			Required:   []string{"cluster"},
			Label:      func(in map[string]any) string { return "Drift van " + arg(in, "cluster") + " bekijken" },
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in struct {
					Cluster string `json:"cluster"`
				}
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				id, err := s.findCluster(ctx, c, in.Cluster)
				if err != nil {
					return nil, err
				}
				var d gen.DriftReport
				if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.GetClusterDrift(w, r, id) }, &d); err != nil {
					return nil, err
				}
				return orNote(compact(d), "geen driftgegevens: het cluster heeft nog geen gewenste staat of is nog niet gecontroleerd"), nil
			},
		},
		{
			Name: "back_ups",
			Description: "Back-ups uit Proxmox: per VM de laatste back-up en of die vers genoeg is volgens het beleid van het " +
				"cluster, en de laatste controle met een terugzetting. Met een node alleen die node.",
			Properties: map[string]any{"node": strProp(nodeDesc)},
			Required:   []string{"node"},
			Label: func(in map[string]any) string {
				if v := arg(in, "node"); v != "" {
					return "Back-ups van " + v + " bekijken"
				}
				return "Back-ups bekijken"
			},
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in struct {
					Node string `json:"node"`
				}
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				if strings.TrimSpace(in.Node) == "" {
					var ov gen.BackupOverview
					if err := s.view(ctx, c, s.GetBackups, &ov); err != nil {
						return nil, err
					}
					items := compactAll(ov.Items)
					ov.Items = nil
					out, _ := compact(ov).(map[string]any)
					if out == nil {
						out = map[string]any{}
					}
					return withList(out, "vms", items, false, false), nil
				}
				id, err := s.findNode(ctx, c, in.Node)
				if err != nil {
					return nil, err
				}
				var nb any
				if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.GetNodeBackups(w, r, id) }, &nb); err != nil {
					return nil, err
				}
				return orNote(compact(nb), "geen back-ups bekend van deze node"), nil
			},
		},
		{
			Name: "testen",
			Description: "Testruns, nieuwste eerst: failovertests (met metingen zoals hoe lang de VIP weg was) en " +
				"back-upcontroles met een terugzetting. Een test is geen storing.",
			Properties: targetProps(map[string]any{
				"soort":    enum("Failovertests, back-upcontroles of allebei", "alle", "failover", "back-up"),
				"uitkomst": enum("Alleen deze uitkomst", "alle", "pass", "warning", "fail", "error", "canceled", "skipped"),
			}),
			Required: []string{"cluster", "node", "soort", "uitkomst"},
			Label:    func(in map[string]any) string { return "Testruns" + of(in) + " bekijken" },
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in struct {
					target
					Soort    string `json:"soort"`
					Uitkomst string `json:"uitkomst"`
				}
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				p := gen.ListTestRunsParams{Limit: ptr(30)}
				switch in.Soort {
				case "failover":
					p.Kind = ptr(gen.ListTestRunsParamsKindFailoverTest)
				case "back-up":
					p.Kind = ptr(gen.ListTestRunsParamsKindBackupVerify)
				}
				if in.Uitkomst != "" && in.Uitkomst != "alle" {
					p.Result = ptr(gen.TestRunResult(in.Uitkomst))
				}
				if in.Cluster != "" || in.Node != "" {
					var err error
					if p.ClusterId, p.NodeId, err = s.target(ctx, c, in.target); err != nil {
						return nil, err
					}
				}
				var l gen.TestRunList
				if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.ListTestRuns(w, r, p) }, &l); err != nil {
					return nil, err
				}
				return withList(map[string]any{}, "testruns", compactAll(l.Items, "timeline", "definition"), false, false), nil
			},
		},
		{
			Name: "taken",
			Description: "Taken van ClusterForge, nieuwste eerst: uitrollen, lifecycle-acties, failovertests, Git-wijzigingen " +
				"toepassen en meer, met status, fout en wie ze vroeg. Lees de stappen van één taak met de tool taak.",
			Properties: targetProps(nil),
			Required:   []string{"cluster", "node"},
			Label:      func(in map[string]any) string { return "Taken" + of(in) + " bekijken" },
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in target
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				p := gen.ListJobsParams{Limit: ptr(40)}
				if in.Cluster != "" || in.Node != "" {
					var err error
					if p.ClusterId, p.NodeId, err = s.target(ctx, c, in); err != nil {
						return nil, err
					}
				}
				var l list[gen.Job]
				if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.ListJobs(w, r, p) }, &l); err != nil {
					return nil, err
				}
				return withList(map[string]any{}, "taken", compactAll(l.Items, "retryable", "cancel_requested", "proxmox_id"), false, false), nil
			},
		},
		{
			Name:        "taak",
			Description: "Eén taak met zijn stappen en hun fout. Met log ook de laatste 50 regels van het log van elke stap.",
			Properties:  map[string]any{"id": strProp("Het id van de taak"), "log": boolean("Ook de logregels van de stappen")},
			Required:    []string{"id", "log"},
			Label:       func(map[string]any) string { return "Taak bekijken" },
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in struct {
					ID  string `json:"id"`
					Log bool   `json:"log"`
				}
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				id, err := uuid.Parse(strings.TrimSpace(in.ID))
				if err != nil {
					return nil, assistant.ToolError{Msg: fmt.Sprintf("%q is geen id van een taak", in.ID)}
				}
				var d gen.JobDetail
				if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.GetJob(w, r, id) }, &d); err != nil {
					return nil, err
				}
				for i := range d.Steps {
					if !in.Log {
						d.Steps[i].Log = nil
					} else if n := len(d.Steps[i].Log); n > 50 {
						d.Steps[i].Log = d.Steps[i].Log[n-50:]
					}
				}
				return compact(d, "retryable", "cancel_requested", "proxmox_id"), nil
			},
		},
		{
			Name: "git",
			Description: "GitOps: wijzigingen uit de Git-repository (gepland, goedgekeurd, toegepast, mislukt) met hun commit, " +
				"en met een cluster ook de revisies van zijn spec met wat er veranderde.",
			Properties: map[string]any{
				"cluster": strProp(clusterDesc),
				"status":  enum("Alleen deze status", "alle", "pending", "applying", "applied", "failed", "rejected", "superseded"),
			},
			Required: []string{"cluster", "status"},
			Label:    func(in map[string]any) string { return "Git-wijzigingen" + of(in) + " bekijken" },
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in struct {
					Cluster string `json:"cluster"`
					Status  string `json:"status"`
				}
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				p := gen.ListGitChangesParams{Limit: ptr(25)}
				if in.Status != "" && in.Status != "alle" {
					p.Status = ptr(gen.GitChangeStatus(in.Status))
				}
				out := map[string]any{}
				if strings.TrimSpace(in.Cluster) != "" {
					id, err := s.findCluster(ctx, c, in.Cluster)
					if err != nil {
						return nil, err
					}
					p.ClusterId = &id
					var h gen.SpecHistory
					if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.ListSpecRevisions(w, r, id) }, &h); err != nil {
						return nil, err
					}
					revs := h.Items
					if len(revs) > 10 {
						revs = revs[:10]
					}
					h.Items = nil
					spec, _ := compact(h).(map[string]any)
					if spec == nil {
						spec = map[string]any{}
					}
					spec["revisies"] = compactAll(revs)
					out["spec"] = spec
				}
				var l list[gen.GitChange]
				if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.ListGitChanges(w, r, p) }, &l); err != nil {
					return nil, err
				}
				return withList(out, "wijzigingen", compactAll(l.Items, "slug", "updated_at"), false, false), nil
			},
		},
		{
			Name: "impact",
			Description: "Wat er gebeurt als een dienst, cluster of node uitvalt: welke diensten ervan afhangen, direct of via " +
				"andere, per cluster of groep, met het pad. Vul precies één in.",
			Properties: map[string]any{
				"dienst": strProp("Naam of id van een dienst, of leeg"), "cluster": strProp(clusterDesc), "node": strProp(nodeDesc),
			},
			Required: []string{"dienst", "cluster", "node"},
			Label: func(in map[string]any) string {
				if v := arg(in, "dienst"); v != "" {
					return "Impact van " + v + " bekijken"
				}
				return "Impact" + of(in) + " bekijken"
			},
			Run: func(ctx context.Context, c assistant.Caller, raw json.RawMessage) (any, error) {
				var in struct {
					Dienst string `json:"dienst"`
					target
				}
				if err := assistant.Decode(raw, &in); err != nil {
					return nil, err
				}
				var p gen.GetImpactParams
				if strings.TrimSpace(in.Dienst) != "" {
					if in.Cluster != "" || in.Node != "" {
						return nil, assistant.ToolError{Msg: "vul precies één van dienst, cluster en node in"}
					}
					id, err := s.findService(ctx, in.Dienst)
					if err != nil {
						return nil, err
					}
					p.ServiceId = &id
				} else {
					var err error
					if p.ClusterId, p.NodeId, err = s.target(ctx, c, in.target); err != nil {
						return nil, err
					}
				}
				var im gen.Impact
				if err := s.view(ctx, c, func(w http.ResponseWriter, r *http.Request) { s.GetImpact(w, r, p) }, &im); err != nil {
					return nil, err
				}
				items := compactAll(im.Items, "dependency_id", "group_id")
				im.Items = nil
				out, _ := compact(im, "group_id").(map[string]any)
				if out == nil {
					out = map[string]any{}
				}
				return withList(out, "diensten", items, false, false), nil
			},
		},
	}
}

func ptr[T any](v T) *T { return &v }
