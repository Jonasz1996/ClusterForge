// Package whatbroke zet bij een storing op een rij wat er gebeurde en wat
// de waarschijnlijke oorzaak is. De tijdlijn voegt het logboek en het
// journaal samen; de analyse kiest het eerste symptoom en rangschikt de
// veranderingen ervoor met vaste regels. Alles is vaste logica, zonder
// taalmodel, en leest alleen.
package whatbroke

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/audit"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/journal"
	"github.com/Jonasz1996/clusterforge/internal/status"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Bronnen van een regel in de tijdlijn. Het journaal heeft zijn eigen
// bronnen (agent, server, proxmox, clusterforge); het logboek is log.
const (
	SourceLog     = "log"
	SourceMetrics = "metrics"
)

// Entry is één regel van de tijdlijn.
type Entry struct {
	// ID is "e" en het id van een event, "c" en het id van een regel uit het
	// journaal, of "m" voor een afwijking in de metrics.
	ID string    `json:"id"`
	Ts time.Time `json:"ts"`
	// From en To zijn het venster waarin het waarschijnlijk gebeurde. Een
	// node gaat pas na 90 s zonder heartbeat op down, dus dan ligt From ver
	// voor Ts. Meestal is To gelijk aan Ts.
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// Delay legt een venster uit; leeg als From gelijk is aan Ts.
	Delay   string       `json:"delay"`
	Class   events.Class `json:"class"`
	Source  string       `json:"source"`
	Kind    string       `json:"kind"`
	Action  string       `json:"action"`
	Summary string       `json:"summary"`
	NodeID  *uuid.UUID   `json:"node_id"`
	// Hostname is de node, Cluster de naam van het cluster.
	Hostname  string     `json:"hostname"`
	ClusterID *uuid.UUID `json:"cluster_id"`
	Cluster   string     `json:"cluster"`
	// Subject is het pakket, de unit of het pad uit het journaal.
	Subject string     `json:"subject"`
	JobID   *uuid.UUID `json:"job_id"`
	// Job is de titel van de taak, als het logboek hem kent.
	Job string `json:"job"`
	// Link is de pagina in de webinterface.
	Link string `json:"link"`
	// Detail zijn een paar vaste velden: van en naar, de reden, de
	// Proxmox-host, de commit.
	Detail map[string]string `json:"detail"`
}

// Vertragingen van de bronnen: hoe lang na het echte moment ClusterForge
// het ziet.
const (
	// statusDelay is een heartbeat plus een ronde van de evaluator.
	statusDelay = 10*time.Second + status.Interval
	// heartbeatDelay is hoe lang een node zonder heartbeat nog niet down is.
	heartbeatDelay = status.HeartbeatDown + status.Interval
	// syncDelay is de interval van de Proxmox-sync.
	syncDelay = 20 * time.Second
	// driftDelay is de interval van de driftcontrole.
	driftDelay = 15 * time.Minute
	// dbWatchDelay is de interval van de bewaking van de databases.
	dbWatchDelay = 20 * time.Second
)

// FromEvent maakt een regel van een event uit het logboek.
func FromEvent(e audit.Entry) Entry {
	en := Entry{
		ID: "e" + strconv.FormatInt(e.ID, 10), Ts: e.Ts, From: e.Ts, To: e.Ts, Class: events.ClassOf(e.Action, e.Payload),
		Source: SourceLog, Kind: eventKind(e.Action), Action: e.Action, Summary: e.Summary, Detail: map[string]string{},
	}
	if e.Node != nil {
		if id, err := uuid.Parse(e.Node.ID); err == nil {
			en.NodeID, en.Hostname = &id, e.Node.Name
		}
	}
	if e.Cluster != nil {
		if id, err := uuid.Parse(e.Cluster.ID); err == nil {
			en.ClusterID, en.Cluster = &id, e.Cluster.Name
		}
	}
	if e.Job != nil {
		if id, err := uuid.Parse(e.Job.ID); err == nil {
			en.JobID, en.Job = &id, e.Job.Name
		}
	}
	for _, k := range []string{"from", "to", "reason", "host", "address", "commit", "name", "impact", "namespace", "kind"} {
		if v := payloadText(e.Payload[k]); v != "" {
			en.Detail[k] = v
		}
	}
	if e.Subject.Type == "proxmox" {
		en.Detail["connection"] = e.Subject.ID
	}
	switch e.Action {
	case "node.status_changed":
		if strings.Contains(en.Detail["reason"], "heartbeat") {
			en.From, en.Delay = e.Ts.Add(-heartbeatDelay), "een node gaat pas na 90 s zonder heartbeat op down"
		} else {
			en.From, en.Delay = e.Ts.Add(-statusDelay), "de status komt uit de heartbeat van elke 10 s"
		}
	case "cluster.status_changed", "service.status_changed", "vip.owner_changed":
		en.From, en.Delay = e.Ts.Add(-statusDelay), "de status komt uit de heartbeat van elke 10 s"
	case "proxmox.host_offline", "proxmox.sync_failed", "vm.status_changed", "vm.missing", "vm.moved":
		en.From, en.Delay = e.Ts.Add(-syncDelay), "Proxmox wordt elke 20 s gelezen"
	case "drift.detected", "drift.changed":
		en.From, en.Delay = e.Ts.Add(-driftDelay), "drift wordt elk kwartier gecontroleerd"
	case "kube.rolled_out":
		rolledOut(&en, payloadText(e.Payload["at"]))
	case "kube.app_status_changed":
		en.From, en.Delay = e.Ts.Add(-statusDelay), "de status van een app komt uit de sync van elke 30 s"
	case "cluster.database_down", "cluster.database_up", "cluster.database_node_back":
		en.From, en.Delay = e.Ts.Add(-dbWatchDelay), "de databases worden elke 20 s bekeken"
	case "cluster.database_node_out":
		en.From, en.Delay = e.Ts.Add(-5*time.Minute-dbWatchDelay), "een databasenode komt pas na 5 minuten in het logboek"
	}
	if !e.Subject.Deleted {
		en.Link = eventLink(e)
	}
	return en
}

func payloadText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}

// eventKind deelt de actions in voor de filters en de gewichten.
func eventKind(action string) string {
	prefix, _, _ := strings.Cut(action, ".")
	switch {
	case prefix == "job":
		return "job"
	case action == "agent.command":
		return "command"
	case prefix == "gitops" || strings.HasPrefix(action, "cluster.git_"):
		return "gitops"
	case prefix == "drift":
		return "drift"
	case prefix == "vm":
		return "vm"
	case prefix == "proxmox":
		return "proxmox"
	case prefix == "failover" || prefix == "failover_test" || action == "backup.verify_finished":
		return "test"
	case prefix == "backup":
		return "backup"
	case action == "node.status_changed" || action == "cluster.status_changed" || action == "vip.owner_changed" ||
		action == "service.status_changed" || action == "kube.app_status_changed" || action == "cluster.database_down" ||
		action == "cluster.database_up" || action == "cluster.database_node_out" || action == "cluster.database_node_back":
		return "status"
	case prefix == "incident":
		return "incident"
	case action == "node.lifecycle_changed":
		return "lifecycle"
	case action == "cluster.updated" || action == "cluster.deployed" || action == "cluster.spec_changed" ||
		action == "secret.created" || action == "node.updated" || prefix == "vip" || prefix == "kube":
		return "config"
	case prefix == "agent" || prefix == "enrollment_token" || action == "node.facts_changed":
		return "agent"
	case prefix == "service" || prefix == "dependency":
		return "deps"
	}
	return "other"
}

func eventLink(e audit.Entry) string {
	switch e.Subject.Type {
	case "cluster":
		return "/clusters/detail?id=" + e.Subject.ID
	case "node":
		return "/nodes/detail?id=" + e.Subject.ID
	case "job":
		return "/taken/detail?id=" + e.Subject.ID
	case "test_run":
		return "/tests/detail?id=" + e.Subject.ID
	case "incident":
		return "/incidenten/detail?id=" + e.Subject.ID
	case "git_change":
		return "/gitops"
	case "proxmox":
		return "/proxmox"
	}
	switch {
	case e.Job != nil && !e.Job.Deleted:
		return "/taken/detail?id=" + e.Job.ID
	case e.Node != nil && !e.Node.Deleted:
		return "/nodes/detail?id=" + e.Node.ID
	case e.Cluster != nil && !e.Cluster.Deleted:
		return "/clusters/detail?id=" + e.Cluster.ID
	}
	return ""
}

// FromChange maakt een regel van een regel uit het journaal.
func FromChange(r store.ListChangesInScopeRow) Entry {
	id := r.NodeID
	en := Entry{
		ID: "c" + strconv.FormatInt(r.ID, 10), Ts: r.Ts, From: r.Ts, To: r.Ts, Class: changeClass(r.Kind, r.Action, r.Subject),
		Source: r.Source, Kind: r.Kind, Action: r.Action, Summary: r.Summary, NodeID: &id, Hostname: r.Hostname,
		ClusterID: r.ClusterID, Cluster: r.ClusterName, Subject: r.Subject, JobID: r.JobID, Detail: map[string]string{},
		Link: "/nodes/detail?id=" + r.NodeID.String(),
	}
	if r.JobID != nil {
		en.Link = "/taken/detail?id=" + r.JobID.String()
	}
	var d map[string]any
	if json.Unmarshal(r.Detail, &d) == nil {
		for k, v := range d {
			if s := payloadText(v); s != "" {
				en.Detail[k] = s
			}
		}
	}
	// Van protocol 4 leidt de server diensten af uit de heartbeats.
	if r.Source == journal.SourceServer && r.Kind == journal.KindService {
		en.From, en.Delay = r.Ts.Add(-statusDelay), "afgeleid uit de heartbeat van elke 10 s"
	}
	return en
}

// changeClass geeft de klasse van een regel uit het journaal: een dienst die
// faalt, een OOM-kill en een onderbreking van de agent zijn symptomen, wat
// iemand of iets op de node veranderde is een verandering.
func changeClass(kind, action, subject string) events.Class {
	switch kind {
	case journal.KindService:
		if action == "failed" || action == "auto_restarted" {
			return events.ClassSymptom
		}
		return events.ClassChange
	case journal.KindOOM:
		return events.ClassSymptom
	case journal.KindAgent:
		if action == "gap" {
			return events.ClassSymptom
		}
		return events.ClassInfo
	case journal.KindFacts:
		if subject == "os" {
			return events.ClassChange
		}
		return events.ClassInfo
	case journal.KindVM:
		switch action {
		case "vzdump", "vncproxy", "termproxy", "vncshell":
			return events.ClassInfo
		}
		return events.ClassChange
	}
	return events.ClassChange
}

// when zegt waar t ligt ten opzichte van het eerste symptoom, dat tussen
// from en to begon, in gewone woorden.
func when(t, from, to time.Time) string {
	const tolerance = 5 * time.Second
	switch {
	case t.Before(from.Add(-tolerance)):
		return span(from.Sub(t)) + " voor het eerste symptoom"
	case t.After(to.Add(tolerance)):
		return span(t.Sub(to)) + " na het eerste symptoom"
	}
	return "tegelijk met het eerste symptoom"
}

// span is een duur in gewone woorden: 40 s, 12 min, 1 u 20 min.
func span(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	h, m := int(d.Hours()), int(d.Minutes())%60
	if m == 0 {
		return fmt.Sprintf("%d u", h)
	}
	return fmt.Sprintf("%d u %d min", h, m)
}

// Redact haalt het IP-adres uit een SSH-login in het journaal, voor een
// viewer en voor de analyse.
func Redact(e Entry) Entry {
	if e.Source == SourceLog || e.Kind != journal.KindLogin || e.Detail["ip"] == "" {
		return e
	}
	d := make(map[string]string, len(e.Detail))
	for k, v := range e.Detail {
		if k != "ip" {
			d[k] = v
		}
	}
	e.Detail = d
	e.Summary = journal.Describe(journal.Entry{Kind: e.Kind, Subject: e.Subject, Action: e.Action, Detail: d}, true)
	return e
}
