package pbs

// Welke aanroepen van de PBS-API ClusterForge doet (fase 32). De agent
// voert elke aanroep uit die er goed uitziet; hier staat wat de GUI mag:
// methode, pad, velden, wat geheim is en wat een overgetikte bevestiging
// vraagt.

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	segStore = `(?P<store>[A-Za-z0-9][A-Za-z0-9_.-]{1,31})`
	segID    = `(?P<id>[A-Za-z0-9][A-Za-z0-9_.:-]{0,63})`
	segUser  = `(?P<user>[A-Za-z0-9][A-Za-z0-9_.-]{0,63}@[A-Za-z0-9][A-Za-z0-9_.-]{0,31})`
	segToken = `(?P<token>[A-Za-z0-9][A-Za-z0-9_.-]{0,63})`
	segUPID  = `(?P<upid>UPID:[A-Za-z0-9:@._!\\-]{10,300})`
	segIface = `(?P<iface>[A-Za-z0-9][A-Za-z0-9_.-]{0,20})`
	segSvc   = `(?P<svc>proxmox-backup|proxmox-backup-proxy|sshd|syslog|cron|postfix|systemd-journald|systemd-timesyncd|chrony)`
	segCmd   = `(?P<cmd>start|stop|restart|reload)`
	anyKey   = `[a-z][a-z0-9_-]{0,63}`
	keepKeys = `keep-last|keep-hourly|keep-daily|keep-weekly|keep-monthly|keep-yearly`
	jobKeys  = anyKey
)

type rule struct {
	method  string
	path    *regexp.Regexp
	keys    *regexp.Regexp
	title   func(m, p map[string]string) string
	confirm func(m, p map[string]string) string
	secret  []string
	quiet   bool
}

func r(method, path, keys string, title func(m, p map[string]string) string) *rule {
	return &rule{
		method: method,
		path:   regexp.MustCompile("^" + path + "$"),
		keys:   regexp.MustCompile("^(" + keys + ")$"),
		title:  title,
	}
}

func (x *rule) confirmWith(f func(m, p map[string]string) string) *rule {
	x.confirm = f
	return x
}

func (x *rule) secrets(k ...string) *rule {
	x.secret = k
	return x
}

func (x *rule) hidden() *rule {
	x.quiet = true
	return x
}

func part(name string) func(m, p map[string]string) string {
	return func(m, _ map[string]string) string { return m[name] }
}

func param(name string) func(m, p map[string]string) string {
	return func(_, p map[string]string) string { return p[name] }
}

func fixed(s string) func(m, p map[string]string) string {
	return func(map[string]string, map[string]string) string { return s }
}

// snap is de naam van een snapshot of groep zoals PBS hem toont.
func snap(m, p map[string]string) string {
	s := p["backup-type"] + "/" + p["backup-id"]
	if t := p["backup-time"]; t != "" {
		s += " (" + t + ")"
	}
	if ns := p["ns"]; ns != "" {
		s = ns + ": " + s
	}
	return s
}

const snapKeys = `ns|backup-type|backup-id|backup-time`

var svcWord = map[string]string{"start": "starten", "stop": "stoppen", "restart": "herstarten", "reload": "herladen"}

var rules = []*rule{
	// --- Datastores ---------------------------------------------------
	r("create", `/config/datastore`, anyKey, func(_, p map[string]string) string { return "datastore " + p["name"] + " aanmaken" }),
	r("set", `/config/datastore/`+segStore, anyKey, func(m, _ map[string]string) string { return "datastore " + m["store"] + " wijzigen" }),
	r("delete", `/config/datastore/`+segStore, `keep-job-configs|destroy-data|digest`,
		func(m, p map[string]string) string {
			if p["destroy-data"] == "1" || p["destroy-data"] == "true" {
				return "datastore " + m["store"] + " verwijderen met alle back-ups"
			}
			return "datastore " + m["store"] + " verwijderen (data blijft)"
		}).confirmWith(part("store")),
	r("create", `/admin/datastore/`+segStore+`/gc`, ``, func(m, _ map[string]string) string { return "garbage collection op " + m["store"] }),
	r("create", `/admin/datastore/`+segStore+`/verify`, snapKeys+`|ignore-verified|outdated-after|max-depth`,
		func(m, p map[string]string) string {
			if p["backup-id"] != "" {
				return "verifiëren " + snap(m, p) + " in " + m["store"]
			}
			return "datastore " + m["store"] + " verifiëren"
		}),
	r("create", `/admin/datastore/`+segStore+`/prune-datastore`, keepKeys+`|ns|max-depth|dry-run`,
		func(m, p map[string]string) string {
			if p["dry-run"] == "1" || p["dry-run"] == "true" {
				return "prune op proef in " + m["store"]
			}
			return "prune in " + m["store"]
		}),
	r("create", `/admin/datastore/`+segStore+`/prune`, keepKeys+`|ns|backup-type|backup-id|dry-run`,
		func(m, p map[string]string) string { return "prune " + snap(m, p) + " in " + m["store"] }),
	r("delete", `/admin/datastore/`+segStore+`/snapshots`, snapKeys,
		func(m, p map[string]string) string {
			return "snapshot " + snap(m, p) + " verwijderen uit " + m["store"]
		}),
	r("delete", `/admin/datastore/`+segStore+`/groups`, `ns|backup-type|backup-id|error-on-protected`,
		func(m, p map[string]string) string { return "groep " + snap(m, p) + " met alle snapshots verwijderen" }).
		confirmWith(param("backup-id")),
	r("set", `/admin/datastore/`+segStore+`/protected`, snapKeys+`|protected`,
		func(m, p map[string]string) string {
			if p["protected"] == "1" || p["protected"] == "true" {
				return "snapshot " + snap(m, p) + " beschermen"
			}
			return "bescherming van " + snap(m, p) + " weghalen"
		}),
	r("set", `/admin/datastore/`+segStore+`/notes`, snapKeys+`|notes`, func(m, p map[string]string) string { return "notitie bij " + snap(m, p) }),
	r("set", `/admin/datastore/`+segStore+`/group-notes`, `ns|backup-type|backup-id|notes`, func(m, p map[string]string) string { return "notitie bij groep " + snap(m, p) }),
	r("set", `/admin/datastore/`+segStore+`/change-owner`, `ns|backup-type|backup-id|new-owner`,
		func(m, p map[string]string) string { return "eigenaar van " + snap(m, p) + " wordt " + p["new-owner"] }),
	r("create", `/admin/datastore/`+segStore+`/namespace`, `name|parent`, func(m, p map[string]string) string {
		if p["parent"] != "" {
			return "namespace " + p["parent"] + "/" + p["name"] + " in " + m["store"]
		}
		return "namespace " + p["name"] + " in " + m["store"]
	}),
	r("delete", `/admin/datastore/`+segStore+`/namespace`, `ns|delete-groups|error-on-protected`,
		func(m, p map[string]string) string { return "namespace " + p["ns"] + " verwijderen uit " + m["store"] }).confirmWith(param("ns")),
	r("create", `/admin/datastore/`+segStore+`/(?P<mnt>mount|unmount)`, ``, func(m, _ map[string]string) string {
		if m["mnt"] == "mount" {
			return "datastore " + m["store"] + " aankoppelen"
		}
		return "datastore " + m["store"] + " loskoppelen"
	}),

	// --- Jobs ---------------------------------------------------------
	r("create", `/config/(?P<kind>prune|verify|sync)`, jobKeys, func(m, p map[string]string) string { return jobWord[m["kind"]] + " " + p["id"] + " aanmaken" }),
	r("set", `/config/(?P<kind>prune|verify|sync)/`+segID, jobKeys, func(m, _ map[string]string) string { return jobWord[m["kind"]] + " " + m["id"] + " wijzigen" }),
	r("delete", `/config/(?P<kind>prune|verify|sync)/`+segID, `digest`, func(m, _ map[string]string) string { return jobWord[m["kind"]] + " " + m["id"] + " verwijderen" }),
	r("create", `/admin/(?P<kind>prune|verify|sync)/`+segID+`/run`, ``, func(m, _ map[string]string) string { return jobWord[m["kind"]] + " " + m["id"] + " nu uitvoeren" }),
	r("create", `/config/remote`, `name|host|port|auth-id|password|fingerprint|comment`, func(_, p map[string]string) string { return "remote " + p["name"] + " toevoegen" }).secrets("password"),
	r("set", `/config/remote/`+segID, `host|port|auth-id|password|fingerprint|comment|delete|digest`, func(m, _ map[string]string) string { return "remote " + m["id"] + " wijzigen" }).secrets("password"),
	r("delete", `/config/remote/`+segID, `digest`, func(m, _ map[string]string) string { return "remote " + m["id"] + " verwijderen" }),
	r("create", `/config/traffic-control`, anyKey, func(_, p map[string]string) string { return "bandbreedteregel " + p["name"] + " aanmaken" }),
	r("set", `/config/traffic-control/`+segID, anyKey, func(m, _ map[string]string) string { return "bandbreedteregel " + m["id"] + " wijzigen" }),
	r("delete", `/config/traffic-control/`+segID, `digest`, func(m, _ map[string]string) string { return "bandbreedteregel " + m["id"] + " verwijderen" }),

	// --- Taken --------------------------------------------------------
	r("delete", `/nodes/localhost/tasks/`+segUPID, ``, fixed("PBS-taak stoppen")),

	// --- Toegang ------------------------------------------------------
	r("create", `/access/users`, `userid|comment|enable|expire|firstname|lastname|email|password`, func(_, p map[string]string) string { return "gebruiker " + p["userid"] + " aanmaken" }).secrets("password"),
	r("set", `/access/users/`+segUser, `comment|enable|expire|firstname|lastname|email|delete|digest`, func(m, _ map[string]string) string { return "gebruiker " + m["user"] + " wijzigen" }),
	r("delete", `/access/users/`+segUser, `digest`, func(m, _ map[string]string) string { return "gebruiker " + m["user"] + " verwijderen" }).confirmWith(part("user")),
	r("set", `/access/password`, `userid|password`, func(_, p map[string]string) string { return "wachtwoord van " + p["userid"] + " wijzigen" }).secrets("password"),
	r("create", `/access/users/`+segUser+`/token/`+segToken, `comment|enable|expire`, func(m, _ map[string]string) string {
		return "API-token " + m["user"] + "!" + m["token"] + " maken"
	}).hidden(),
	r("set", `/access/users/`+segUser+`/token/`+segToken, `comment|enable|expire|regenerate|delete|digest`, func(m, p map[string]string) string {
		if p["regenerate"] == "1" || p["regenerate"] == "true" {
			return "nieuw geheim voor API-token " + m["user"] + "!" + m["token"]
		}
		return "API-token " + m["user"] + "!" + m["token"] + " wijzigen"
	}).hidden(),
	r("delete", `/access/users/`+segUser+`/token/`+segToken, `digest`, func(m, _ map[string]string) string {
		return "API-token " + m["user"] + "!" + m["token"] + " verwijderen"
	}),
	r("set", `/access/acl`, `path|role|auth-id|group|propagate|delete|digest`, func(_, p map[string]string) string {
		who := p["auth-id"]
		if p["delete"] == "1" || p["delete"] == "true" {
			return "recht " + p["role"] + " op " + p["path"] + " afnemen van " + who
		}
		return "recht " + p["role"] + " op " + p["path"] + " geven aan " + who
	}),

	// --- Systeem ------------------------------------------------------
	r("create", `/nodes/localhost/apt/update`, `notify|quiet`, fixed("pakketlijsten van PBS verversen")),
	r("create", `/nodes/localhost/services/`+segSvc+`/`+segCmd, ``, func(m, _ map[string]string) string { return "dienst " + m["svc"] + " " + svcWord[m["cmd"]] }),
	r("create", `/nodes/localhost/status`, `command`, func(_, p map[string]string) string {
		if p["command"] == "shutdown" {
			return "PBS uitzetten"
		}
		return "PBS herstarten"
	}).confirmWith(fixed("localhost")),
	r("set", `/nodes/localhost/dns`, `search|dns1|dns2|dns3|delete|digest`, fixed("DNS van PBS wijzigen")),
	r("set", `/nodes/localhost/time`, `timezone`, fixed("tijdzone van PBS wijzigen")),
	r("create", `/nodes/localhost/network`, anyKey, func(_, p map[string]string) string { return "netwerkkaart " + p["iface"] + " toevoegen" }),
	r("set", `/nodes/localhost/network/`+segIface, anyKey, func(m, _ map[string]string) string { return "netwerkkaart " + m["iface"] + " wijzigen" }),
	r("delete", `/nodes/localhost/network/`+segIface, `digest`, func(m, _ map[string]string) string { return "netwerkkaart " + m["iface"] + " verwijderen" }),
	r("set", `/nodes/localhost/network`, ``, fixed("netwerkwijzigingen van PBS toepassen")).confirmWith(fixed("netwerk")),
	r("delete", `/nodes/localhost/network`, ``, fixed("netwerkwijzigingen van PBS terugdraaien")),
	r("create", `/nodes/localhost/disks/initgpt`, `disk|uuid`, func(_, p map[string]string) string { return "schijf " + p["disk"] + " GPT geven" }),
	r("set", `/nodes/localhost/disks/wipedisk`, `disk`, func(_, p map[string]string) string { return "schijf " + p["disk"] + " wissen" }).confirmWith(param("disk")),
	r("create", `/nodes/localhost/disks/zfs`, `name|devices|raidlevel|compression|ashift|add-datastore`, func(_, p map[string]string) string { return "ZFS-pool " + p["name"] + " maken" }),
	r("create", `/nodes/localhost/disks/directory`, `name|disk|filesystem|add-datastore|removable-datastore`, func(_, p map[string]string) string { return "map " + p["name"] + " op " + p["disk"] + " maken" }),
	r("create", `/nodes/localhost/certificates/acme/certificate`, `force`, fixed("ACME-certificaat bestellen")),
	r("set", `/nodes/localhost/certificates/acme/certificate`, `force`, fixed("ACME-certificaat vernieuwen")),
	r("create", `/nodes/localhost/certificates/custom`, `certificates|key|force|restart`, fixed("eigen certificaat op PBS zetten")).secrets("key"),
	r("set", `/nodes/localhost/subscription`, `key`, fixed("abonnementssleutel zetten")).secrets("key"),
	r("create", `/nodes/localhost/subscription`, `force`, fixed("abonnement controleren")),
}

var jobWord = map[string]string{"prune": "prune-job", "verify": "verify-job", "sync": "sync-job"}

// Allowed is wat de GUI over een aanroep moet weten.
type Allowed struct {
	Title   string
	Confirm string
	Secret  []string
	Quiet   bool
}

// check zoekt de regel van een aanroep.
func check(method, path string, params map[string]string) (Allowed, error) {
	for _, x := range rules {
		if x.method != method {
			continue
		}
		m := x.path.FindStringSubmatch(path)
		if m == nil {
			continue
		}
		parts := map[string]string{}
		for i, n := range x.path.SubexpNames() {
			if n != "" {
				parts[n] = m[i]
			}
		}
		for k := range params {
			if !x.keys.MatchString(k) {
				return Allowed{}, fmt.Errorf("veld %s mag niet bij %s %s", k, method, path)
			}
		}
		a := Allowed{Title: x.title(parts, params), Secret: x.secret, Quiet: x.quiet}
		if x.confirm != nil {
			a.Confirm = x.confirm(parts, params)
		}
		return a, nil
	}
	return Allowed{}, fmt.Errorf("ClusterForge doet %s %s niet", method, path)
}

// readDenied zijn paden die een kijker niet leest: gebruikers en rechten,
// remotes (wachtwoorden), ACME en meldingen (sleutels), certificaten.
func readDenied(path string) bool {
	for _, p := range []string{"/access", "/config/remote", "/config/acme", "/config/notifications", "/nodes/localhost/certificates", "/nodes/localhost/subscription"} {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}
