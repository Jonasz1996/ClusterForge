package updates

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// DescribeDocker zegt in één regel wat een Docker-update met een project
// deed.
func DescribeDocker(d protocol.DockerReport) string {
	var b strings.Builder
	switch d.Result {
	case protocol.DockerUpToDate:
		fmt.Fprintf(&b, "%s: al bij", d.Project)
	case protocol.DockerUpdated:
		fmt.Fprintf(&b, "%s: bijgewerkt en gezond", d.Project)
	case protocol.DockerRolledBack:
		fmt.Fprintf(&b, "%s: teruggezet naar de vorige versie (%s)", d.Project, d.Problem)
	default:
		fmt.Fprintf(&b, "%s: mislukt (%s)", d.Project, d.Problem)
	}
	for _, s := range d.Services {
		if s.NewID == "" || s.NewID == s.OldID {
			continue
		}
		fmt.Fprintf(&b, "; %s %s", s.Service, s.Image)
		if s.OldVersion != "" || s.NewVersion != "" {
			fmt.Fprintf(&b, " (%s → %s)", orQ(s.OldVersion), orQ(s.NewVersion))
		}
		if len(s.EnvAdded) > 0 {
			fmt.Fprintf(&b, ", nieuwe variabelen: %s", strings.Join(s.EnvAdded, ", "))
		}
		if len(s.EnvRemoved) > 0 {
			fmt.Fprintf(&b, ", verdwenen variabelen: %s", strings.Join(s.EnvRemoved, ", "))
		}
	}
	return b.String()
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// saveReports bewaart het rapport per project, voor de pagina Updates en
// Fix voorstellen.
func (s *Service) saveReports(ctx context.Context, j *jobs.Job, n jobNode, reps []protocol.DockerReport) {
	for _, d := range reps {
		data, err := json.Marshal(d)
		if err != nil {
			continue
		}
		_ = s.q.SaveDockerReport(ctx, store.SaveDockerReportParams{
			NodeID: n.ID, Project: d.Project, JobID: &j.ID, Result: d.Result, Report: data,
		})
	}
}
