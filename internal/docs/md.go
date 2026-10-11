package docs

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// doc bouwt één Markdown-document. Tekst van buiten (namen, beschrijvingen,
// commitberichten) gaat door esc, zodat ze geen link, afbeelding of HTML
// kan worden, op GitHub noch in de webinterface.
type doc struct {
	b strings.Builder
}

func (d *doc) String() string { return strings.TrimRight(d.b.String(), "\n") + "\n" }

// block schrijft een blok met een lege regel erna.
func (d *doc) block(s string) {
	d.b.WriteString(strings.TrimRight(s, "\n"))
	d.b.WriteString("\n\n")
}

func (d *doc) h1(s string) { d.block("# " + s) }
func (d *doc) h2(s string) { d.block("## " + s) }
func (d *doc) h3(s string) { d.block("### " + s) }
func (d *doc) h4(s string) { d.block("#### " + s) }

// p schrijft een alinea; lege delen vallen weg.
func (d *doc) p(parts ...string) {
	var out []string
	for _, s := range parts {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if len(out) > 0 {
		d.block(strings.Join(out, " "))
	}
}

func (d *doc) bullets(items []string) {
	if len(items) == 0 {
		return
	}
	var b strings.Builder
	for _, it := range items {
		b.WriteString("- " + oneLine(it) + "\n")
	}
	d.block(b.String())
}

func (d *doc) steps(items []string) {
	if len(items) == 0 {
		return
	}
	var b strings.Builder
	for i, it := range items {
		b.WriteString(strconv.Itoa(i+1) + ". " + oneLine(it) + "\n")
	}
	d.block(b.String())
}

// table schrijft een tabel. De cellen zijn al Markdown; cell maakt ze
// veilig voor een tabel.
func (d *doc) table(head []string, rows [][]string) {
	var b strings.Builder
	line := func(cells []string, fill bool) {
		b.WriteString("|")
		for i := range head {
			v := ""
			if i < len(cells) {
				v = cell(cells[i])
			}
			if v == "" && fill {
				v = "–"
			}
			b.WriteString(" " + v + " |")
		}
		b.WriteString("\n")
	}
	line(head, false)
	b.WriteString("|")
	for range head {
		b.WriteString(" --- |")
	}
	b.WriteString("\n")
	for _, r := range rows {
		line(r, true)
	}
	d.block(b.String())
}

// facts schrijft een tabel met twee kolommen zonder kop.
func (d *doc) facts(rows [][2]string) {
	var keep [][]string
	for _, r := range rows {
		if r[1] != "" {
			keep = append(keep, []string{"**" + r[0] + "**", r[1]})
		}
	}
	if len(keep) == 0 {
		return
	}
	d.table([]string{"", ""}, keep)
}

// code schrijft een codeblok met een omheining die langer is dan elke rij
// backticks in de tekst.
func (d *doc) code(lang, text string) {
	fence := strings.Repeat("`", max(3, longestRun(text, '`')+1))
	d.block(fence + lang + "\n" + strings.TrimRight(text, "\n") + "\n" + fence)
}

func longestRun(s string, c byte) int {
	best, cur := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			cur++
			best = max(best, cur)
		} else {
			cur = 0
		}
	}
	return best
}

// mdSpecial zijn de tekens die in Markdown iets betekenen. Een backslash
// ervoor maakt ze gewone tekst, op GitHub en in de webinterface.
const mdSpecial = "\\`*_[]<>|#!~"

// esc maakt tekst van buiten veilig als gewone tekst op één regel.
func esc(s string) string {
	s = oneLine(s)
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(mdSpecial, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// oneLine vervangt regeleinden door een spatie.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(s)), " ")
}

// cell maakt een cel veilig voor een tabel: geen regeleinden en een | die
// nog niet ontsnapt is, krijgt een backslash.
func cell(s string) string {
	s = oneLine(s)
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '|' && (i == 0 || s[i-1] != '\\') {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// code geeft tekst als inline code. Backticks erin krijgen een langere
// omheining.
func code(s string) string {
	s = oneLine(s)
	if s == "" {
		return ""
	}
	n := longestRun(s, '`')
	if n == 0 {
		return "`" + s + "`"
	}
	fence := strings.Repeat("`", n+1)
	return fence + " " + s + " " + fence
}

// link geeft een relatieve link naar een ander document.
func link(text, path string) string { return "[" + esc(text) + "](" + path + ")" }

// --- Tekst ---

var months = []string{"januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"}

// dateText schrijft een datum als 3 november 2026.
func (o Opts) dateText(t time.Time) string {
	t = t.In(o.loc())
	return strconv.Itoa(t.Day()) + " " + months[t.Month()-1] + " " + strconv.Itoa(t.Year())
}

// timeText schrijft een datum met tijd als 3 november 2026 om 14:05.
func (o Opts) timeText(t time.Time) string {
	l := t.In(o.loc())
	return o.dateText(t) + " om " + fmt.Sprintf("%02d:%02d", l.Hour(), l.Minute())
}

// duration schrijft een duur zoals mensen hem zeggen: 2,1 s, 45 s,
// 2 min 40 s, 1 u 5 min.
func duration(d time.Duration) string {
	switch {
	case d < 10*time.Second:
		return decimal(d.Seconds(), 1) + " s"
	case d < time.Minute:
		return strconv.Itoa(int(d.Round(time.Second)/time.Second)) + " s"
	case d < time.Hour:
		d = d.Round(time.Second)
		m, s := int(d/time.Minute), int(d%time.Minute/time.Second)
		if s == 0 {
			return strconv.Itoa(m) + " min"
		}
		return strconv.Itoa(m) + " min " + strconv.Itoa(s) + " s"
	}
	d = d.Round(time.Minute)
	h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
	if m == 0 {
		return strconv.Itoa(h) + " u"
	}
	return strconv.Itoa(h) + " u " + strconv.Itoa(m) + " min"
}

// decimal schrijft een getal met een komma.
func decimal(f float64, places int) string {
	return strings.Replace(strconv.FormatFloat(f, 'f', places, 64), ".", ",", 1)
}

// size schrijft bytes als 1,2 GB of 512 MB.
func size(b int64) string {
	switch {
	case b >= 1<<40:
		return decimal(float64(b)/(1<<40), 1) + " TB"
	case b >= 1<<30:
		return decimal(float64(b)/(1<<30), 1) + " GB"
	case b >= 1<<20:
		return strconv.FormatInt(b>>20, 10) + " MB"
	}
	return strconv.FormatInt(b>>10, 10) + " kB"
}

// mib schrijft een geheugengrootte in MiB als 2 GB of 512 MB.
func mib(n int) string {
	if n >= 1024 && n%1024 == 0 {
		return strconv.Itoa(n/1024) + " GB"
	}
	if n >= 1024 {
		return decimal(float64(n)/1024, 1) + " GB"
	}
	return strconv.Itoa(n) + " MB"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// list schrijft a, b en c.
func list(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " en " + items[len(items)-1]
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

var statusText = map[string]string{
	"healthy": "gezond", "degraded": "verminderd", "down": "down", "split_brain": "split-brain", "unknown": "onbekend",
}

func statusOf(s string) string { return or(statusText[s], or(s, "onbekend")) }

var lifecycleText = map[string]string{
	"provisioning": "in aanmaak", "active": "actief", "maintenance": "onderhoud", "draining": "draining", "decommissioned": "uit dienst",
}

var envText = map[string]string{"prod": "prod", "test": "test", "lab": "lab"}

func envOf(s string) string { return or(envText[s], s) }

// --- Mermaid ---

// flow bouwt een Mermaid-flowchart. Id's zijn n1, n2 enzovoort in de
// volgorde waarin knopen erbij komen, dus dezelfde invoer geeft hetzelfde
// diagram. Labels gaan tussen aanhalingstekens met de tekens die Mermaid
// zou lezen als entiteit, zodat een naam het diagram niet kan breken.
type flow struct {
	dir    string
	ids    map[string]string
	lines  []string
	groups []*flowGroup
	edges  []string
	n      int
}

type flowGroup struct {
	id, label string
	lines     []string
}

func newFlow(dir string) *flow { return &flow{dir: dir, ids: map[string]string{}} }

// label maakt tekst veilig voor een Mermaid-label; regels worden <br/>.
func label(lines ...string) string {
	var out []string
	for _, l := range lines {
		if l = oneLine(l); l == "" {
			continue
		}
		r := strings.NewReplacer("&", "#amp;", `"`, "#quot;", "<", "#lt;", ">", "#gt;", "#", "#35;", "`", "#96;")
		out = append(out, r.Replace(l))
	}
	return `"` + strings.Join(out, "<br/>") + `"`
}

// shapes zijn de vormen die het diagram gebruikt.
var shapes = map[string][2]string{
	"box": {"[", "]"}, "round": {"(", ")"}, "stadium": {"([", "])"}, "db": {"[(", ")]"},
}

// node voegt een knoop toe, in group als die er is, en geeft zijn id.
// Dezelfde key geeft dezelfde knoop.
func (f *flow) node(key string, g *flowGroup, shape string, lines ...string) string {
	if id, ok := f.ids[key]; ok {
		return id
	}
	f.n++
	id := "n" + strconv.Itoa(f.n)
	f.ids[key] = id
	sh := shapes[shape]
	if sh[0] == "" {
		sh = shapes["box"]
	}
	line := id + sh[0] + label(lines...) + sh[1]
	if g != nil {
		g.lines = append(g.lines, line)
	} else {
		f.lines = append(f.lines, line)
	}
	return id
}

func (f *flow) has(key string) bool { _, ok := f.ids[key]; return ok }

// group maakt een vak met een titel.
func (f *flow) group(lines ...string) *flowGroup {
	g := &flowGroup{id: "g" + strconv.Itoa(len(f.groups)+1), label: label(lines...)}
	f.groups = append(f.groups, g)
	return g
}

// edge tekent een lijn. arrow is --> (pijl), -.-> (gestippelde pijl), ---
// (lijn) of -.- (gestippelde lijn).
func (f *flow) edge(from, to, arrow, text string) {
	if text != "" {
		f.edges = append(f.edges, from+" "+arrow+"|"+label(text)+"| "+to)
		return
	}
	f.edges = append(f.edges, from+" "+arrow+" "+to)
}

func (f *flow) empty() bool { return f.n == 0 }

func (f *flow) String() string {
	var b strings.Builder
	b.WriteString("flowchart " + f.dir + "\n")
	for _, l := range f.lines {
		b.WriteString("  " + l + "\n")
	}
	for _, g := range f.groups {
		if len(g.lines) == 0 {
			continue
		}
		b.WriteString("  subgraph " + g.id + "[" + g.label + "]\n")
		for _, l := range g.lines {
			b.WriteString("    " + l + "\n")
		}
		b.WriteString("  end\n")
	}
	for _, e := range f.edges {
		b.WriteString("  " + e + "\n")
	}
	return b.String()
}
