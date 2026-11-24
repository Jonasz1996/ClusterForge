package httpapi

import (
	"net/http"
	"time"

	"github.com/google/uuid"
)

// Testlijst 3, N5: wat ClusterForge de laatste 24 uur vanzelf veranderde
// (CrowdSec of de firewall toegepast, publicatie, onderhoud, updates). Een
// automatische wijziging op de proxy van alle sites mag niet onopgemerkt
// blijven; per regel de taak en waar je ze terugdraait.

type autoChange struct {
	JobID    uuid.UUID  `json:"job_id"`
	Kind     string     `json:"kind"`
	Title    string     `json:"title"`
	Status   string     `json:"status"`
	At       time.Time  `json:"at"`
	NodeID   *uuid.UUID `json:"node_id,omitempty"`
	Hostname string     `json:"hostname,omitempty"`
	Cluster  string     `json:"cluster,omitempty"`
	// Undo zegt waar je de wijziging terugdraait.
	Undo     string `json:"undo"`
	UndoHref string `json:"undo_href"`
}

var autoKinds = map[string][2]string{
	"crowdsec.apply": {"CrowdSec: bij de node Verwijderen (en automatisch toepassen uit in CrowdSec → Instellingen)", "/crowdsec?tab=nodes"},
	"firewall.apply": {"Firewall: de node op voorbeeld zetten; de agent zet de vorige regels terug bij een mislukte controle", "/firewall?tab=nodes"},
	"publish.apply":  {"Proxy hosts → Laatste 24 uur door ClusterForge veranderd: per host terugzetten", "/publicatie/npm"},
	"upkeep.run":     {"Onderhoud verwijdert alleen oude logs en caches; niets terug te zetten", "/onderhoud"},
	"updates.run":    {"Updates: bij de node de vorige versie (snapshot of pakketversie)", "/updates"},
	"agents.upgrade": {"Agents: Nodes → Agents bijwerken met een vorige versie", "/nodes"},
	"heal.run":       {"Herstel: zie de taak", "/taken"},
	"cluster.apply":  {"Drift: zie de taak; de vorige bestanden staan als .cf-vorige op de node", "/taken"},
	"updates.queue":  {"Updates: zie de taak", "/updates"},
}

func (s *Server) autoChanges(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	kinds := make([]string, 0, len(autoKinds))
	for k := range autoKinds {
		kinds = append(kinds, k)
	}
	rows, err := s.pool.Query(r.Context(), `SELECT j.id, j.kind, j.title, j.status::text, coalesce(j.finished_at, j.created_at), j.node_id,
		coalesce(n.hostname, ''), coalesce(c.name, '')
		FROM jobs j
		LEFT JOIN nodes n ON n.id = j.node_id
		LEFT JOIN clusters c ON c.id = coalesce(j.cluster_id, n.cluster_id)
		WHERE j.requested_by IS NULL AND j.kind = ANY($1) AND j.created_at > now() - interval '24 hours'
		AND j.status IN ('succeeded', 'failed')
		ORDER BY j.created_at DESC LIMIT 200`, kinds)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	defer rows.Close()
	out := []autoChange{}
	for rows.Next() {
		var c autoChange
		if err := rows.Scan(&c.JobID, &c.Kind, &c.Title, &c.Status, &c.At, &c.NodeID, &c.Hostname, &c.Cluster); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		u := autoKinds[c.Kind]
		c.Undo, c.UndoHref = u[0], u[1]
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}
