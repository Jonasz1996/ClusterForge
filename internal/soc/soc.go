// Package soc is het beveiligingsoverzicht van ClusterForge (fase 25): één
// dreigingsniveau met de reden, een beveiligingsscore die uitlegt waarom hij
// niet 100 is, de aanvallers over CrowdSec, de firewall en de login van
// ClusterForge heen, en per node welke bescherming er staat.
//
// Alles komt uit gegevens die ClusterForge al heeft; er is geen nieuw
// agentprotocol. Het niveau en de score volgen vaste regels in evaluate.go,
// zodat elke kleur een zin heeft die zegt waarom.
package soc

import (
	"time"

	"github.com/google/uuid"
)

// Window is de periode van de cijfers, de activiteit en de aanvallers.
type Window string

const (
	Window24h Window = "24h"
	Window7d  Window = "7d"
)

// Duration is de lengte van het venster.
func (w Window) Duration() time.Duration {
	if w == Window7d {
		return 7 * 24 * time.Hour
	}
	return 24 * time.Hour
}

// bucket is de breedte van een staaf in de activiteit.
func (w Window) bucket() time.Duration {
	if w == Window7d {
		return 4 * time.Hour
	}
	return time.Hour
}

// ParseWindow leest 24h of 7d; al de rest wordt 24h.
func ParseWindow(s string) Window {
	if s == string(Window7d) {
		return Window7d
	}
	return Window24h
}

// Levels van laag naar hoog.
const (
	LevelNormal   = "normaal"
	LevelWatch    = "waakzaam"
	LevelElevated = "verhoogd"
	LevelCritical = "kritiek"
)

var levelRank = map[string]int{LevelNormal: 0, LevelWatch: 1, LevelElevated: 2, LevelCritical: 3}

// Tones van een punt.
const (
	ToneRed   = "red"
	ToneAmber = "amber"
	ToneInfo  = "info"
)

// Level is het dreigingsniveau met hoogstens twee redenen.
type Level struct {
	Level   string   `json:"level"`
	Reasons []string `json:"reasons"`
}

// Deduction is een aftrekpost van de score.
type Deduction struct {
	Text   string  `json:"text"`
	Points float64 `json:"points"`
	Href   string  `json:"href"`
}

// ScorePart is een deel van de score, zoals Dekking.
type ScorePart struct {
	Key        string      `json:"key"`
	Label      string      `json:"label"`
	Max        int         `json:"max"`
	Points     float64     `json:"points"`
	Deductions []Deduction `json:"deductions"`
}

// TrendPoint is de score op een uur.
type TrendPoint struct {
	At    time.Time `json:"at"`
	Score int       `json:"score"`
}

// Score is de beveiligingsscore 0–100.
type Score struct {
	Score int         `json:"score"`
	Parts []ScorePart `json:"parts"`
	// Previous is de score een venster geleden, als er een momentopname is.
	Previous *int         `json:"previous"`
	Trend    []TrendPoint `json:"trend"`
}

// Stats zijn de cijfers van de tegels, telkens met de vorige periode.
type Stats struct {
	Attacks           int   `json:"attacks"`
	AttacksPrev       int   `json:"attacks_prev"`
	ActiveBans        int   `json:"active_bans"`
	Blocked           int64 `json:"blocked"`
	BlockedPrev       int64 `json:"blocked_prev"`
	LoginFailures     int   `json:"login_failures"`
	LoginFailuresPrev int   `json:"login_failures_prev"`
	// SecurityUpdates is het aantal pakketten, SecurityNodes op hoeveel nodes.
	SecurityUpdates int `json:"security_updates"`
	SecurityNodes   int `json:"security_nodes"`
	// Nodes zijn de actieve nodes met een agent; Protected die zonder gaten.
	Nodes     int `json:"nodes"`
	Protected int `json:"protected"`
}

// Item is een punt in "Nu belangrijk".
type Item struct {
	Key    string  `json:"key"`
	Tone   string  `json:"tone"`
	Prio   float64 `json:"prio"`
	Title  string  `json:"title"`
	Detail string  `json:"detail"`
	Href   string  `json:"href"`
	// Action is de tekst van de knop, zoals "Bouncer nakijken".
	Action string `json:"action"`
}

// Bucket is één staaf in de activiteit.
type Bucket struct {
	At       time.Time `json:"at"`
	CrowdSec int       `json:"crowdsec"`
	Firewall int64     `json:"firewall"`
	Logins   int       `json:"logins"`
}

// Sources van een aanvaller.
const (
	SourceCrowdSec = "crowdsec"
	SourceFirewall = "firewall"
	SourceLogin    = "login"
)

// Attacker is een bronadres, samengevoegd over de bronnen.
type Attacker struct {
	IP string `json:"ip"`
	// Kind is extern (niet in de eigen netwerken van de firewallinstellingen),
	// intern, of node: een eigen node, meestal een vergeten firewallregel.
	Kind string `json:"kind"`
	// Node is de hostnaam als het adres van een eigen node is.
	Node      string     `json:"node"`
	Country   string     `json:"country"`
	AS        string     `json:"as"`
	Sources   []string   `json:"sources"`
	Alerts    int        `json:"alerts"`
	Blocked   int64      `json:"blocked"`
	Logins    int        `json:"logins"`
	Nodes     []string   `json:"nodes"`
	Scenarios []string   `json:"scenarios"`
	Ports     []string   `json:"ports"`
	FirstSeen time.Time  `json:"first_seen"`
	LastSeen  time.Time  `json:"last_seen"`
	Banned    bool       `json:"banned"`
	BanUntil  *time.Time `json:"ban_until"`
}

// Kinds van een aanvaller.
const (
	KindExternal = "extern"
	KindInternal = "intern"
	KindNode     = "node"
)

// Count is een naam met een aantal.
type Count struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Count  int64  `json:"count"`
	Detail string `json:"detail"`
}

// Targets is waar de aanvallen op mikken.
type Targets struct {
	Nodes     []Count `json:"nodes"`
	Ports     []Count `json:"ports"`
	Scenarios []Count `json:"scenarios"`
}

// Protection states in de dekking.
const (
	StateOn          = "on"          // beheerd en actief
	StatePreview     = "preview"     // firewall: alleen voorbeeld
	StateOff         = "off"         // bewust uit in ClusterForge
	StateMissing     = "missing"     // nooit aangezet: een gat
	StateDown        = "down"        // aangezet, maar draait niet
	StateError       = "error"       // toepassen mislukt
	StateUnsupported = "unsupported" // agent te oud
	StateUnknown     = "unknown"     // nog geen status
	// StateUnmanaged: de dienst draait, maar ClusterForge beheert hem niet.
	StateUnmanaged = "unmanaged"
)

// CoverageRow is de bescherming van één actieve node met een agent.
type CoverageRow struct {
	NodeID          uuid.UUID  `json:"node_id"`
	Hostname        string     `json:"hostname"`
	ClusterID       *uuid.UUID `json:"cluster_id"`
	ClusterName     string     `json:"cluster_name"`
	Online          bool       `json:"online"`
	Firewall        string     `json:"firewall"`
	CrowdSec        string     `json:"crowdsec"`
	Bouncer         string     `json:"bouncer"`
	SecurityUpdates int        `json:"security_updates"`
	// Zabbix (fase 33): on (bewaakt en bereikbaar), down (Zabbix ziet de node
	// niet) of missing (geen Zabbix-host); leeg zonder Zabbix-API. Telt niet
	// mee als gat of in de score.
	Zabbix string `json:"zabbix"`
	// Gaps zijn de korte omschrijvingen van wat ontbreekt.
	Gaps []string `json:"gaps"`
}

// Overview is alles voor de pagina SOC.
type Overview struct {
	GeneratedAt time.Time     `json:"generated_at"`
	Window      Window        `json:"window"`
	Level       Level         `json:"level"`
	Score       Score         `json:"score"`
	Stats       Stats         `json:"stats"`
	Now         []Item        `json:"now"`
	Activity    []Bucket      `json:"activity"`
	Attackers   []Attacker    `json:"attackers"`
	Targets     Targets       `json:"targets"`
	Coverage    []CoverageRow `json:"coverage"`
}
