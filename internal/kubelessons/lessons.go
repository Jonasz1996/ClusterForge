// Package kubelessons zijn de lessen Kubernetes: per les een paar
// opdrachten die ClusterForge op het echte cluster controleert, alleen
// lezend. De voortgang staat per gebruiker en cluster in
// kube_lesson_steps. De opdrachten van een les zijn op volgorde: de eerste
// die niet af is, is aan de beurt, en de rest wacht.
package kubelessons

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/kube"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Namespace is waar de lessen in werken; Opruimen haalt hem weg.
const Namespace = "leren"

// Reader leest het cluster; kube.Service doet dat via de kube-brug.
type Reader interface {
	Nodes(ctx context.Context, clusterID uuid.UUID) ([]kube.Node, error)
	Namespaces(ctx context.Context, clusterID uuid.UUID) ([]string, error)
	Pods(ctx context.Context, clusterID uuid.UUID, ns, node string) ([]kube.Pod, error)
	Workloads(ctx context.Context, clusterID uuid.UUID, ns string) ([]kube.Workload, error)
	Network(ctx context.Context, clusterID uuid.UUID, ns string) ([]kube.KService, []kube.Ingress, error)
	Storage(ctx context.Context, clusterID uuid.UUID, ns string) (kube.Storage, error)
	Config(ctx context.Context, clusterID uuid.UUID, ns string) ([]kube.ConfigMap, []kube.Secret, error)
	Get(ctx context.Context, clusterID uuid.UUID, path string, query url.Values) ([]byte, error)
	Logs(ctx context.Context, clusterID uuid.UUID, ns, pod string, o kube.LogOptions) (string, bool, error)
}

// Progress bewaart de voortgang; store.Queries doet dat in PostgreSQL.
type Progress interface {
	ListKubeLessonSteps(ctx context.Context, arg store.ListKubeLessonStepsParams) ([]store.KubeLessonStep, error)
	StartKubeLessonStep(ctx context.Context, arg store.StartKubeLessonStepParams) (store.KubeLessonStep, error)
	FinishKubeLessonStep(ctx context.Context, arg store.FinishKubeLessonStepParams) error
}

// Soorten opdrachten.
const (
	// KindDo is iets doen op het cluster; ClusterForge ziet zelf of het
	// gelukt is.
	KindDo = "do"
	// KindQuiz is een vraag over wat er op het cluster staat.
	KindQuiz = "quiz"
	// KindRead is lezen of kijken; je zegt zelf dat het gelukt is.
	KindRead = "read"
)

// Toestanden van een opdracht.
const (
	StateDone    = "done"
	StateCurrent = "current"
	StateLocked  = "locked"
)

// Link wijst naar het tabblad of object waar je de opdracht doet.
type Link struct {
	Label string
	// Tab is een tabblad van het cluster, zoals apps.
	Tab       string
	Namespace string
	// System toont ook kube-system.
	System bool
	// Ref is een object; dan opent de link zijn pagina.
	Ref *kube.Ref
	// URL is een webadres, zoals dat van je app.
	URL string
}

// Data is wat ClusterForge bij een opdracht onthoudt, zoals de pods die er
// waren toen hij aan de beurt kwam.
type Data map[string]any

// Str geeft een tekst uit de data.
func (d Data) Str(key string) string {
	s, _ := d[key].(string)
	return s
}

// Strs geeft een lijst teksten uit de data.
func (d Data) Strs(key string) []string {
	switch v := d[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// detailKey bewaart de uitleg bij een afgeronde opdracht, zodat die er
// later nog staat.
const detailKey = "uitleg"

// Outcome is wat een controle zag.
type Outcome struct {
	// Done: de opdracht is gelukt.
	Done bool
	// Detail zegt wat er nog ontbreekt, of bij Done wat ClusterForge zag.
	Detail string
	// Data komt bij de opdracht, bijvoorbeeld welke node op cordon ging.
	Data Data
}

// Step is één opdracht.
type Step struct {
	ID    string
	Title string
	Kind  string
	// Text is de uitleg, in alinea's. `code` staat tussen backticks.
	// {vip}, {host} en de tekstwaarden uit de data van de les worden
	// ingevuld.
	Text []string
	// Web zegt hoe het in ClusterForge kan.
	Web string
	// Kubectl zijn de commando's op een server.
	Kubectl []string
	// YAML kan op het tabblad Toepassen of met kubectl apply.
	YAML string
	// Question en Hint bij een vraag; de hint komt bij een fout antwoord.
	Question string
	Hint     string
	// Done is de knop bij lezen, zoals Gelukt.
	Done string
	Link *Link

	// start legt vast wat er is als de opdracht aan de beurt komt.
	start func(x *run) (Data, error)
	// check kijkt of een opdracht gelukt is.
	check func(x *run, d Data) (Outcome, error)
	// answer geeft de goede antwoorden op een vraag, met uitleg. Zonder
	// antwoorden slaat de les de vraag over, met skip als reden.
	answer func(x *run) (answers []string, explain, skip string, err error)
}

// Lesson is één les.
type Lesson struct {
	ID      string
	Number  int
	Title   string
	Summary string
	Intro   []string
	// Outro staat er als de les af is.
	Outro []string
	Steps []*Step
	// MinNodes is hoeveel nodes de les nodig heeft.
	MinNodes int
	// MinServers is hoeveel servers de les nodig heeft.
	MinServers int
	// Lab: de les kan alleen op een cluster met omgeving lab, omdat ze iets
	// stukmaakt.
	Lab bool
}

// Lessons zijn alle lessen, op volgorde.
func Lessons() []*Lesson { return lessons }

// Find zoekt een les op id.
func Find(id string) (*Lesson, bool) {
	for _, l := range lessons {
		if l.ID == id {
			return l, true
		}
	}
	return nil, false
}

// Unavailable zegt waarom een les nu niet kan, of niets als ze kan.
func (l *Lesson) Unavailable(env Env) string {
	switch {
	case l.Lab && env.Environment != "lab":
		return fmt.Sprintf("Deze les zet een server uit en kan daarom alleen op een cluster met omgeving lab; dit cluster is %s.", env.Environment)
	case env.Servers < l.MinServers:
		return fmt.Sprintf("Deze les heeft minstens %d servers nodig; dit cluster heeft er %d. Zet de servers op %d op het tabblad Nodes.", l.MinServers, env.Servers, l.MinServers)
	case env.Nodes < l.MinNodes:
		return fmt.Sprintf("Deze les heeft minstens %d nodes nodig; dit cluster heeft er %d.", l.MinNodes, env.Nodes)
	}
	return ""
}

// Env is het cluster en de gebruiker.
type Env struct {
	ClusterID uuid.UUID
	UserID    uuid.UUID
	// VIP is het adres van de API en de apps.
	VIP string
	// Nodes is het aantal Kubernetes-nodes uit de laatste sync, en Servers
	// hoeveel daarvan de rol control-plane hebben.
	Nodes   int
	Servers int
	// Environment is de omgeving van het cluster: lab, test of prod.
	Environment string
}

// Engine controleert de lessen.
type Engine struct {
	kube Reader
	db   Progress
	Now  func() time.Time
}

func New(kube Reader, db Progress) *Engine {
	return &Engine{kube: kube, db: db, Now: time.Now}
}

// Summary is de voortgang van een les.
type Summary struct {
	Lesson      *Lesson
	Done        int
	StartedAt   *time.Time
	FinishedAt  *time.Time
	Unavailable string
}

// List geeft de voortgang van elke les, alleen uit de database.
func (e *Engine) List(ctx context.Context, env Env) ([]Summary, error) {
	rows, err := e.db.ListKubeLessonSteps(ctx, store.ListKubeLessonStepsParams{ClusterID: env.ClusterID, UserID: env.UserID})
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(lessons))
	for _, l := range lessons {
		s := Summary{Lesson: l, Unavailable: l.Unavailable(env)}
		var last time.Time
		for _, r := range rows {
			if r.Lesson != l.ID || !l.has(r.Step) {
				continue
			}
			if s.StartedAt == nil || r.StartedAt.Before(*s.StartedAt) {
				s.StartedAt = &r.StartedAt
			}
			if r.DoneAt != nil {
				s.Done++
				if r.DoneAt.After(last) {
					last = *r.DoneAt
				}
			}
		}
		if s.Done == len(l.Steps) && s.Done > 0 {
			s.FinishedAt = &last
		}
		out = append(out, s)
	}
	return out, nil
}

func (l *Lesson) has(step string) bool {
	for _, s := range l.Steps {
		if s.ID == step {
			return true
		}
	}
	return false
}

// StepResult is een opdracht zoals de gebruiker hem ziet, met de
// plaatshouders ingevuld.
type StepResult struct {
	Step     *Step
	State    string
	Title    string
	Text     []string
	Web      string
	Kubectl  []string
	YAML     string
	Question string
	// Detail is wat de controle zag.
	Detail string
	// Answer is het goede antwoord dat gegeven is.
	Answer string
	// Wrong: het antwoord van daarnet klopt niet.
	Wrong  bool
	DoneAt *time.Time
	Link   *Link
}

// Result is een les met de toestand van elke opdracht.
type Result struct {
	Summary
	Steps []StepResult
	// Problem: het cluster was niet te lezen; de opdracht die aan de beurt
	// is, blijft dat.
	Problem string
}

// Check bekijkt een les: de opdrachten die af zijn blijven af, en de
// eerste die niet af is, controleert ClusterForge op het cluster. Lukt
// die, dan is de volgende meteen aan de beurt. answers zijn antwoorden op
// vragen en bevestigingen van leesopdrachten, per opdracht.
func (e *Engine) Check(ctx context.Context, env Env, l *Lesson, answers map[string]string) (Result, error) {
	rows, err := e.db.ListKubeLessonSteps(ctx, store.ListKubeLessonStepsParams{ClusterID: env.ClusterID, UserID: env.UserID})
	if err != nil {
		return Result{}, err
	}
	have := map[string]store.KubeLessonStep{}
	for _, r := range rows {
		if r.Lesson == l.ID {
			have[r.Step] = r
		}
	}
	x := &run{ctx: ctx, kube: e.kube, env: env, data: map[string]Data{}}
	res := Result{Summary: Summary{Lesson: l, Unavailable: l.Unavailable(env)}}
	for _, r := range have {
		var d Data
		_ = json.Unmarshal(r.Data, &d)
		x.data[r.Step] = d
		if res.StartedAt == nil || r.StartedAt.Before(*res.StartedAt) {
			res.StartedAt = &r.StartedAt
		}
	}
	x.fillVars()
	blocked := res.Unavailable != ""
	var last time.Time
	for _, st := range l.Steps {
		sr := StepResult{Step: st, State: StateLocked, Link: st.Link}
		row, ok := have[st.ID]
		switch {
		case ok && row.DoneAt != nil:
			sr.State, sr.DoneAt, sr.Answer = StateDone, row.DoneAt, row.Answer
			sr.Detail = x.data[st.ID].Str(detailKey)
			res.Done++
			if row.DoneAt.After(last) {
				last = *row.DoneAt
			}
		case blocked:
		default:
			sr.State = StateCurrent
			blocked = true
			done, err := e.current(x, env, l, st, ok, answers, &sr)
			if err != nil {
				if isClusterError(err) {
					res.Problem = err.Error()
					break
				}
				return Result{}, err
			}
			if done {
				blocked = false
				res.Done++
				last = *sr.DoneAt
			}
		}
		res.Steps = append(res.Steps, sr)
	}
	if res.StartedAt == nil && x.started {
		now := e.Now()
		res.StartedAt = &now
	}
	if res.Done == len(l.Steps) && res.Done > 0 {
		res.FinishedAt = &last
	}
	// De tekst pas invullen als alles gezien is: een opdracht kan iets
	// vastleggen, zoals de node die op cordon ging, dat een latere noemt.
	x.fillVars()
	for i := range res.Steps {
		x.render(&res.Steps[i])
	}
	return res, nil
}

// current controleert de opdracht die aan de beurt is, en bewaart hem als
// hij af is.
func (e *Engine) current(x *run, env Env, l *Lesson, st *Step, started bool, answers map[string]string, sr *StepResult) (bool, error) {
	key := func(d Data) store.StartKubeLessonStepParams {
		b, _ := json.Marshal(d)
		return store.StartKubeLessonStepParams{ClusterID: env.ClusterID, UserID: env.UserID, Lesson: l.ID, Step: st.ID, Data: b}
	}
	if !started {
		d := Data{}
		if st.start != nil {
			var err error
			if d, err = st.start(x); err != nil {
				return false, err
			}
		}
		row, err := e.db.StartKubeLessonStep(x.ctx, key(d))
		if err != nil {
			return false, err
		}
		x.started = true
		// Bestond de rij al (twee tabbladen tegelijk), dan geldt wat daar
		// staat.
		_ = json.Unmarshal(row.Data, &d)
		x.data[st.ID] = d
		x.fillVars()
	}
	d := x.data[st.ID]
	if d == nil {
		d = Data{}
	}
	var out Outcome
	answer := strings.TrimSpace(answers[st.ID])
	switch st.Kind {
	case KindDo:
		var err error
		if out, err = st.check(x, d); err != nil {
			return false, err
		}
	case KindRead:
		out.Done = answer != ""
		answer = ""
	case KindQuiz:
		good, explain, skip, err := st.answer(x)
		if err != nil {
			return false, err
		}
		switch {
		case len(good) == 0:
			out = Outcome{Done: true, Detail: skip}
		case answer == "":
		case matches(answer, good):
			out = Outcome{Done: true, Detail: explain}
		default:
			sr.Wrong = true
			out.Detail = fmt.Sprintf("%s klopt niet. %s", answer, st.Hint)
		}
	}
	sr.Detail = out.Detail
	if !out.Done {
		return false, nil
	}
	for k, v := range out.Data {
		d[k] = v
	}
	if out.Detail != "" {
		d[detailKey] = out.Detail
	}
	b, _ := json.Marshal(d)
	err := e.db.FinishKubeLessonStep(x.ctx, store.FinishKubeLessonStepParams{
		ClusterID: env.ClusterID, UserID: env.UserID, Lesson: l.ID, Step: st.ID, Data: b, Answer: answer,
	})
	if err != nil {
		return false, err
	}
	now := e.Now()
	x.data[st.ID] = d
	x.fillVars()
	sr.State, sr.DoneAt, sr.Answer = StateDone, &now, answer
	return true, nil
}

// isClusterError is true voor een fout van het cluster of de agent, niet
// van ClusterForge zelf.
func isClusterError(err error) bool {
	var ue *kube.UnavailableError
	var ae *kube.APIError
	return errors.As(err, &ue) || errors.As(err, &ae) || errors.Is(err, kube.ErrNotFound) || errors.Is(err, kube.ErrInvalid)
}

// numbers zijn de getallen die je ook voluit mag schrijven.
var numbers = map[string]string{
	"nul": "0", "een": "1", "één": "1", "twee": "2", "drie": "3", "vier": "4", "vijf": "5",
	"zes": "6", "zeven": "7", "acht": "8", "negen": "9", "tien": "10",
}

// normalize maakt een antwoord vergelijkbaar: kleine letters, zonder
// aanhalingstekens of punt, en een getal in cijfers.
func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Trim(s, "`'\".!? ")
	if n, ok := numbers[s]; ok {
		return n
	}
	return s
}

// matches is true als het antwoord een van de goede is.
func matches(answer string, good []string) bool {
	a := normalize(answer)
	for _, g := range good {
		if a == normalize(g) {
			return true
		}
	}
	return false
}

var placeholder = regexp.MustCompile(`\{([a-z]+)\}`)

// fill vult de plaatshouders in; een onbekende wordt <naam>, zoals in een
// voorbeeldcommando.
func (x *run) fill(s string) string {
	return placeholder.ReplaceAllStringFunc(s, func(m string) string {
		k := m[1 : len(m)-1]
		if v, ok := x.vars[k]; ok && v != "" {
			return v
		}
		return "<" + k + ">"
	})
}

func (x *run) render(sr *StepResult) {
	st := sr.Step
	sr.Title = x.fill(st.Title)
	sr.Text = make([]string, len(st.Text))
	for i, t := range st.Text {
		sr.Text[i] = x.fill(t)
	}
	sr.Web = x.fill(st.Web)
	sr.Kubectl = make([]string, len(st.Kubectl))
	for i, c := range st.Kubectl {
		sr.Kubectl[i] = x.fill(c)
	}
	sr.YAML = x.fill(st.YAML)
	sr.Question = x.fill(st.Question)
	if st.Link != nil {
		l := *st.Link
		l.Label, l.URL = x.fill(l.Label), x.fill(l.URL)
		if l.Ref != nil {
			r := *l.Ref
			r.Name = x.fill(r.Name)
			l.Ref = &r
		}
		sr.Link = &l
	}
}
