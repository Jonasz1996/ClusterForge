package httpapi

import (
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/kubelessons"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// lessonEnv is het cluster, de gebruiker, het VIP, de omgeving en het
// aantal nodes en servers uit de laatste sync.
func (s *Server) lessonEnv(r *http.Request, clusterID uuid.UUID) (kubelessons.Env, error) {
	ctx := r.Context()
	p, _ := principalFrom(ctx)
	c, err := s.kube.Cluster(ctx, clusterID)
	if err != nil {
		return kubelessons.Env{}, err
	}
	nodes, err := s.q.ListKubeNodes(ctx, clusterID)
	if err != nil {
		return kubelessons.Env{}, err
	}
	servers := 0
	for _, n := range nodes {
		if slices.Contains(n.Roles, "control-plane") || slices.Contains(n.Roles, "master") {
			servers++
		}
	}
	vips, err := s.clusterVIPs(ctx)
	if err != nil {
		return kubelessons.Env{}, err
	}
	return kubelessons.Env{
		ClusterID: clusterID, UserID: p.User.ID, VIP: vips[clusterID], Nodes: len(nodes), Servers: servers, Environment: string(c.Environment),
	}, nil
}

func nullableTimePtr(t *time.Time) nullable.Nullable[time.Time] {
	if t == nil {
		return nullable.NewNullNullable[time.Time]()
	}
	return nullable.NewNullableWithValue(*t)
}

func (s *Server) ListKubeLessons(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID) {
	env, err := s.lessonEnv(r, clusterID)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	sums, err := s.lessons.List(r.Context(), env)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]gen.KubeLessonSummary, 0, len(sums))
	for _, sum := range sums {
		l := sum.Lesson
		items = append(items, gen.KubeLessonSummary{
			Id: l.ID, Number: l.Number, Title: l.Title, Summary: l.Summary, Steps: len(l.Steps), Done: sum.Done,
			Unavailable: sum.Unavailable, StartedAt: nullableTimePtr(sum.StartedAt), FinishedAt: nullableTimePtr(sum.FinishedAt),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) CheckKubeLesson(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID, lesson string) {
	var in gen.KubeLessonCheckInput
	if !decode(w, r, &in) {
		return
	}
	l, ok := kubelessons.Find(lesson)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "onbekende les")
		return
	}
	env, err := s.lessonEnv(r, clusterID)
	if err != nil {
		s.kubeError(w, r, err)
		return
	}
	res, err := s.lessons.Check(r.Context(), env, l, deref(in.Answers))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := gen.KubeLesson{
		Id: l.ID, Number: l.Number, Title: l.Title, Summary: l.Summary, Intro: l.Intro, Outro: l.Outro,
		Steps: make([]gen.KubeLessonStep, 0, len(res.Steps)), Done: res.Done, Unavailable: res.Unavailable,
		StartedAt: nullableTimePtr(res.StartedAt), FinishedAt: nullableTimePtr(res.FinishedAt), Problem: res.Problem,
	}
	if out.Intro == nil {
		out.Intro = []string{}
	}
	if out.Outro == nil {
		out.Outro = []string{}
	}
	for _, sr := range res.Steps {
		st := gen.KubeLessonStep{
			Id: sr.Step.ID, Title: sr.Title, Kind: gen.KubeLessonStepKind(sr.Step.Kind), State: gen.KubeLessonStepState(sr.State),
			Text: sr.Text, Web: sr.Web, Kubectl: sr.Kubectl, Yaml: sr.YAML, Question: sr.Question, Detail: sr.Detail,
			Answer: sr.Answer, Wrong: sr.Wrong, DoneLabel: sr.Step.Done, DoneAt: nullableTimePtr(sr.DoneAt),
			Link: nullable.NewNullNullable[gen.KubeLessonLink](),
		}
		if sr.Link != nil {
			ln := gen.KubeLessonLink{
				Label: sr.Link.Label, Tab: sr.Link.Tab, Namespace: sr.Link.Namespace, System: sr.Link.System, Url: sr.Link.URL,
				Ref: nullable.NewNullNullable[gen.KubeRef](),
			}
			if sr.Link.Ref != nil {
				ln.Ref = nullable.NewNullableWithValue(toAPIKubeRef(*sr.Link.Ref))
			}
			st.Link = nullable.NewNullableWithValue(ln)
		}
		out.Steps = append(out.Steps, st)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) ResetKubeLesson(w http.ResponseWriter, r *http.Request, clusterID uuid.UUID, lesson string) {
	if _, ok := kubelessons.Find(lesson); !ok {
		writeError(w, http.StatusNotFound, "not_found", "onbekende les")
		return
	}
	ctx := r.Context()
	p, _ := principalFrom(ctx)
	if _, err := s.kube.Cluster(ctx, clusterID); err != nil {
		s.kubeError(w, r, err)
		return
	}
	if err := s.q.ResetKubeLesson(ctx, store.ResetKubeLessonParams{ClusterID: clusterID, UserID: p.User.ID, Lesson: lesson}); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
