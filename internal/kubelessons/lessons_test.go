package kubelessons

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/kube"
)

func TestAnswers(t *testing.T) {
	for _, tc := range []struct {
		answer string
		good   []string
		want   bool
	}{
		{"3", []string{"3"}, true},
		{" Drie. ", []string{"3"}, true},
		{"één", []string{"1"}, true},
		{"`kube-system`", []string{"kube-system"}, true},
		{"LEREN-SERVER-01", []string{"leren-server-01", "leren-server-02"}, true},
		{"leren-server", []string{"leren-server-01"}, false},
		{"4", []string{"3"}, false},
		{"", []string{"3"}, false},
	} {
		if got := matches(tc.answer, tc.good); got != tc.want {
			t.Errorf("matches(%q, %v) = %v", tc.answer, tc.good, got)
		}
	}
}

func TestClip(t *testing.T) {
	if clip("kort", 10) != "kort" || clip("ééééé", 3) != "ééé…" {
		t.Fatal(clip("ééééé", 3))
	}
}

func TestFill(t *testing.T) {
	x := &run{env: Env{VIP: "10.0.40.100"}, data: map[string]Data{
		"cordon": {"node": "leren-worker-01", detailKey: "{niet}", "pods": []any{"a", "b"}},
	}}
	x.fillVars()
	got := x.fill("{node} via {host} op {vip}, {pod} en {pods}")
	if got != "leren-worker-01 via web.10.0.40.100.nip.io op 10.0.40.100, <pod> en <pods>" {
		t.Fatalf("fill: %q", got)
	}
	x = &run{data: map[string]Data{"ingress": {"host": "web.leren.lab"}}}
	x.fillVars()
	if got := x.fill("http://{host}/"); got != "http://web.leren.lab/" {
		t.Fatalf("host uit de Ingress: %q", got)
	}
}

// placeholders zijn de plaatshouders die een les kan invullen: het VIP, het
// webadres, en wat de opdrachten vastleggen.
var placeholders = []string{"vip", "host", "pod", "service", "oud", "voorbeeld", "node"}

// Elke les is af te maken: een opdracht heeft wat zijn soort nodig heeft,
// de YAML is geldig en hoort in leren, en de commando's en plaatshouders
// kloppen.
func TestLessonContent(t *testing.T) {
	ids := map[string]bool{}
	ph := regexp.MustCompile(`\{([a-z]+)\}`)
	for i, l := range Lessons() {
		if l.Number != i+1 || ids[l.ID] || l.Title == "" || l.Summary == "" {
			t.Fatalf("les %d: %+v", i+1, l)
		}
		ids[l.ID] = true
		if l.Later != "" {
			if len(l.Steps) != 0 || l.Unavailable(10) == "" {
				t.Errorf("%s: komt later maar heeft opdrachten", l.ID)
			}
			continue
		}
		if len(l.Steps) == 0 || len(l.Intro) == 0 || len(l.Outro) == 0 || l.Unavailable(3) != "" {
			t.Errorf("%s: onvolledig", l.ID)
		}
		steps := map[string]bool{}
		for _, st := range l.Steps {
			name := l.ID + "/" + st.ID
			if steps[st.ID] || st.Title == "" || len(st.Text) == 0 || len(st.Kubectl) == 0 {
				t.Errorf("%s: onvolledig", name)
			}
			steps[st.ID] = true
			switch st.Kind {
			case KindDo:
				if st.check == nil || st.Web == "" {
					t.Errorf("%s: geen controle of uitleg voor de webinterface", name)
				}
			case KindQuiz:
				if st.answer == nil || st.Question == "" || st.Hint == "" {
					t.Errorf("%s: vraag zonder antwoord of hint", name)
				}
			case KindRead:
				if st.Done == "" {
					t.Errorf("%s: geen knop", name)
				}
			default:
				t.Errorf("%s: soort %q", name, st.Kind)
			}
			for _, c := range st.Kubectl {
				if !strings.HasPrefix(c, "sudo kubectl ") && !strings.HasPrefix(c, "curl ") {
					t.Errorf("%s: commando %q", name, c)
				}
				if strings.HasPrefix(c, "sudo kubectl ") && !strings.Contains(c, " -n leren") && !strings.Contains(c, " -n kube-system") &&
					strings.Contains(c, "deployment") {
					t.Errorf("%s: %q zonder namespace", name, c)
				}
			}
			texts := append(append([]string{st.Title, st.Web, st.YAML, st.Question}, st.Text...), st.Kubectl...)
			if st.Link != nil {
				texts = append(texts, st.Link.Label, st.Link.URL)
				if st.Link.Ref != nil {
					texts = append(texts, st.Link.Ref.Name)
				}
			}
			for _, txt := range texts {
				for _, m := range ph.FindAllStringSubmatch(txt, -1) {
					if !slices.Contains(placeholders, m[1]) {
						t.Errorf("%s: onbekende plaatshouder {%s}", name, m[1])
					}
				}
			}
			if st.YAML == "" {
				continue
			}
			x := &run{env: Env{VIP: "10.0.40.100"}, data: map[string]Data{"service": {"service": "web"}}}
			x.fillVars()
			ms, err := kube.ParseManifests(x.fill(st.YAML))
			if err != nil || len(ms) != 1 {
				t.Errorf("%s: YAML: %v", name, err)
				continue
			}
			md, _ := ms[0].Object["metadata"].(map[string]any)
			if ms[0].Object["kind"] != "Namespace" && md["namespace"] != Namespace {
				t.Errorf("%s: YAML niet in leren: %v", name, md)
			}
		}
	}
	if _, ok := Find("onderhoud"); !ok {
		t.Fatal("Find")
	}
	if l, _ := Find("onderhoud"); !strings.Contains(l.Unavailable(1), "minstens 2 nodes") {
		t.Fatalf("les 8 met één node: %q", l.Unavailable(1))
	}
}
