package protocol

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// AIVSince is de eerste protocolversie die AI-verslag kent (fase 38): de
// facts aiverslag, het leesverzoek AIVOpGet en de run-actie AIVCall. Net
// als bij PBS en TrueNAS geeft de agent de lokale API door (hier de
// FastAPI-dienst van AI-verslag op 127.0.0.1); welke paden de GUI
// gebruikt, beslist de server (internal/aiverslag).
//
// De agent koppelt zichzelf lokaal als app "ClusterForge" (eenmalige code
// en /api/app/pair), zodat er nergens een sleutel geplakt moet worden. De
// sleutel verlaat de machine nooit.
const AIVSince = 54

// AIVOpGet is een GET op de lokale API van AI-verslag, over het onderwerp
// pve (PVERequest met Path en Params).
const AIVOpGet = "aiv_get"

// AIVCall is een POST of DELETE op de lokale API, als run pve.
const AIVCall = "aiv_call"

func init() {
	PVEActions = append(PVEActions, AIVCall)
}

// AIVFacts staat in de facts van een agent op de machine van AI-verslag.
type AIVFacts struct {
	// Version is de versie van de startpagina van AI-verslag (kern.APPVERSIE)
	// of van de app-koppeling; leeg als de dienst niet antwoordt.
	Version string `json:"version,omitempty"`
	// Dir is waar de code staat (/opt/aiverslag).
	Dir string `json:"dir"`
	// URL is het lokale adres van de webdienst (http://127.0.0.1:8088).
	URL string `json:"url"`
	// Reachable: de webdienst antwoordde op /api/app/ping.
	Reachable bool `json:"reachable"`
	// Paired: de agent heeft een app-sleutel van AI-verslag.
	Paired bool `json:"paired"`
	// Units zijn de systemd-units van AI-verslag en Ollama.
	Units []AIVUnit `json:"units,omitempty"`
	// Model is het AI-model uit /etc/aiverslag.env (MODEL); geen geheimen.
	Model string `json:"model,omitempty"`
	// DataUsedPct is het gebruik van het bestandssysteem met
	// /var/lib/aiverslag, in procent.
	DataUsedPct float64 `json:"data_used_pct,omitempty"`
}

// AIVUnit is één systemd-unit.
type AIVUnit struct {
	Name   string `json:"name"`
	Active string `json:"active"`
	Sub    string `json:"sub,omitempty"`
	// Result is het resultaat van de laatste run (success, exit-code …).
	Result string `json:"result,omitempty"`
	// Since is wanneer de unit in deze toestand kwam (unix-seconden).
	Since int64 `json:"since,omitempty"`
}

// AIVCallSpec is één schrijvende aanroep op de lokale API.
type AIVCallSpec struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

var aivPath = regexp.MustCompile(`^/api/[A-Za-z0-9_./-]{1,200}$`)

// ValidAIVPath zegt of p een pad van de API van AI-verslag is: begint met
// /api/, geen query, geen .. en geen dubbele schuine streep.
func ValidAIVPath(p string) bool {
	return aivPath.MatchString(p) && !strings.Contains(p, "..") && !strings.Contains(p, "//")
}

var aivParam = regexp.MustCompile(`^[a-z_]{1,32}$`)

// Validate controleert de vorm van een aanroep.
func (c *AIVCallSpec) Validate() error {
	if c == nil {
		return fmt.Errorf("aiv_call zonder aanroep")
	}
	if c.Method != "POST" && c.Method != "DELETE" {
		return fmt.Errorf("methode is POST of DELETE")
	}
	if !ValidAIVPath(c.Path) {
		return fmt.Errorf("ongeldig pad %q", c.Path)
	}
	if len(c.Body) > 64<<10 {
		return fmt.Errorf("inhoud te groot")
	}
	if len(c.Body) > 0 && !json.Valid(c.Body) {
		return fmt.Errorf("inhoud is geen JSON")
	}
	return nil
}

// ValidateAIVGet controleert een leesverzoek: Path is het pad, Params de
// query.
func (r PVERequest) ValidateAIVGet() error {
	if !ValidAIVPath(r.Path) {
		return fmt.Errorf("ongeldig pad %q", r.Path)
	}
	if len(r.Params) > 8 {
		return fmt.Errorf("te veel parameters")
	}
	for k, v := range r.Params {
		if !aivParam.MatchString(k) || len(v) > 200 {
			return fmt.Errorf("ongeldige parameter %q", k)
		}
	}
	return nil
}
