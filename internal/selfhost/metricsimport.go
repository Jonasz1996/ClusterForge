package selfhost

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// ImportNode is hoe het inladen van de metrics van de oude server op één
// node staat (fase 48, 4.5). Na een verhuizing laadt de VIP-node ze op de
// achtergrond in, op alle nodes tegelijk; de server draait dan al.
type ImportNode struct {
	Address  string     `json:"address"`
	Hostname string     `json:"hostname,omitempty"`
	State    string     `json:"state"`
	Started  *time.Time `json:"started_at,omitempty"`
	Ended    *time.Time `json:"ended_at,omitempty"`
	Bytes    int64      `json:"bytes,omitempty"`
	Reason   string     `json:"reason,omitempty"`
}

// parseImport leest clusterforge-ctl.sh metrics-status: per regel
// <adres> <toestand> <start> <eind of bytes> [reden].
func parseImport(lines []string) []ImportNode {
	var out []ImportNode
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		n := ImportNode{Address: f[0], State: f[1]}
		switch n.State {
		case "bezig", "klaar", "mislukt":
		default:
			continue
		}
		unix := func(s string) *time.Time {
			v, err := strconv.ParseInt(s, 10, 64)
			if err != nil || v <= 0 {
				return nil
			}
			t := time.Unix(v, 0).UTC()
			return &t
		}
		n.Started = unix(f[2])
		if len(f) > 3 {
			if n.State == "bezig" {
				n.Bytes, _ = strconv.ParseInt(f[3], 10, 64)
			} else {
				n.Ended = unix(f[3])
			}
		}
		if n.State == "mislukt" && len(f) > 4 {
			n.Reason = strings.Join(f[4:], " ")
		}
		out = append(out, n)
	}
	return out
}

// MetricsImport vraagt de nodes van het cluster waarop ClusterForge
// draait hoe het inladen van de metrics staat. Leeg: er loopt er geen en
// er liep er geen.
func (s *Service) MetricsImport(ctx context.Context) ([]ImportNode, error) {
	out := []ImportNode{}
	if s.Cluster == "" || s.Run == nil {
		return out, nil
	}
	var cluster uuid.UUID
	if err := s.pool.QueryRow(ctx, "SELECT id FROM clusters WHERE slug = $1", s.Cluster).Scan(&cluster); err != nil {
		return out, nil //nolint:nilerr // geen cluster: niets in te laden
	}
	agents, err := s.agents(ctx)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, a := range agents {
		if a.Cluster != nil && *a.Cluster == cluster && a.Address != "" {
			names[a.Address] = a.Hostname
		}
	}
	for _, a := range agents {
		if a.Cluster == nil || *a.Cluster != cluster || !a.Online {
			continue
		}
		c := &capture{}
		spec := protocol.RunSpec{ID: "cf-metrics-status-" + uuid.NewString(), Kind: protocol.RunScript, Title: "metrics-status",
			Script: ctl("metrics-status"), TimeoutSeconds: 30}
		res, err := s.Run.DoWith(ctx, c, &memState{}, a.NodeID, spec)
		if err != nil || res.State != protocol.RunOK {
			continue
		}
		for _, n := range parseImport(c.lines) {
			if n.Address == "127.0.0.1" {
				n.Address, n.Hostname = a.Address, a.Hostname
			} else {
				n.Hostname = names[n.Address]
			}
			out = append(out, n)
		}
		if len(out) > 0 {
			// Alleen de node die overnam heeft statusbestanden.
			break
		}
	}
	slices.SortFunc(out, func(a, b ImportNode) int { return strings.Compare(a.Hostname+a.Address, b.Hostname+b.Address) })
	return out, nil
}
