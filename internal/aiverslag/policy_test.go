package aiverslag

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestCheckRead(t *testing.T) {
	ok := []struct {
		path   string
		params map[string]string
	}{
		{"/api/state", nil},
		{"/api/inzicht/start", nil},
		{"/api/inzicht/opzoeken", map[string]string{"q": "1.2.3.4"}},
		{"/api/fix/netwerk/12", nil},
		{"/api/chat/a1b2c3d4e5f6", nil},
	}
	for _, c := range ok {
		if err := checkRead(c.path, c.params); err != nil {
			t.Errorf("%s: %v", c.path, err)
		}
	}
	bad := []struct {
		path   string
		params map[string]string
	}{
		{"/api/app/tokens", nil},    // sleutels lezen nooit
		{"/api/schrijflog", nil},    // niet in de lijst
		{"/api/../etc/passwd", nil}, // geen ..
		{"/api/state", map[string]string{"x": "1"}},
		{"/api/fix/Netwerk/12", nil},
		{"/api/fix/netwerk/12", map[string]string{"q": "1"}},
		{"/static/index.html", nil},
	}
	for _, c := range bad {
		if err := checkRead(c.path, c.params); err == nil {
			t.Errorf("%s aanvaard", c.path)
		}
	}
}

func TestCheckCall(t *testing.T) {
	r, spec, err := checkCall("/api/run", nil)
	if err != nil || r.Direct || spec.Method != "POST" || spec.Body != nil {
		t.Fatalf("run: %+v %+v %v", r, spec, err)
	}
	if _, _, err := checkCall("/api/run", json.RawMessage(`{"x":1}`)); err == nil {
		t.Fatal("run met inhoud aanvaard")
	}
	r, _, err = checkCall("/api/fix/netwerk/3/run", nil)
	if err != nil || r.Confirm != "fix" || !r.Firewall {
		t.Fatalf("fix: %+v %v", r, err)
	}
	if _, _, err := checkCall("/api/clearall", nil); err == nil {
		t.Fatal("alles wissen aanvaard")
	}
	if _, _, err := checkCall("/api/app/tokens", nil); err == nil {
		t.Fatal("koppelcode maken aanvaard")
	}
	r, spec, err = checkCall("/api/chat", json.RawMessage(`{"question":"  Waarom staat het niveau op verhoogd?  ","history":[{"role":"u","text":"hoi"},{"role":"x","text":"weg"}],"extra":1}`))
	if err != nil || !r.Direct {
		t.Fatalf("chat: %v", err)
	}
	if string(spec.Body) != `{"history":[{"role":"u","text":"hoi"}],"question":"Waarom staat het niveau op verhoogd?"}` {
		t.Fatalf("chat-inhoud %s", spec.Body)
	}
	if _, _, err := checkCall("/api/chat", json.RawMessage(`{"question":"`+strings.Repeat("a", 801)+`"}`)); err == nil {
		t.Fatal("te lange vraag aanvaard")
	}
	if _, spec, err := checkCall("/api/app/actie", json.RawMessage(`{"gezien":7,"token":"x"}`)); err != nil || string(spec.Body) != `{"gezien":7}` {
		t.Fatalf("gezien: %s %v", spec.Body, err)
	}
	if _, _, err := checkCall("/api/app/actie", json.RawMessage(`{"token":"x"}`)); err == nil {
		t.Fatal("actie met code aanvaard")
	}
}

const stateJSON = `{"running":false,"last":"17/11 13:00","sam":"Rustig.","busy":[],"cats":[
 {"id":"netwerk","label":"Netwerk","model":"qwen3.5:9b","err":"","items":[
  {"id":1,"sev":"laag","titel":"a","uitleg":"","actie_nodig":false,"actie":"","fix":false,"status":"nieuw","ev":1,"tot":1,"count":1},
  {"id":2,"sev":"hoog","titel":"Poort 22 open","uitleg":"x","actie_nodig":true,"actie":"sluit","fix":true,"status":"nieuw","ev":3,"count":2}]},
 {"id":"dns","label":"DNS","err":"timeout","items":[
  {"id":5,"sev":"midden","titel":"Vreemd domein","uitleg":"y","actie_nodig":false,"status":"nieuw","count":1}]}]}`

func TestSummarize(t *testing.T) {
	st, err := Summarize(json.RawMessage(stateJSON), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Cats) != 2 || st.Cats[0].Hoog != 1 || st.Cats[0].Laag != 1 || st.Cats[0].Actie != 1 || st.Cats[1].Midden != 1 {
		t.Fatalf("cats %+v", st.Cats)
	}
	if len(st.Top) != 2 || st.Top[0].ID != 2 || st.Top[0].Cat != "netwerk" || st.Top[1].Cat != "dns" {
		t.Fatalf("top %+v", st.Top)
	}
	if st.Last != "17/11 13:00" {
		t.Fatalf("last %q", st.Last)
	}
}

func TestWarnings(t *testing.T) {
	v := Instance{Online: true, Protocol: protocol.AIVSince, Facts: protocol.AIVFacts{Reachable: true, DataUsedPct: 92},
		Units: []protocol.AIVUnit{{Name: "aiverslag-web.service", Active: "active"}, {Name: "aiverslag-run.service", Active: "failed", Result: "exit-code"}, {Name: "ollama.service", Active: "inactive"}}}
	start := json.RawMessage(`{"niveau":{"niveau":"verhoogd","redenen":["incident: scan van 1.2.3.4"]},"feeds":{"fout":2}}`)
	app := json.RawMessage(`{"meldingen":[{"id":3,"titel":"C2-contact","gezien":false,"ts":9999999999},{"id":2,"titel":"oud","gezien":true,"ts":9999999999}]}`)
	st, _ := Summarize(json.RawMessage(stateJSON), 5)
	w := Warnings(v, start, app, st)
	var txt []string
	for _, x := range w {
		txt = append(txt, x.Level+": "+x.Text)
	}
	all := strings.Join(txt, "\n")
	for _, want := range []string{"critical: aiverslag-run.service is mislukt (exit-code)", "warning: Ollama draait niet", "warning: schijf van AI-verslag 92% vol",
		"warning: dreigingsniveau verhoogd: incident: scan van 1.2.3.4", "warning: 2 dreigingsfeed(s) werken niet", "warning: alarm niet gezien: C2-contact", "warning: analyse dns: timeout"} {
		if !strings.Contains(all, want) {
			t.Errorf("mist %q in\n%s", want, all)
		}
	}
	var sum Summary
	fillLevel(&sum, Overview{Start: json.RawMessage(`{"niveau":{"niveau":"kritiek","redenen":["x"]},"score":{"score":71}}`), State: st})
	if sum.Niveau != "kritiek" || sum.Score == nil || *sum.Score != 71 || sum.OpenHoog != 1 {
		t.Fatalf("sum %+v", sum)
	}
}
