package updates

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Hervatten na een fout (fase 47): een gestopte clusterupdate gaat verder
// bij de node waar ze stopte, met dezelfde keuzes, in plaats van opnieuw
// vanaf het begin. Mislukte de Docker-update van één project, dan kan dat
// project deze keer overgeslagen worden.

// ErrNotResumable: de taak is geen gestopte clusterupdate.
var ErrNotResumable = &ConflictError{"alleen een gestopte clusterupdate kun je hervatten"}

// ResumeHint is waar een gestopte clusterupdate kan hervatten.
type ResumeHint struct {
	NodeID   uuid.UUID
	Hostname string
	// Done zijn de nodes die al bijgewerkt waren.
	Done []string
	// Projects zijn de Compose-projecten waarvan de update op die node
	// mislukte.
	Projects []string
}

func (s *Service) resumable(ctx context.Context, jobID uuid.UUID) (store.Job, params, error) {
	row, err := s.q.GetJob(ctx, jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Job{}, params{}, ErrNotResumable
	}
	if err != nil {
		return store.Job{}, params{}, err
	}
	j := row.Job
	if j.Kind != Kind || j.ClusterID == nil || (j.Status != store.JobStatusFailed && j.Status != store.JobStatusCanceled) {
		return store.Job{}, params{}, ErrNotResumable
	}
	var p params
	if err := json.Unmarshal(j.Params, &p); err != nil {
		return store.Job{}, params{}, err
	}
	return j, p, nil
}

// ResumeHint zoekt de node waar de update stopte.
func (s *Service) ResumeHint(ctx context.Context, jobID uuid.UUID) (ResumeHint, error) {
	j, p, err := s.resumable(ctx, jobID)
	if err != nil {
		return ResumeHint{}, err
	}
	steps, err := s.q.ListJobSteps(ctx, j.ID)
	if err != nil {
		return ResumeHint{}, err
	}
	var h ResumeHint
	for _, n := range p.Nodes {
		ok, started := true, false
		for _, st := range steps {
			if strings.HasPrefix(st.Name, n.Hostname+":") || strings.HasPrefix(st.Name, n.Hostname+" (VIP-houder):") {
				started = true
				if st.Status != store.JobStatusSucceeded {
					ok = false
				}
			}
		}
		// Een node zonder stappen kwam niet aan de beurt.
		if ok && started {
			h.Done = append(h.Done, n.Hostname)
			continue
		}
		h.NodeID, h.Hostname = n.ID, n.Hostname
		break
	}
	if h.NodeID == uuid.Nil {
		return ResumeHint{}, &ConflictError{"elke node van deze update is al gelukt"}
	}
	reps, err := s.q.ListDockerReports(ctx)
	if err != nil {
		return ResumeHint{}, err
	}
	for _, r := range reps {
		if r.NodeID == h.NodeID && r.JobID != nil && *r.JobID == j.ID && (r.Result == protocol.DockerFailed || r.Result == protocol.DockerRolledBack) {
			h.Projects = append(h.Projects, r.Project)
		}
	}
	return h, nil
}

// ResumeRequest is de update die verder gaat: dezelfde keuzes, vanaf from
// (leeg: waar ze stopte), met skip als overgeslagen projecten.
func (s *Service) ResumeRequest(ctx context.Context, jobID uuid.UUID, from *uuid.UUID, skip []string) (Request, error) {
	j, p, err := s.resumable(ctx, jobID)
	if err != nil {
		return Request{}, err
	}
	if from == nil {
		h, err := s.ResumeHint(ctx, jobID)
		if err != nil {
			return Request{}, err
		}
		from = &h.NodeID
	}
	req := Request{ClusterID: j.ClusterID, Apt: p.Apt, Reboot: p.Reboot, NoFailover: p.NoFailover, StartFrom: from}
	if p.Docker != nil {
		d := *p.Docker
		d.Skip = append(slices.Clone(d.Skip), skip...)
		// Een bewerking uit de editor hoort bij die ene keer.
		d.Edit = nil
		req.Docker = &d
	} else if len(skip) > 0 {
		return Request{}, &FieldError{"deze update deed geen Docker; er is niets over te slaan"}
	}
	return req, nil
}

// Resume start de update die verder gaat.
func (s *Service) Resume(ctx context.Context, actor events.Actor, req Request, nodes []Node) ([]store.Job, []string, error) {
	return s.Start(ctx, actor, req, nodes)
}
