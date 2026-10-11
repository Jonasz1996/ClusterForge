package docs

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxSummary is de langste samenvatting in tekens; de rest valt weg.
const MaxSummary = 3000

var (
	mdImage    = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	mdLink     = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	mdAutolink = regexp.MustCompile(`<((?:https?|mailto):[^>\s]*)>`)
	mdListItem = regexp.MustCompile(`^\s*(?:[-*+]|\d{1,2}[.)])\s+`)
	mdNumbered = regexp.MustCompile(`^\s*(\d{1,2})[.)]\s+`)
	mdHeading  = regexp.MustCompile(`^\s{0,3}#{1,6}\s*`)
	mdRule     = regexp.MustCompile(`^\s*([-*_])(\s*[-*_]){2,}\s*$`)
)

// SummaryText maakt de samenvatting van de assistent veilig voor de
// documentatie, die ook naar GitHub kan gaan. Alinea's, lijsten, vet en
// inline code blijven. Een link wordt zijn tekst, een afbeelding verdwijnt,
// een kopje wordt een gewone regel, en tabellen, codeblokken en HTML worden
// gewone tekst. Zo kan tekst van buiten die via de documentatie in de
// samenvatting kwam, geen afbeelding laden of naar buiten linken.
func SummaryText(raw string) string {
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	raw = mdImage.ReplaceAllString(raw, "")
	raw = mdLink.ReplaceAllString(raw, "$1")
	raw = mdAutolink.ReplaceAllString(raw, "$1")
	var lines []string
	blank := true
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, " \t")
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") || mdRule.MatchString(t) {
			if !blank {
				lines = append(lines, "")
				blank = true
			}
			continue
		}
		// Een tabelregel wordt tekst; de scheidingsregel valt weg.
		if strings.HasPrefix(t, "|") {
			if strings.Trim(t, "|-: ") == "" {
				continue
			}
			t = strings.Join(strings.Fields(strings.ReplaceAll(strings.Trim(t, "|"), "|", " · ")), " ")
		}
		prefix := ""
		switch {
		case mdNumbered.MatchString(t):
			prefix = mdNumbered.FindStringSubmatch(t)[1] + ". "
			t = mdNumbered.ReplaceAllString(t, "")
		case mdListItem.MatchString(t):
			prefix = "- "
			t = mdListItem.ReplaceAllString(t, "")
		case mdHeading.MatchString(t):
			t = mdHeading.ReplaceAllString(t, "")
			t = strings.TrimSpace(strings.TrimRight(t, "#"))
			if t != "" {
				t = "**" + strings.Trim(t, "*") + "**"
			}
		case strings.HasPrefix(t, ">"):
			t = strings.TrimSpace(strings.TrimLeft(t, ">"))
		}
		if t = escapeSummary(t); t == "" {
			continue
		}
		lines = append(lines, prefix+t)
		blank = false
	}
	out := strings.TrimSpace(strings.Join(lines, "\n"))
	if utf8.RuneCountInString(out) > MaxSummary {
		r := []rune(out)
		cut := string(r[:MaxSummary])
		if i := strings.LastIndex(cut, "\n"); i > MaxSummary/2 {
			cut = cut[:i]
		}
		out = strings.TrimSpace(cut) + " …"
	}
	return out
}

// escapeSummary ontsnapt de tekens die een link, afbeelding, HTML of tabel
// kunnen maken. Vet (*) en inline code blijven; binnen code verandert
// niets.
func escapeSummary(s string) string {
	const special = "\\[]<>|!~#_"
	var b strings.Builder
	inCode := false
	for _, r := range s {
		if r == '`' {
			inCode = !inCode
		} else if !inCode && strings.ContainsRune(special, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	out := b.String()
	if inCode {
		// Een backtick die nergens sluit, is gewone tekst.
		out = strings.Replace(out, "`", "\\`", 1)
	}
	return strings.TrimSpace(out)
}

// WithoutSummary haalt het stuk 'In het kort' uit een hoofdstuk, zodat de
// assistent bij een nieuwe samenvatting zijn vorige niet leest.
func WithoutSummary(content string) string {
	start := strings.Index(content, "\n## In het kort\n")
	if start < 0 {
		return content
	}
	rest := content[start+1:]
	end := strings.Index(rest[3:], "\n## ")
	if end < 0 {
		return content[:start+1]
	}
	return content[:start+1] + rest[3+end+1:]
}

// modelText maakt de id van een model, zoals de API hem gaf, leesbaar: de
// familie met een hoofdletter en de versie met een punt. Na een fallback
// staan er meer, gescheiden door een komma.
func modelText(models string) string {
	var out []string
	for _, m := range strings.Split(models, ",") {
		m = strings.TrimSpace(m)
		parts := strings.Split(m, "-")
		if len(parts) < 3 || parts[0] != "claude" {
			out = append(out, m)
			continue
		}
		var version []string
		for _, p := range parts[2:] {
			if len(p) <= 2 && strings.Trim(p, "0123456789") == "" {
				version = append(version, p)
			}
		}
		name := strings.ToUpper(parts[1][:1]) + parts[1][1:]
		out = append(out, strings.TrimSpace("Claude "+name+" "+strings.Join(version, ".")))
	}
	return strings.Join(out, ", ")
}
