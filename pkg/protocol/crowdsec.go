package protocol

import (
	"fmt"
	"net/netip"
	"regexp"
	"time"
)

// CrowdSecSince is de eerste protocolversie die CrowdSec leest en bans zet
// (fase 14). Installeren en configureren gaat via een run (RunSince).
const CrowdSecSince = 28

// CrowdSecEnrollSince is de eerste protocolversie die een node bij de
// CrowdSec Console aanmeldt (op enroll).
const CrowdSecEnrollSince = 31

// CrowdSecListsSince is de eerste protocolversie die centrale blocklists van
// ClusterForge importeert en weghaalt (list_import, list_drop), de Console
// loskoppelt (console_off) en bij poll de lijsten per tag meldt (fase 47).
const CrowdSecListsSince = 57

// SubjectCrowdSec draagt de CrowdSec-verzoeken: cf.node.<id>.crowdsec.
const SubjectCrowdSec = "crowdsec"

const (
	TypeCrowdSecRequest = "crowdsec_request"
	TypeCrowdSecResult  = "crowdsec_result"
)

// Soorten CrowdSec-verzoek.
const (
	// CrowdSecStatus geeft de stand van CrowdSec, de bouncers, de bronnen,
	// de Docker-containers en welke globs in Probes iets vinden. Werkt ook
	// zonder CrowdSec (Installed false).
	CrowdSecOpStatus = "status"
	// CrowdSecPoll geeft de alerts sinds Since en de actieve lokale bans.
	CrowdSecOpPoll = "poll"
	// CrowdSecDecide zet of verwijdert een ban.
	CrowdSecOpDecide = "decide"
	// CrowdSecOpEnroll meldt de node aan bij de CrowdSec Console
	// (cscli console enroll). De sleutel gaat alleen in dit verzoek mee en
	// komt niet in een script, log of bestand van ClusterForge.
	CrowdSecOpEnroll = "enroll"
	// CrowdSecOpListImport zet één stuk van een centrale lijst op de node
	// (cscli decisions import, reden = de tag).
	CrowdSecOpListImport = "list_import"
	// CrowdSecOpListDrop haalt de bans van de tags in Drop weg.
	CrowdSecOpListDrop = "list_drop"
	// CrowdSecOpConsoleOff zet het delen met de CrowdSec Console uit
	// (cscli console disable --all) en herstart CrowdSec. De community-
	// blocklist (CAPI) blijft.
	CrowdSecOpConsoleOff = "console_off"
)

// MaxCrowdSecListChunk is het grootste aantal waarden per list_import.
const MaxCrowdSecListChunk = 5000

// CrowdSecListPrefix begint de reden (in CrowdSec: scenario) van elke ban
// uit een centrale lijst: cf-list:<lijst>:<generatie>.
const CrowdSecListPrefix = "cf-list:"

var crowdSecListTag = regexp.MustCompile(`^cf-list:[a-z0-9][a-z0-9-]{0,39}:[0-9]{1,15}$`)

// ValidListTag zegt of een tag van een centrale lijst geldig is.
func ValidListTag(t string) bool { return crowdSecListTag.MatchString(t) }

// CrowdSecListChunk is een stuk van een centrale lijst.
type CrowdSecListChunk struct {
	Tag      string   `json:"tag"`
	Duration string   `json:"duration"`
	Values   []string `json:"values"`
}

// Validate controleert een stuk; de waarden gaan naar een bestand voor
// cscli, de tag en de duur als argumenten.
func (c CrowdSecListChunk) Validate() error {
	if !ValidListTag(c.Tag) {
		return fmt.Errorf("ongeldige lijst-tag %q", c.Tag)
	}
	if !crowdSecDuration.MatchString(c.Duration) {
		return fmt.Errorf("ongeldige duur %q", c.Duration)
	}
	if len(c.Values) == 0 || len(c.Values) > MaxCrowdSecListChunk {
		return fmt.Errorf("een stuk heeft 1 tot %d waarden", MaxCrowdSecListChunk)
	}
	for _, v := range c.Values {
		if _, _, err := ListValue(v); err != nil {
			return err
		}
	}
	return nil
}

// ListValue leest een IP of range; scope is ip of range.
func ListValue(v string) (scope, norm string, err error) {
	if a, err := netip.ParseAddr(v); err == nil && a.Zone() == "" {
		return "ip", a.String(), nil
	}
	if p, err := netip.ParsePrefix(v); err == nil {
		p = p.Masked()
		if p.IsSingleIP() {
			return "ip", p.Addr().String(), nil
		}
		return "range", p.String(), nil
	}
	return "", "", fmt.Errorf("ongeldig IP-adres of range %q", v)
}

// CrowdSecRequestTimeout is hoe lang een CrowdSec-verzoek mag duren.
const CrowdSecRequestTimeout = 60 * time.Second

// CrowdSecStateFile is waar het script van ClusterForge onthoudt wat het
// toepaste: de hash en de collections die het zelf installeerde.
const CrowdSecStateFile = "/var/lib/clusterforge/crowdsec.json"

// MaxCrowdSecProbes is het grootste aantal globs per status.
const MaxCrowdSecProbes = 64

// MaxCrowdSecAlerts is het grootste aantal alerts per poll.
const MaxCrowdSecAlerts = 1000

type CrowdSecRequest struct {
	ID       string    `json:"id"`
	Op       string    `json:"op"`
	Deadline time.Time `json:"deadline"`
	// Probes horen bij status: globs waarvan de server wil weten of ze
	// bestaan, zoals /var/www/*/data/nextcloud.log.
	Probes []string `json:"probes,omitempty"`
	// Since hoort bij poll; nul is het afgelopen uur.
	Since  time.Time          `json:"since,omitzero"`
	Decide *CrowdSecDecision  `json:"decide,omitempty"`
	Enroll *CrowdSecEnroll    `json:"enroll,omitempty"`
	List   *CrowdSecListChunk `json:"list,omitempty"`
	// Drop hoort bij list_drop: de tags die weg moeten.
	Drop []string `json:"drop,omitempty"`
}

// CrowdSecEnroll meldt een node aan bij de CrowdSec Console.
type CrowdSecEnroll struct {
	Key string `json:"key"`
	// Name is de naam in de Console; meestal de hostnaam.
	Name string `json:"name"`
	// Overwrite meldt opnieuw aan, ook als de node al aangemeld was.
	Overwrite bool `json:"overwrite,omitempty"`
}

var (
	crowdSecEnrollKey  = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
	crowdSecEnrollName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
)

// Validate controleert een aanmelding; de waarden gaan als argumenten naar
// cscli.
func (e CrowdSecEnroll) Validate() error {
	if !crowdSecEnrollKey.MatchString(e.Key) {
		return fmt.Errorf("ongeldige enroll-sleutel (letters, cijfers, - en _)")
	}
	if !crowdSecEnrollName.MatchString(e.Name) {
		return fmt.Errorf("ongeldige naam %q", e.Name)
	}
	return nil
}

// ValidEnrollKey zegt of een enroll-sleutel er geldig uitziet.
func ValidEnrollKey(k string) bool { return crowdSecEnrollKey.MatchString(k) }

// CrowdSecDecision is een ban die de server zet (Delete false) of weghaalt
// (Delete true: alle bans op Value).
type CrowdSecDecision struct {
	Delete bool `json:"delete,omitempty"`
	// Scope is ip of range.
	Scope string `json:"scope"`
	Value string `json:"value"`
	// Type is ban of captcha; leeg is ban.
	Type     string `json:"type,omitempty"`
	Duration string `json:"duration,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

var (
	crowdSecDuration = regexp.MustCompile(`^[0-9]{1,6}(s|m|h)$`)
	crowdSecReason   = regexp.MustCompile(`^[\pL\pN @:._/+-]{1,120}$`)
)

// Validate controleert een ban; server en agent gebruiken dezelfde regels,
// want de waarden gaan als argumenten naar cscli.
func (d CrowdSecDecision) Validate() error {
	switch d.Scope {
	case "ip":
		a, err := netip.ParseAddr(d.Value)
		if err != nil || a.Zone() != "" {
			return fmt.Errorf("ongeldig IP-adres %q", d.Value)
		}
	case "range":
		if _, err := netip.ParsePrefix(d.Value); err != nil {
			return fmt.Errorf("ongeldige range %q", d.Value)
		}
	default:
		return fmt.Errorf("onbekende scope %q (ip of range)", d.Scope)
	}
	if d.Delete {
		return nil
	}
	switch d.Type {
	case "", "ban", "captcha":
	default:
		return fmt.Errorf("onbekend soort ban %q", d.Type)
	}
	if !crowdSecDuration.MatchString(d.Duration) {
		return fmt.Errorf("ongeldige duur %q (zoals 4h, 30m)", d.Duration)
	}
	if d.Reason != "" && !crowdSecReason.MatchString(d.Reason) {
		return fmt.Errorf("de reden mag letters, cijfers, spaties en @:._/+- bevatten (max 120)")
	}
	return nil
}

type CrowdSecResponse struct {
	Error  string          `json:"error,omitempty"`
	Status *CrowdSecStatus `json:"status,omitempty"`
	Poll   *CrowdSecPoll   `json:"poll,omitempty"`
	// Count is bij decide het aantal gezette of verwijderde bans.
	Count int `json:"count,omitempty"`
}

// CrowdSecStatus is de stand van CrowdSec op een node.
type CrowdSecStatus struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	// Active is systemctl is-active crowdsec.
	Active string `json:"active,omitempty"`
	// Bouncer is systemctl is-active crowdsec-firewall-bouncer, of leeg
	// als de bouncer er niet is.
	Bouncer string `json:"bouncer,omitempty"`
	// CAPI is true als de node bij de Central API aangemeld is (community-
	// blocklist).
	CAPI        bool                  `json:"capi"`
	Collections []CrowdSecItem        `json:"collections,omitempty"`
	Bouncers    []CrowdSecBouncer     `json:"bouncers,omitempty"`
	Sources     []CrowdSecSource      `json:"sources,omitempty"`
	Containers  []CrowdSecContainer   `json:"containers,omitempty"`
	Probes      map[string][]string   `json:"probes,omitempty"`
	Applied     *CrowdSecAppliedState `json:"applied,omitempty"`
	// Errors zijn de cscli-aanroepen die faalden; de rest is er wel.
	Errors []string `json:"errors,omitempty"`
}

// CrowdSecItem is een item van de hub, zoals een collection.
type CrowdSecItem struct {
	Name    string `json:"name"`
	Status  string `json:"status,omitempty"`
	Version string `json:"version,omitempty"`
}

type CrowdSecBouncer struct {
	Name     string    `json:"name"`
	IP       string    `json:"ip,omitempty"`
	Type     string    `json:"type,omitempty"`
	Version  string    `json:"version,omitempty"`
	Valid    bool      `json:"valid"`
	LastPull time.Time `json:"last_pull,omitzero"`
}

// CrowdSecSource is een bron van de acquisitie met de cijfers sinds de
// start van CrowdSec.
type CrowdSecSource struct {
	Source   string `json:"source"`
	Read     int64  `json:"read"`
	Parsed   int64  `json:"parsed"`
	Unparsed int64  `json:"unparsed"`
	Poured   int64  `json:"poured"`
}

// CrowdSecContainer is een draaiende Docker-container; zo herkent de server
// apps die ClusterForge niet zelf uitrolde.
type CrowdSecContainer struct {
	Name   string            `json:"name"`
	Image  string            `json:"image"`
	Mounts map[string]string `json:"mounts,omitempty"` // doel in de container → bron op de host
}

// CrowdSecAppliedState is wat het script in CrowdSecStateFile schrijft.
type CrowdSecAppliedState struct {
	Hash        string    `json:"hash"`
	Collections []string  `json:"collections"`
	AppliedAt   time.Time `json:"applied_at"`
}

// CrowdSecPoll zijn de alerts en bans van een node.
type CrowdSecPoll struct {
	Alerts []CrowdSecAlert `json:"alerts"`
	// Decisions zijn de actieve bans zonder de community-blocklist.
	Decisions []CrowdSecActive `json:"decisions"`
	// Blocklist is het aantal bans uit de community-blocklist en lijsten,
	// of -1 als het niet te zien is.
	Blocklist int `json:"blocklist"`
	// Lists is het aantal actieve bans per tag van een centrale lijst
	// (protocol 57); nil als het niet te zien is.
	Lists map[string]int `json:"lists,omitempty"`
}

type CrowdSecAlert struct {
	ID        int64     `json:"id"`
	Scenario  string    `json:"scenario"`
	Message   string    `json:"message,omitempty"`
	Scope     string    `json:"scope"`
	Value     string    `json:"value"`
	Country   string    `json:"country,omitempty"`
	AS        string    `json:"as,omitempty"`
	Events    int       `json:"events"`
	StartedAt time.Time `json:"started_at"`
	StoppedAt time.Time `json:"stopped_at"`
	// Decisions is het aantal bans dat dit alert zette.
	Decisions int  `json:"decisions"`
	Simulated bool `json:"simulated,omitempty"`
}

type CrowdSecActive struct {
	ID       int64     `json:"id"`
	Origin   string    `json:"origin"`
	Type     string    `json:"type"`
	Scope    string    `json:"scope"`
	Value    string    `json:"value"`
	Scenario string    `json:"scenario"`
	Until    time.Time `json:"until"`
	Country  string    `json:"country,omitempty"`
	AS       string    `json:"as,omitempty"`
}
