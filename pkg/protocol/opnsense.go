package protocol

import (
	"fmt"
	"regexp"
	"strings"
)

// OPNsenseSince is de eerste protocolversie die op een OPNsense-firewall
// draait (fase 34): een FreeBSD-build van de agent, de facts opnsense en
// het platform in de heartbeat (Heartbeat.Arch, zie AgentPlatform).
const OPNsenseSince = 50

// AgentPlatform is de sleutel van de binary die een agent draait, zoals de
// server hem aanbiedt: de architectuur op Linux (amd64, arm64, zo bleef het
// sinds SelfUpgradeSince), en os-architectuur daarbuiten (freebsd-amd64).
func AgentPlatform(goos, goarch string) string {
	if goos == "linux" || goos == "" {
		return goarch
	}
	return goos + "-" + goarch
}

// AgentBinary is de bestandsnaam van de agent voor een platform onder
// /downloads/: cf-agent-linux-amd64 of cf-agent-freebsd-amd64.
func AgentBinary(platform string) string {
	if !strings.Contains(platform, "-") {
		return "cf-agent-linux-" + platform
	}
	return "cf-agent-" + platform
}

// AgentPlatforms zijn de platforms waarvoor de server een agent bouwt.
var AgentPlatforms = []string{"amd64", "arm64", "freebsd-amd64"}

// OPNsenseFacts staat in de facts van een agent op een OPNsense-firewall.
type OPNsenseFacts struct {
	// Version is de versie van OPNsense, zoals 26.1.2.
	Version string `json:"version"`
	// Series is de reeks, zoals 26.1.
	Series string `json:"series,omitempty"`
	// FreeBSD is de versie van het onderliggende FreeBSD.
	FreeBSD string `json:"freebsd,omitempty"`
	// Hostname is hostnaam.domein uit config.xml.
	Hostname string `json:"hostname,omitempty"`
	// CARP is de algemene CARP-toestand van deze node.
	CARP OPNsenseCARP `json:"carp"`
	// VIPs zijn de virtuele adressen (CARP en IP-alias met vhid).
	VIPs []OPNsenseVIP `json:"vips,omitempty"`
	// HA zijn de instellingen van System → High Availability.
	HA OPNsenseHA `json:"ha"`
	// Interfaces zijn de toegewezen interfaces (lan, wan, opt1, ...).
	Interfaces []OPNsenseInterface `json:"interfaces,omitempty"`
	// Config beschrijft de laatste opgeslagen configuratie.
	Config OPNsenseConfig `json:"config"`
	// API zegt of de agent de lokale API kan gebruiken.
	API OPNsenseAPIState `json:"api"`
	// GUI is protocol en poort van de webinterface, zoals https:443; de
	// server test na een wijziging of hij die nog bereikt.
	GUI string `json:"gui,omitempty"`
}

// OPNsenseCARP is de algemene CARP-toestand.
type OPNsenseCARP struct {
	// Demotion is net.inet.carp.demotion; hoger dan 0 betekent dat deze
	// node zichzelf terugzet (link down, onderhoudsmodus, pfsync niet klaar).
	Demotion int `json:"demotion"`
	// Allow is net.inet.carp.allow; false zet CARP op deze node uit.
	Allow bool `json:"allow"`
	// Maintenance is de blijvende CARP-onderhoudsmodus.
	Maintenance bool `json:"maintenance"`
}

// OPNsenseVIP is één virtueel adres met vhid.
type OPNsenseVIP struct {
	// Interface is de logische naam (lan, wan, opt3) en Device het
	// apparaat (vtnet1, vlan0.10).
	Interface string `json:"interface"`
	Device    string `json:"device,omitempty"`
	// Address is het adres met prefix, zoals 192.168.10.1/24.
	Address string `json:"address"`
	VHID    int    `json:"vhid"`
	// State is MASTER, BACKUP, INIT of DISABLED.
	State   string `json:"state"`
	AdvBase int    `json:"advbase"`
	AdvSkew int    `json:"advskew"`
	// Mode is carp of ipalias.
	Mode  string `json:"mode,omitempty"`
	Descr string `json:"descr,omitempty"`
}

// OPNsenseHA zijn de HA-instellingen; het wachtwoord komt nooit mee.
type OPNsenseHA struct {
	// PfsyncInterface is de logische interface voor pfsync; leeg is uit.
	PfsyncInterface string `json:"pfsync_interface,omitempty"`
	PfsyncPeer      string `json:"pfsync_peer,omitempty"`
	// PfsyncUp: pfsync0 bestaat, heeft een syncdev en meldt syncok.
	PfsyncUp bool `json:"pfsync_up"`
	// SyncTo is het adres van de backup voor de configsync (XMLRPC); leeg
	// op een backup.
	SyncTo   string `json:"sync_to,omitempty"`
	Username string `json:"username,omitempty"`
	// SyncItems zijn de onderdelen die gesynchroniseerd worden.
	SyncItems      []string `json:"sync_items,omitempty"`
	DisablePreempt bool     `json:"disable_preempt,omitempty"`
}

// OPNsenseInterface is een toegewezen interface.
type OPNsenseInterface struct {
	Name    string `json:"name"`
	Device  string `json:"device"`
	Descr   string `json:"descr,omitempty"`
	Enabled bool   `json:"enabled"`
	Up      bool   `json:"up"`
	// Addresses zijn de adressen op het apparaat, zonder VIP's.
	Addresses []string `json:"addresses,omitempty"`
}

// OPNsenseConfig beschrijft config.xml.
type OPNsenseConfig struct {
	// Time is het moment van de laatste opslag (unix-seconden).
	Time int64 `json:"time,omitempty"`
	// Description en User zeggen wie wat veranderde.
	Description string `json:"description,omitempty"`
	User        string `json:"user,omitempty"`
	// Backups is het aantal revisies in /conf/backup.
	Backups int `json:"backups"`
}

// OPNsenseAPIState zegt of de lokale API werkt.
type OPNsenseAPIState struct {
	OK bool `json:"ok"`
	// User is de API-gebruiker van ClusterForge.
	User string `json:"user,omitempty"`
	// Pasted: de sleutel kwam uit de GUI in plaats van vanzelf.
	Pasted bool   `json:"pasted,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Role is de rol van deze node in het CARP-cluster: master als elke
// CARP-VIP MASTER is, backup als ze allemaal BACKUP zijn, mixed als ze
// verschillen of er een in INIT staat, en leeg zonder CARP-VIP's.
func (f *OPNsenseFacts) Role() string {
	if f == nil {
		return ""
	}
	n, master, backup := 0, 0, 0
	for _, v := range f.VIPs {
		if v.Mode != "" && v.Mode != "carp" {
			continue
		}
		n++
		switch v.State {
		case "MASTER":
			master++
		case "BACKUP":
			backup++
		}
	}
	switch {
	case n == 0:
		return ""
	case master == n:
		return "master"
	case backup == n:
		return "backup"
	}
	return "mixed"
}

// Het pad van een aanroep van de lokale OPNsense-API, zonder /api ervoor:
// module/controller/command met eventueel uuid's of namen erachter.
var opnAPIPath = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}/[a-z][a-z0-9_]{0,40}/[a-zA-Z][a-zA-Z0-9_]{0,60}(/[A-Za-z0-9_.:,=@+-]{1,200}){0,4}$`)

// ValidOPNsenseAPIPath is true voor een pad als firewall/alias/searchItem.
func ValidOPNsenseAPIPath(p string) bool {
	return opnAPIPath.MatchString(p) && !strings.Contains(p, "..")
}

// OPNsense-aanvragen gaan over het onderwerp pve (PVERequest), zoals PBS.
const (
	// OPNOpGet is een GET op de lokale API: Path zoals
	// core/firmware/status.
	OPNOpGet = "opn_get"
	// OPNOpPost is een POST zonder wijziging: een zoekopdracht
	// (searchItem, searchRule) met Params als formulier.
	OPNOpPost = "opn_search"
	// OPNOpConfigctl is een configctl-actie die alleen leest, zoals
	// "interface list ifconfig". De agent laat alleen OPNReadActions toe.
	OPNOpConfigctl = "opn_configctl"
	// OPNOpRevisions geeft de revisies van config.xml.
	OPNOpRevisions = "opn_revisions"
	// OPNOpRevision geeft één revisie (Path is de bestandsnaam of
	// "current"), zonder geheimen.
	OPNOpRevision = "opn_revision"
	// OPNOpFacts geeft de facts van nu (OPNsenseFacts), zonder op de
	// volgende heartbeat te wachten; de proeven na een wijziging gebruiken
	// het.
	OPNOpFacts = "opn_facts"
)

// OPNReadActions zijn de configctl-acties die alleen lezen.
var OPNReadActions = []string{
	"interface list ifconfig",
	"interface show carp",
	"interface gateways status",
	"filter list pfsync json",
	"system ha options",
	"system ha services_cached",
	"system ha exec version",
	"filter diag info",
}

// OPNsense-acties van een run pve (PVERun.API), die iets veranderen.
const (
	// OPNAPI is een veilige wijziging via de lokale API: herstelpunt,
	// aanroepen, toepassen en een terugdraaitimer (zie OPNChange).
	OPNAPI = "opn_api"
	// OPNConfirm bevestigt een wijziging: de timer stopt.
	OPNConfirm = "opn_confirm"
	// OPNRollback zet het herstelpunt van een wijziging meteen terug.
	OPNRollback = "opn_rollback"
	// OPNHASync synchroniseert de configuratie naar de backup en laadt
	// daar de diensten opnieuw.
	OPNHASync = "opn_hasync"
	// OPNKey zet een met de hand gemaakte API-sleutel op de firewall
	// (terugval als de agent er zelf geen kan maken).
	OPNKey = "opn_key"
)

func init() {
	PVEActions = append(PVEActions, OPNAPI, OPNConfirm, OPNRollback, OPNHASync, OPNKey)
}

// OPNCall is één aanroep van de lokale API binnen een wijziging.
type OPNCall struct {
	// Path zonder /api, zoals firewall/alias/setItem/<uuid>.
	Path string `json:"path"`
	// Body is het JSON-lichaam van de POST; leeg is een POST zonder lichaam.
	Body map[string]any `json:"body,omitempty"`
}

// OPNChange is een wijziging op een OPNsense-firewall (run-actie opn_api).
type OPNChange struct {
	// ID is het nummer van de wijziging; de bevestiging en het
	// terugdraaien noemen het.
	ID string `json:"id"`
	// Calls zijn de aanroepen, in volgorde. Mislukt er een, dan zet de
	// agent meteen het herstelpunt terug.
	Calls []OPNCall `json:"calls"`
	// Apply zijn de aanroepen die de wijziging toepassen
	// (firewall/alias/reconfigure). Ze draaien ook na het terugzetten, zodat
	// de oude toestand weer actief is.
	Apply []OPNCall `json:"apply,omitempty"`
	// ConfirmWithin is hoe lang de server heeft om te bevestigen
	// (seconden, 30-900); daarna zet de agent het herstelpunt terug.
	ConfirmWithin int `json:"confirm_within"`
	// Description komt in de revisie van config.xml.
	Description string `json:"description,omitempty"`
	// Secret zijn velden (punt-paden in Body, zoals "alias.content") die
	// niet in de uitvoer mogen.
	Secret []string `json:"secret,omitempty"`
	// RequireMaster: weiger als deze node niet voor elke CARP-VIP MASTER
	// is. De server zet dit altijd behalve op een losse firewall.
	RequireMaster bool `json:"require_master,omitempty"`
}

// OPNKeyInput is een met de hand gemaakte API-sleutel (run-actie opn_key).
type OPNKeyInput struct {
	Key    string `json:"key"`
	Secret string `json:"secret"`
}

// Validate controleert de vorm van een wijziging.
func (c *OPNChange) Validate() error {
	if c == nil {
		return fmt.Errorf("geen wijziging")
	}
	if !opnChangeID.MatchString(c.ID) {
		return fmt.Errorf("ongeldig nummer van de wijziging")
	}
	if len(c.Calls) == 0 || len(c.Calls) > 50 || len(c.Apply) > 10 {
		return fmt.Errorf("een wijziging heeft 1-50 aanroepen en hoogstens 10 om toe te passen")
	}
	for _, call := range append(append([]OPNCall{}, c.Calls...), c.Apply...) {
		if !ValidOPNsenseAPIPath(call.Path) {
			return fmt.Errorf("ongeldig API-pad %q", call.Path)
		}
	}
	if c.ConfirmWithin < 30 || c.ConfirmWithin > 900 {
		return fmt.Errorf("de bevestigingstijd moet tussen 30 en 900 seconden liggen")
	}
	if len(c.Description) > 200 || strings.ContainsAny(c.Description, "\n\r") {
		return fmt.Errorf("ongeldige omschrijving")
	}
	return nil
}

var (
	opnChangeID = regexp.MustCompile(`^[a-z0-9-]{8,64}$`)
	// Sleutel en geheim van OPNsense zijn base64 (80 tekens), met de hand
	// soms in een andere lengte.
	opnKeyPart = regexp.MustCompile(`^[A-Za-z0-9+/=]{16,200}$`)
)

// ValidOPNChangeID is true voor het nummer van een wijziging.
func ValidOPNChangeID(id string) bool { return opnChangeID.MatchString(id) }
