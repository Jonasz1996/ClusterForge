package protocol

import "time"

// Fase 40: het filterlog van OPNsense samengevat. De agent leest
// /var/log/filter/ stukje bij stuk (hij onthoudt bestand en positie) en geeft
// per uur en per stroom terug hoe vaak pf hem doorliet of blokkeerde. De
// regels zelf verlaten de firewall niet.

// OPNFlowsSince is de eerste protocolversie met OPNOpFilterLog.
const OPNFlowsSince = 56

// OPNOpFilterLog leest wat er sinds de vorige vraag in het filterlog kwam.
const OPNOpFilterLog = "opn_filterlog"

// Grenzen van één OPNOpFilterLog-antwoord.
const (
	// MaxFilterLogRead is hoeveel bytes log de agent per keer leest.
	MaxFilterLogRead = 32 << 20
	// MaxFilterFlows is het meeste aantal stromen per antwoord; de rest
	// telt mee in Dropped.
	MaxFilterFlows = 5000
	// FilterLogFirstRead: een log die de agent nog niet kende, leest hij
	// vanaf zoveel bytes voor het einde.
	FilterLogFirstRead = 2 << 20
)

// FilterFlow is één stroom in één uur: zelfde actie, interface, richting,
// protocol, bron, doel, doelpoort en regel.
type FilterFlow struct {
	Hour      time.Time `json:"t"`
	Action    string    `json:"a"`
	Interface string    `json:"i"`
	Dir       string    `json:"d"`
	Proto     string    `json:"p"`
	Src       string    `json:"s"`
	Dst       string    `json:"x"`
	// Port is de doelpoort; 0 zonder poort (icmp).
	Port int `json:"o,omitempty"`
	// Rule is het label van de regel (rid), zoals pf het logt.
	Rule  string `json:"r,omitempty"`
	Count int    `json:"n"`
}

// FilterLogResult is wat de agent sinds de vorige keer in het filterlog zag.
type FilterLogResult struct {
	Flows   []FilterFlow `json:"flows"`
	Lines   int          `json:"lines"`
	Skipped int          `json:"skipped"`
	// Dropped zijn de regels die niet meer in MaxFilterFlows pasten.
	Dropped int `json:"dropped"`
	// More: er staat nog meer te lezen.
	More bool `json:"more,omitempty"`
}
