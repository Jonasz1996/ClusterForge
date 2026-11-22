// Package journal schrijft en leest het wijzigingsjournaal: wat er op de
// nodes en in Proxmox veranderde, ook buiten ClusterForge om. De regels
// komen van de agent (protocol 5), van de server zelf (heartbeats en
// facts), uit Proxmox en van ClusterForge. Elke regel krijgt een
// Nederlandse zin.
//
// Regels uit de agent zijn invoer van de node en dus niet te vertrouwen:
// ze worden ingekort, de webinterface toont ze als tekst, en ze sturen
// nooit een actie aan.
package journal

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Bronnen.
const (
	SourceAgent        = "agent"
	SourceServer       = "server"
	SourceProxmox      = "proxmox"
	SourceClusterForge = "clusterforge"
)

// Soorten. De eerste zes komen ook van de agent (zie protocol.Change).
const (
	KindPackage = "package"
	KindBoot    = "boot"
	KindService = "service"
	KindFile    = "file"
	KindLogin   = "login"
	KindOOM     = "oom"
	KindFacts   = "facts"
	KindNetwork = "network"
	KindAgent   = "agent"
	KindVM      = "vm"
	KindUser    = "user"
	KindCommand = "command"
)

// Kinds zijn alle soorten, in de volgorde van de filters.
var Kinds = []string{
	KindPackage, KindService, KindFile, KindBoot, KindLogin, KindOOM, KindNetwork, KindFacts, KindVM, KindAgent,
	KindUser, KindCommand,
}

// AgentKinds zijn de soorten die een agent mag sturen.
var AgentKinds = map[string]bool{
	KindPackage: true, KindBoot: true, KindService: true, KindFile: true, KindLogin: true, KindOOM: true,
}

// Retention is hoe lang het journaal bewaard wordt.
const Retention = 180 * 24 * time.Hour

// Entry is één regel.
type Entry struct {
	NodeID    uuid.UUID
	ClusterID *uuid.UUID
	TS        time.Time
	Source    string
	Kind      string
	Subject   string
	Action    string
	From      string
	To        string
	By        string
	// Detail zijn vaste velden per soort, zoals ip en method bij een login.
	Detail map[string]string
	JobID  *uuid.UUID
}

// detail is wat er in de kolom detail staat: Detail plus van, naar en door.
func (e Entry) detail() map[string]string {
	d := map[string]string{}
	for k, v := range e.Detail {
		d[k] = v
	}
	if e.From != "" {
		d["from"] = e.From
	}
	if e.To != "" {
		d["to"] = e.To
	}
	if e.By != "" {
		d["by"] = e.By
	}
	return d
}

// FromRow leest een opgeslagen regel terug.
func FromRow(kind, subject, action string, detail []byte) Entry {
	e := Entry{Kind: kind, Subject: subject, Action: action, Detail: map[string]string{}}
	_ = json.Unmarshal(detail, &e.Detail)
	e.From, e.To, e.By = e.Detail["from"], e.Detail["to"], e.Detail["by"]
	delete(e.Detail, "from")
	delete(e.Detail, "to")
	delete(e.Detail, "by")
	return e
}

// Limieten voor velden uit de agent.
const (
	maxSubject = 300
	maxValue   = 200
	maxDetail  = 8
)

// clean kort een waarde in en haalt stuurtekens en een wachtwoord in een
// URL weg.
func clean(s string, n int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "?")
	}
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = stripURLPassword(s)
	if len(s) > n {
		cut := n
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

var urlCreds = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^\s/@:]+(:[^\s/@]*)?@`)

// stripURLPassword maakt van http://jonas:geheim@host weer http://host.
func stripURLPassword(s string) string {
	if !strings.Contains(s, "@") || !strings.Contains(s, "://") {
		return s
	}
	return urlCreds.ReplaceAllString(s, "$1")
}

// Sanitize maakt een regel uit de agent veilig om te bewaren.
func Sanitize(e Entry) Entry {
	e.Subject = clean(e.Subject, maxSubject)
	e.Action = clean(e.Action, 40)
	e.From, e.To, e.By = clean(e.From, maxValue), clean(e.To, maxValue), clean(e.By, 100)
	if len(e.Detail) > 0 {
		d := map[string]string{}
		for k, v := range e.Detail {
			if len(d) == maxDetail {
				break
			}
			d[clean(k, 40)] = clean(v, maxValue)
		}
		e.Detail = d
	}
	return e
}

// Record schrijft regels met hun zin. Een regel die er al is, wordt
// overgeslagen.
func Record(ctx context.Context, q *store.Queries, entries ...Entry) error {
	if len(entries) == 0 {
		return nil
	}
	params := make([]store.InsertNodeChangesParams, 0, len(entries))
	for _, e := range entries {
		d, err := json.Marshal(e.detail())
		if err != nil {
			return err
		}
		params = append(params, store.InsertNodeChangesParams{
			NodeID: e.NodeID, ClusterID: e.ClusterID, Ts: e.TS.UTC().Truncate(time.Microsecond), Source: e.Source,
			Kind: e.Kind, Subject: e.Subject, Action: e.Action, Summary: Describe(e, false), Detail: d, JobID: e.JobID,
		})
	}
	var batchErr error
	q.InsertNodeChanges(ctx, params).Exec(func(_ int, err error) {
		if err != nil && batchErr == nil {
			batchErr = err
		}
	})
	return batchErr
}

// Describe geeft de zin bij een regel. Met redact valt het IP-adres van
// een login weg, voor een viewer.
func Describe(e Entry, redact bool) string {
	switch e.Kind {
	case KindPackage:
		return describePackage(e)
	case KindBoot:
		switch {
		case e.Action == "requested":
			return "Herstart gevraagd door ClusterForge"
		case e.Action == "shutdown_requested":
			return "Afsluiten gevraagd door ClusterForge"
		case e.From != "" && e.To != "":
			return fmt.Sprintf("Herstart, kernel van %s naar %s", e.From, e.To)
		case e.To != "":
			return "Herstart, kernel " + e.To
		}
		return "Herstart"
	case KindService:
		return describeService(e)
	case KindFile:
		return describeFile(e)
	case KindLogin:
		s := "SSH-login als " + e.Subject
		if ip := e.Detail["ip"]; ip != "" && !redact {
			s += " vanaf " + ip
		}
		if m := e.Detail["method"]; m != "" {
			s += " (" + loginMethod(m) + ")"
		}
		return s
	case KindOOM:
		return e.Subject + " gestopt door geheugentekort (OOM-killer)"
	case KindVM:
		return describeVM(e)
	case KindAgent:
		switch e.Action {
		case "upgrade":
			return fmt.Sprintf("Agent bijgewerkt van %s naar %s", or(e.From, "onbekend"), e.To)
		case "gap":
			secs, _ := strconv.Atoi(e.Detail["seconds"])
			return "Geen contact met de agent gedurende " + Duration(time.Duration(secs)*time.Second)
		case "truncated":
			return "Het journaal van de agent liep over; een deel van de oudste regels ontbreekt"
		}
	case KindFacts:
		return describeFacts(e)
	case KindNetwork:
		return describeNetwork(e)
	case KindUser:
		return "Gebruiker " + e.Subject + " aangemaakt" + byClusterForge(e)
	case KindCommand:
		if e.Subject != "" {
			return "Commando uitgevoerd (maakt " + e.Subject + ")" + byClusterForge(e)
		}
		return "Commando uitgevoerd" + byClusterForge(e)
	}
	return strings.TrimSpace(e.Kind + " " + e.Subject + " " + e.Action)
}

// Duration schrijft een duur zoals mensen hem zeggen: 45 s, 5 min, 2 u 10 min,
// 3 dagen.
func Duration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + " s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " min"
	case d < 48*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return strconv.Itoa(h) + " u"
		}
		return fmt.Sprintf("%d u %d min", h, m)
	}
	return strconv.Itoa(int(d.Hours()/24)) + " dagen"
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func byClusterForge(e Entry) string {
	if e.By == "clusterforge" {
		return " door ClusterForge"
	}
	return ""
}

// who zegt wie het deed, tussen haakjes: de gebruiker, of via dpkg.
func who(e Entry) string {
	switch {
	case e.By == "clusterforge":
		return " (ClusterForge)"
	case e.By != "":
		return " (" + e.By + ")"
	case e.Detail["via"] != "":
		return " (via " + e.Detail["via"] + ")"
	}
	return ""
}

func describePackage(e Entry) string {
	p := e.Subject
	switch e.Action {
	case "upgrade":
		return fmt.Sprintf("%s bijgewerkt van %s naar %s%s", p, e.From, e.To, who(e))
	case "downgrade":
		return fmt.Sprintf("%s teruggezet van %s naar %s%s", p, e.From, e.To, who(e))
	case "install":
		return strings.TrimSpace(p+" "+e.To) + " geïnstalleerd" + who(e)
	case "reinstall":
		return strings.TrimSpace(p+" "+e.To) + " opnieuw geïnstalleerd" + who(e)
	case "remove":
		return strings.TrimSpace(p+" "+e.From) + " verwijderd" + who(e)
	case "purge":
		return strings.TrimSpace(p+" "+e.From) + " met configuratie verwijderd" + who(e)
	}
	return p + " " + e.Action + who(e)
}

// results vertaalt het Result van systemd.
var results = map[string]string{
	"exit-code":       "afgesloten met een fout",
	"signal":          "afgebroken door een signaal",
	"core-dump":       "gecrasht",
	"timeout":         "time-out",
	"watchdog":        "watchdog verlopen",
	"oom-kill":        "geheugentekort",
	"start-limit-hit": "te vaak herstart",
	"resources":       "kon niet starten",
	"protocol":        "protocolfout",
}

func describeService(e Entry) string {
	u := e.Subject
	switch e.Action {
	case "started":
		return u + " gestart" + byClusterForge(e) + paren(e.Detail["reason"])
	case "stopped":
		return u + " gestopt" + byClusterForge(e) + paren(e.Detail["reason"])
	case "restarted":
		return u + " herstart" + byClusterForge(e)
	case "reloaded":
		return u + " herladen" + byClusterForge(e)
	case "auto_restarted":
		s := u + " automatisch herstart door systemd"
		if n := e.Detail["restarts"]; n != "" && n != "1" {
			s += " (" + n + " keer sinds de laatste start)"
		}
		return s
	case "failed":
		if r := e.Detail["result"]; r != "" && r != "success" {
			return u + " mislukt: " + or(results[r], r)
		}
		return u + " mislukt"
	case "enabled":
		return u + " ingeschakeld" + byClusterForge(e)
	case "disabled":
		return u + " uitgeschakeld" + byClusterForge(e)
	case "changed":
		return fmt.Sprintf("%s van %s naar %s", u, e.From, e.To)
	}
	return u + " " + e.Action
}

func describeFile(e Entry) string {
	p := e.Subject
	switch e.Action {
	case "created":
		return p + " aangemaakt" + byClusterForge(e)
	case "modified":
		return p + " aangepast" + byClusterForge(e)
	case "written":
		return p + " geschreven door ClusterForge"
	case "deleted":
		return p + " verwijderd"
	case "mode":
		return fmt.Sprintf("Rechten van %s van %s naar %s", p, e.From, e.To)
	case "owner":
		return fmt.Sprintf("Eigenaar van %s van %s naar %s", p, e.From, e.To)
	case "changed":
		return fmt.Sprintf("%s bestanden aangepast in %s", or(e.Detail["count"], "Meerdere"), p)
	}
	return p + " " + e.Action
}

func loginMethod(m string) string {
	switch m {
	case "publickey":
		return "sleutel"
	case "password":
		return "wachtwoord"
	case "keyboard-interactive/pam":
		return "wachtwoord via PAM"
	}
	return m
}

// vmActions vertaalt de soorten Proxmox-taken.
var vmActions = map[string]string{
	"qmstart": "gestart", "vzstart": "gestart",
	"qmstop": "hard gestopt", "vzstop": "hard gestopt",
	"qmshutdown": "afgesloten", "vzshutdown": "afgesloten",
	"qmreboot": "herstart", "vzreboot": "herstart",
	"qmreset":   "gereset",
	"qmsuspend": "gepauzeerd", "vzsuspend": "gepauzeerd",
	"qmresume": "hervat", "vzresume": "hervat",
	"qmigrate": "verhuisd", "vzmigrate": "verhuisd",
	"qmsnapshot": "snapshot gemaakt", "vzsnapshot": "snapshot gemaakt",
	"qmrollback": "teruggezet naar een snapshot", "vzrollback": "teruggezet naar een snapshot",
	"qmdelsnapshot": "snapshot verwijderd", "vzdelsnapshot": "snapshot verwijderd",
	"qmclone": "gekloond", "vzclone": "gekloond",
	"qmrestore": "teruggezet uit een back-up", "vzrestore": "teruggezet uit een back-up",
	"qmconfig": "configuratie gewijzigd", "qmresize": "schijf vergroot", "vzresize": "schijf vergroot",
	"qmmove": "schijf verplaatst", "qmdiskmove": "schijf verplaatst",
	"vzdump":    "back-up",
	"vncproxy":  "console geopend",
	"termproxy": "console geopend",
	"vncshell":  "console geopend",
	"hastart":   "gestart door HA", "hastop": "gestopt door HA",
}

// VMTaskTypes zijn de Proxmox-taken die een regel bij de node van de VM
// geven.
func VMTaskType(t string) bool { _, ok := vmActions[t]; return ok }

func describeVM(e Entry) string {
	vm := "VM " + e.Subject
	what := or(vmActions[e.Action], e.Action)
	s := vm + " " + what
	if e.Action == "vzdump" {
		s = "Back-up van " + vm
	}
	if host := e.Detail["host"]; host != "" && (e.Action == "qmigrate" || e.Action == "vzmigrate") {
		s += " (van " + host + ")"
	}
	s += " in Proxmox"
	if e.By != "" {
		if e.Detail["clusterforge"] == "true" {
			s += " door ClusterForge"
		} else {
			s += " door " + e.By
		}
	}
	switch st := e.Detail["status"]; {
	case st == "" || st == "OK":
	case strings.HasPrefix(st, "WARNINGS"):
		s += ", met waarschuwingen"
	default:
		s += ", mislukt: " + st
	}
	return s
}

func describeFacts(e Entry) string {
	switch e.Subject {
	case "os":
		return fmt.Sprintf("Besturingssysteem van %s naar %s", e.From, e.To)
	case "updates":
		s := e.To + " updates open"
		if sec := e.Detail["security"]; sec != "" && sec != "0" {
			s += ", waarvan " + sec + " voor beveiliging"
		}
		return s + " (was " + e.From + ")"
	}
	return fmt.Sprintf("%s van %s naar %s", e.Subject, e.From, e.To)
}

func describeNetwork(e Entry) string {
	switch e.Action {
	case "interface_added":
		return "Interface " + e.Subject + " erbij" + paren(e.To)
	case "interface_removed":
		return "Interface " + e.Subject + " weg"
	case "addresses":
		return "Adressen van " + e.Subject + ": " + changeList(e)
	case "mtu":
		return fmt.Sprintf("MTU van %s van %s naar %s", e.Subject, e.From, e.To)
	case "master":
		return fmt.Sprintf("%s zit nu in %s (was %s)", e.Subject, or(e.To, "niets"), or(e.From, "niets"))
	case "gateway":
		return fmt.Sprintf("Standaardgateway van %s naar %s", or(e.From, "geen"), or(e.To, "geen"))
	case "dns":
		return fmt.Sprintf("DNS-servers van %s naar %s", or(e.From, "geen"), or(e.To, "geen"))
	case "routes":
		return "Routes: " + changeList(e)
	case "listeners":
		return "Luisterende poorten: " + changeList(e)
	case "keepalived":
		return "Keepalived-instantie " + e.Subject + " gewijzigd: " + changeList(e)
	}
	return e.Subject + " " + e.Action
}

func paren(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}

// changeList zegt wat erbij kwam en wat wegging, uit de velden added en
// removed van Detail.
func changeList(e Entry) string {
	var parts []string
	if a := e.Detail["added"]; a != "" {
		parts = append(parts, a+" erbij")
	}
	if r := e.Detail["removed"]; r != "" {
		parts = append(parts, r+" weg")
	}
	if len(parts) == 0 {
		return fmt.Sprintf("van %s naar %s", or(e.From, "niets"), or(e.To, "niets"))
	}
	return strings.Join(parts, ", ")
}
