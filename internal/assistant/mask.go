package assistant

import "regexp"

// Masked staat op de plek van een geheim in tekst die naar het model gaat.
const Masked = "[geheim]"

// secretName is een naam die op een geheim wijst, zoals DB_PASSWORD,
// apiKey of client-secret. Achter het woord mag alleen iets na een
// scheidingsteken, zodat passed of tokenizer er niet onder vallen.
const secretName = `(?i:[a-z0-9_.-]*(?:passw(?:or)?d|passphrase|pass|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credentials?)` +
	`(?:[_.-][a-z0-9_.-]*)?)`

var masks = []struct {
	re  *regexp.Regexp
	rep string
}{
	// Een PEM-blok met een sleutel, ook over meerdere regels.
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(-----END [A-Z ]*PRIVATE KEY-----|$)`), Masked},
	// Authorization: Bearer ... of Basic ...
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "$1 " + Masked},
	// Een JWT, zoals een token van een ServiceAccount.
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]+`), Masked},
	// scheme://gebruiker:wachtwoord@host
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s/@:]+):[^\s/@]+@`), "${1}:" + Masked + "@"},
	// Een env-variabele in YAML: de naam op de ene regel, de waarde op de
	// volgende.
	{regexp.MustCompile(`(?m)(name:\s*["']?` + secretName + `["']?[ \t]*\r?\n[ \t]*value:[ \t]*)\S.*$`), "${1}" + Masked},
	// Dezelfde env-variabele als JSON, zoals in de annotatie
	// last-applied-configuration.
	{regexp.MustCompile(`("name"\s*:\s*"` + secretName + `"\s*,\s*"value"\s*:\s*)"(?:[^"\\]|\\.)*"`), `${1}"` + Masked + `"`},
	// naam=waarde, naam: waarde en "naam": "waarde", met de naam van een
	// geheim.
	{regexp.MustCompile(`(["']?\b` + secretName + `["']?[ \t]*[:=][ \t]*)("[^"\n]*"|'[^'\n]*'|[^\s,;&}]+)`), "${1}" + Masked},
}

// Mask haalt geheimen uit tekst die naar het model gaat, zoals de log van
// een pod of de YAML van een object: wachtwoorden en tokens achter een naam
// die daarop wijst, Bearer-tokens, JWT's, sleutels en het wachtwoord in een
// URL. Wat erop lijkt maar geen geheim is, kan ook wegvallen; dat is de
// prijs.
func Mask(s string) string {
	for _, m := range masks {
		s = m.re.ReplaceAllString(s, m.rep)
	}
	return s
}
