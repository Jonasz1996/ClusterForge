package pvehost

// Welke aanroepen van de Proxmox-API ClusterForge doet (fase 26). De agent
// voert elke pvesh-aanroep uit die er goed uitziet; hier staat wat de GUI
// mag: methode, pad, velden, en wat een overgetikte bevestiging vraagt.

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Delen van een pad.
const (
	segNode = `(?P<node>[A-Za-z0-9][A-Za-z0-9-]{0,62})`
	segType = `(?P<type>qemu|lxc)`
	segVMID = `(?P<vmid>[1-9][0-9]{2,8})`
	segID   = `(?P<id>[A-Za-z0-9][A-Za-z0-9_.:@=-]{0,127})`
	segSub  = `(?P<sub>[A-Za-z0-9][A-Za-z0-9_.:@=/-]{0,127})`
	segStor = `(?P<storage>[A-Za-z][A-Za-z0-9_.-]{0,99})`
	segVol  = `(?P<volume>[A-Za-z][A-Za-z0-9_.-]{0,99}:[A-Za-z0-9][A-Za-z0-9._+/:=-]{0,300})`
	segUPID = `(?P<upid>UPID:[A-Za-z0-9:@._!-]{10,200})`
	segUser = `(?P<user>[A-Za-z0-9][A-Za-z0-9_.-]{0,63}@[A-Za-z0-9][A-Za-z0-9_.-]{0,63})`
)

// anyKey zijn alle gewone velden.
const anyKey = `[a-z][a-z0-9_-]{0,63}`

// deniedKeys mag de GUI nooit zetten: ruwe QEMU-argumenten en hookscripts
// voeren code uit op de host, skiplock omzeilt een slot.
var deniedKeys = []string{"args", "hookscript", "skiplock"}

type rule struct {
	method string
	path   *regexp.Regexp
	keys   *regexp.Regexp
	// title maakt de titel van de taak uit de delen van het pad.
	title func(m map[string]string, params map[string]string) string
	// confirm geeft wat Jonas moet overtikken; leeg is geen bevestiging.
	confirm func(m map[string]string, params map[string]string) string
	// secret zijn velden die nooit in de uitvoer of het logboek komen.
	secret []string
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

func fixed(s string) func(m, p map[string]string) string {
	return func(map[string]string, map[string]string) string { return s }
}

func part(name string) func(m, p map[string]string) string {
	return func(m, _ map[string]string) string { return m[name] }
}

func guestName(m map[string]string) string {
	if m["type"] == "lxc" {
		return "CT " + m["vmid"]
	}
	return "VM " + m["vmid"]
}

func on(m map[string]string) string {
	if m["node"] == "" {
		return ""
	}
	return " op " + m["node"]
}

const (
	guestPath = `/nodes/` + segNode + `/` + segType + `/` + segVMID
	fwRules   = `/firewall/(?:rules(?:/[0-9]{1,4})?|options|aliases(?:/[A-Za-z0-9_-]{1,64})?|ipset(?:/[A-Za-z0-9_-]{1,64}(?:/[0-9a-fA-F.:/]{1,64})?)?|groups(?:/[A-Za-z0-9_-]{1,18}(?:/[0-9]{1,4})?)?)`
)

var rules = []*rule{
	// --- Gasten -----------------------------------------------------
	r("create", `/nodes/`+segNode+`/qemu`, anyKey, func(m, p map[string]string) string {
		if p["archive"] != "" {
			return "VM " + p["vmid"] + " terugzetten uit back-up" + on(m)
		}
		return "VM " + p["vmid"] + " maken" + on(m)
	}).confirmWith(restoreOverExisting).secrets("cipassword"),
	r("create", `/nodes/`+segNode+`/lxc`, anyKey, func(m, p map[string]string) string {
		if p["restore"] == "1" {
			return "CT " + p["vmid"] + " terugzetten uit back-up" + on(m)
		}
		return "CT " + p["vmid"] + " maken" + on(m)
	}).confirmWith(restoreOverExisting).secrets("password"),
	r("set", guestPath+`/config`, anyKey, func(m, _ map[string]string) string { return guestName(m) + " configuratie wijzigen" }).
		confirmWith(deleteUnused).secrets("cipassword", "password"),
	r("create", guestPath+`/status/(?P<cmd>start|stop|shutdown|reboot|reset|suspend|resume)`, `timeout|forceStop|overrule-shutdown|todisk`,
		func(m, _ map[string]string) string { return guestName(m) + " " + statusWord[m["cmd"]] }),
	r("create", guestPath+`/clone`, `newid|name|hostname|full|target|storage|pool|description|snapname|format`,
		func(m, p map[string]string) string { return guestName(m) + " klonen naar " + p["newid"] }),
	r("create", guestPath+`/template`, `disk`, func(m, _ map[string]string) string { return guestName(m) + " omzetten naar template" }).
		confirmWith(part("vmid")),
	r("create", guestPath+`/migrate`, `target|online|with-local-disks|targetstorage|target-storage|restart|timeout|bwlimit`,
		func(m, p map[string]string) string { return guestName(m) + " verhuizen naar " + p["target"] }),
	r("create", `/nodes/`+segNode+`/qemu/`+segVMID+`/move_disk`, `disk|storage|delete|format|bwlimit|target-vmid|target-disk`,
		func(m, p map[string]string) string {
			return "VM " + m["vmid"] + " schijf " + p["disk"] + " naar " + p["storage"]
		}),
	r("create", `/nodes/`+segNode+`/lxc/`+segVMID+`/move_volume`, `volume|storage|delete|bwlimit|target-vmid|target-volume`,
		func(m, p map[string]string) string {
			return "CT " + m["vmid"] + " volume " + p["volume"] + " naar " + p["storage"]
		}),
	r("create", guestPath+`/snapshot`, `snapname|description|vmstate`,
		func(m, p map[string]string) string { return guestName(m) + " snapshot " + p["snapname"] }),
	r("create", guestPath+`/snapshot/`+segID+`/rollback`, `start`,
		func(m, _ map[string]string) string { return guestName(m) + " terugzetten naar snapshot " + m["id"] }).confirmWith(part("id")),
	r("delete", guestPath+`/snapshot/`+segID, `force`,
		func(m, _ map[string]string) string { return guestName(m) + " snapshot " + m["id"] + " verwijderen" }),
	r("set", guestPath+`/snapshot/`+segID+`/config`, `description`,
		func(m, _ map[string]string) string { return guestName(m) + " snapshot " + m["id"] + " beschrijven" }),
	r("set", guestPath+`/resize`, `disk|size`,
		func(m, p map[string]string) string {
			return guestName(m) + " schijf " + p["disk"] + " naar " + p["size"]
		}),
	r("create", `/nodes/`+segNode+`/qemu/`+segVMID+`/agent/(?P<cmd>fsfreeze-thaw|fstrim|ping)`, ``,
		func(m, _ map[string]string) string { return "VM " + m["vmid"] + " guest agent " + m["cmd"] }),
	r("set", `/nodes/`+segNode+`/qemu/`+segVMID+`/cloudinit`, ``,
		func(m, _ map[string]string) string { return "VM " + m["vmid"] + " cloud-init opnieuw maken" }),
	r("set", `/nodes/`+segNode+`/qemu/`+segVMID+`/unlink`, `idlist|force`,
		func(m, p map[string]string) string {
			return "VM " + m["vmid"] + " schijven " + p["idlist"] + " verwijderen"
		}).
		confirmWith(part("vmid")),

	// --- Back-ups ---------------------------------------------------
	r("create", `/cluster/backup`, anyKey, func(_, p map[string]string) string { return "back-upjob " + p["id"] + " maken" }),
	r("set", `/cluster/backup/`+segID, anyKey, func(m, _ map[string]string) string { return "back-upjob " + m["id"] + " wijzigen" }),
	r("delete", `/cluster/backup/`+segID, ``, func(m, _ map[string]string) string { return "back-upjob " + m["id"] + " verwijderen" }),
	r("create", `/nodes/`+segNode+`/vzdump`, anyKey, func(m, p map[string]string) string {
		if p["vmid"] == "" {
			return "back-up nu" + on(m)
		}
		return "back-up nu van " + p["vmid"] + on(m)
	}),
	r("set", `/nodes/`+segNode+`/storage/`+segStor+`/content/`+segVol, `notes|protected`,
		func(m, _ map[string]string) string { return "back-up " + m["volume"] + " aanpassen" }),
	r("delete", `/nodes/`+segNode+`/storage/`+segStor+`/content/`+segVol, `delay`,
		func(m, _ map[string]string) string { return m["volume"] + " verwijderen" }).confirmWith(part("volume")),
	r("delete", `/nodes/`+segNode+`/storage/`+segStor+`/prunebackups`, `prune-backups|type|vmid`,
		func(m, _ map[string]string) string { return "oude back-ups opruimen op " + m["storage"] }).confirmWith(part("storage")),
	r("create", `/nodes/`+segNode+`/storage/`+segStor+`/download-url`, `url|filename|content|checksum|checksum-algorithm|verify-certificates|compression`,
		func(m, p map[string]string) string { return p["filename"] + " downloaden naar " + m["storage"] + on(m) }),
	r("create", `/nodes/`+segNode+`/aplinfo`, `storage|template`,
		func(m, p map[string]string) string {
			return "containertemplate " + p["template"] + " downloaden" + on(m)
		}),

	// --- HA ---------------------------------------------------------
	r("create", `/cluster/ha/resources`, anyKey, func(_, p map[string]string) string { return p["sid"] + " onder HA zetten" }),
	r("set", `/cluster/ha/resources/`+segID, anyKey, func(m, _ map[string]string) string { return "HA van " + m["id"] + " wijzigen" }),
	r("delete", `/cluster/ha/resources/`+segID, `purge`, func(m, _ map[string]string) string { return m["id"] + " uit HA halen" }),
	r("create", `/cluster/ha/resources/`+segID+`/(?P<cmd>migrate|relocate)`, `node`,
		func(m, p map[string]string) string { return m["id"] + " via HA naar " + p["node"] }),
	r("create", `/cluster/ha/(?P<kind>groups|rules)`, anyKey, func(m, p map[string]string) string {
		return haKind[m["kind"]] + " " + firstNonEmpty(p["group"], p["rule"]) + " maken"
	}),
	r("set", `/cluster/ha/(?P<kind>groups|rules)/`+segID, anyKey, func(m, _ map[string]string) string { return haKind[m["kind"]] + " " + m["id"] + " wijzigen" }),
	r("delete", `/cluster/ha/(?P<kind>groups|rules)/`+segID, ``, func(m, _ map[string]string) string { return haKind[m["kind"]] + " " + m["id"] + " verwijderen" }),

	// --- Replicatie -------------------------------------------------
	r("create", `/cluster/replication`, anyKey, func(_, p map[string]string) string { return "replicatie " + p["id"] + " maken" }),
	r("set", `/cluster/replication/`+segID, anyKey, func(m, _ map[string]string) string { return "replicatie " + m["id"] + " wijzigen" }),
	r("delete", `/cluster/replication/`+segID, `keep|force`, func(m, _ map[string]string) string { return "replicatie " + m["id"] + " verwijderen" }),
	r("create", `/nodes/`+segNode+`/replication/`+segID+`/schedule_now`, ``, func(m, _ map[string]string) string { return "replicatie " + m["id"] + " nu" }),

	// --- Hosts ------------------------------------------------------
	r("create", `/nodes/`+segNode+`/status`, `command`, func(m, p map[string]string) string {
		if p["command"] == "shutdown" {
			return m["node"] + " uitzetten"
		}
		return m["node"] + " herstarten"
	}).confirmWith(part("node")),
	r("create", `/nodes/`+segNode+`/startall`, `vms|force`, func(m, _ map[string]string) string { return "alle gasten starten" + on(m) }),
	r("create", `/nodes/`+segNode+`/stopall`, `vms|force-stop|timeout`, func(m, _ map[string]string) string { return "alle gasten stoppen" + on(m) }).
		confirmWith(part("node")),
	r("create", `/nodes/`+segNode+`/migrateall`, `target|maxworkers|vms|with-local-disks`,
		func(m, p map[string]string) string { return "alle gasten van " + m["node"] + " naar " + p["target"] }),
	r("create", `/nodes/`+segNode+`/apt/update`, `notify|quiet`, func(m, _ map[string]string) string { return "pakketlijst bijwerken" + on(m) }),
	r("set", `/nodes/`+segNode+`/(?P<what>config|dns|time)`, anyKey, func(m, _ map[string]string) string { return "instellingen van " + m["node"] + " wijzigen" }),
	r("create", `/nodes/`+segNode+`/wakeonlan`, ``, func(m, _ map[string]string) string { return m["node"] + " wekken (wake-on-LAN)" }),
	r("delete", `/nodes/`+segNode+`/tasks/`+segUPID, ``, func(m, _ map[string]string) string { return "taak stoppen" + on(m) }),

	// --- Storage en schijven ----------------------------------------
	r("create", `/storage`, anyKey, func(_, p map[string]string) string { return "storage " + p["storage"] + " toevoegen" }).
		secrets("password", "encryption-key", "master-pubkey", "keyring"),
	r("set", `/storage/`+segStor, anyKey, func(m, _ map[string]string) string { return "storage " + m["storage"] + " wijzigen" }).
		secrets("password", "encryption-key", "master-pubkey", "keyring"),
	r("delete", `/storage/`+segStor, ``, func(m, _ map[string]string) string { return "storage " + m["storage"] + " weghalen" }).
		confirmWith(part("storage")),
	r("create", `/nodes/`+segNode+`/disks/(?P<kind>zfs|lvm|lvmthin|directory)`, anyKey,
		func(m, p map[string]string) string { return diskKind[m["kind"]] + " " + p["name"] + " maken" + on(m) }).
		confirmWith(func(_, p map[string]string) string { return p["name"] }),
	r("create", `/nodes/`+segNode+`/disks/initgpt`, `disk|uuid`, func(m, p map[string]string) string { return p["disk"] + " GPT geven" + on(m) }).
		confirmWith(func(_, p map[string]string) string { return p["disk"] }),
	r("set", `/nodes/`+segNode+`/disks/wipedisk`, `disk`, func(m, p map[string]string) string { return p["disk"] + " wissen" + on(m) }).
		confirmWith(func(_, p map[string]string) string { return p["disk"] }),

	// --- Netwerk en SDN ---------------------------------------------
	r("create", `/nodes/`+segNode+`/network`, anyKey, func(m, p map[string]string) string { return "interface " + p["iface"] + " toevoegen" + on(m) }),
	r("set", `/nodes/`+segNode+`/network/`+segID, anyKey, func(m, _ map[string]string) string { return "interface " + m["id"] + " wijzigen" + on(m) }),
	r("delete", `/nodes/`+segNode+`/network/`+segID, ``, func(m, _ map[string]string) string { return "interface " + m["id"] + " verwijderen" + on(m) }),
	r("set", `/nodes/`+segNode+`/network`, `regenerate-frr`, func(m, _ map[string]string) string { return "netwerkwijzigingen toepassen" + on(m) }).
		confirmWith(part("node")),
	r("delete", `/nodes/`+segNode+`/network`, ``, func(m, _ map[string]string) string { return "netwerkwijzigingen terugdraaien" + on(m) }),
	r("create", `/cluster/sdn/`+segSub, anyKey, func(m, _ map[string]string) string { return "SDN " + m["sub"] + " toevoegen" }),
	r("set", `/cluster/sdn/`+segSub, anyKey, func(m, _ map[string]string) string { return "SDN " + m["sub"] + " wijzigen" }),
	r("delete", `/cluster/sdn/`+segSub, ``, func(m, _ map[string]string) string { return "SDN " + m["sub"] + " verwijderen" }),
	r("set", `/cluster/sdn`, `lock-token|release-lock`, fixed("SDN-wijzigingen toepassen")).confirmWith(fixed("sdn")),

	// --- Firewall van Proxmox ---------------------------------------
	r("create|set|delete", `/cluster`+fwRules, anyKey, fixed("firewall van het datacenter wijzigen")),
	r("create|set|delete", `/nodes/`+segNode+fwRules, anyKey, func(m, _ map[string]string) string { return "firewall van " + m["node"] + " wijzigen" }),
	r("create|set|delete", guestPath+fwRules, anyKey, func(m, _ map[string]string) string { return "firewall van " + guestName(m) + " wijzigen" }),

	// --- Certificaten -----------------------------------------------
	r("create|set", `/cluster/acme/account(?:/`+segID+`)?`, anyKey, fixed("ACME-account instellen")).secrets("eab-hmac-key"),
	r("delete", `/cluster/acme/account/`+segID, ``, func(m, _ map[string]string) string { return "ACME-account " + m["id"] + " verwijderen" }),
	r("create|set", `/cluster/acme/plugins(?:/`+segID+`)?`, anyKey, fixed("ACME-plugin instellen")).secrets("data"),
	r("delete", `/cluster/acme/plugins/`+segID, ``, func(m, _ map[string]string) string { return "ACME-plugin " + m["id"] + " verwijderen" }),
	r("create|set", `/nodes/`+segNode+`/certificates/acme/certificate`, `force`, func(m, _ map[string]string) string { return "certificaat bestellen" + on(m) }),
	r("delete", `/nodes/`+segNode+`/certificates/acme/certificate`, ``, func(m, _ map[string]string) string { return "ACME-certificaat intrekken" + on(m) }).
		confirmWith(part("node")),
	r("create", `/nodes/`+segNode+`/certificates/custom`, `certificates|key|restart|force`, func(m, _ map[string]string) string { return "eigen certificaat zetten" + on(m) }).
		secrets("key"),
	r("delete", `/nodes/`+segNode+`/certificates/custom`, `restart`, func(m, _ map[string]string) string { return "eigen certificaat weghalen" + on(m) }),

	// --- Gebruikers, groepen, rechten -------------------------------
	r("create", `/access/users`, anyKey, func(_, p map[string]string) string { return "gebruiker " + p["userid"] + " maken" }).secrets("password"),
	r("set", `/access/users/`+segUser, anyKey, func(m, _ map[string]string) string { return "gebruiker " + m["user"] + " wijzigen" }),
	r("delete", `/access/users/`+segUser, ``, func(m, _ map[string]string) string { return "gebruiker " + m["user"] + " verwijderen" }).
		confirmWith(part("user")),
	r("set|delete", `/access/users/`+segUser+`/token/`+segID, anyKey, func(m, _ map[string]string) string { return "API-token " + m["user"] + "!" + m["id"] + " wijzigen" }),
	r("set", `/access/password`, `userid|password|confirmation-password`, func(_, p map[string]string) string { return "wachtwoord van " + p["userid"] + " wijzigen" }).
		secrets("password", "confirmation-password"),
	r("set", `/access/acl`, anyKey, fixed("rechten wijzigen")),
	r("create", `/access/(?P<kind>groups|roles|domains)`, anyKey, func(m, _ map[string]string) string { return accessKind[m["kind"]] + " maken" }),
	r("set|delete", `/access/(?P<kind>groups|roles|domains)/`+segID, anyKey, func(m, _ map[string]string) string { return accessKind[m["kind"]] + " " + m["id"] + " wijzigen" }),

	// --- Datacenter -------------------------------------------------
	r("set", `/cluster/options`, anyKey, fixed("datacenter-opties wijzigen")),
	r("create", `/pools`, anyKey, func(_, p map[string]string) string { return "pool " + p["poolid"] + " maken" }),
	r("set|delete", `/pools/`+segID, anyKey, func(m, _ map[string]string) string { return "pool " + m["id"] + " wijzigen" }),
	r("set", `/pools`, anyKey, func(_, p map[string]string) string { return "pool " + p["poolid"] + " wijzigen" }),
	r("delete", `/pools`, `poolid`, func(_, p map[string]string) string { return "pool " + p["poolid"] + " verwijderen" }),
}

var statusWord = map[string]string{
	"start": "starten", "stop": "hard uitzetten", "shutdown": "afsluiten", "reboot": "herstarten",
	"reset": "resetten", "suspend": "pauzeren", "resume": "hervatten",
}

var haKind = map[string]string{"groups": "HA-groep", "rules": "HA-regel"}

var diskKind = map[string]string{"zfs": "ZFS-pool", "lvm": "LVM-groep", "lvmthin": "LVM-thinpool", "directory": "map"}

var accessKind = map[string]string{"groups": "groep", "roles": "rol", "domains": "realm"}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// restoreOverExisting vraagt het VMID overtikken als een back-up over een
// bestaande gast komt (force=1).
func restoreOverExisting(_, p map[string]string) string {
	if p["force"] == "1" {
		return p["vmid"]
	}
	return ""
}

// deleteUnused vraagt het VMID overtikken als een ongebruikte schijf
// weggehaald wordt: Proxmox wist dan het volume.
func deleteUnused(m, p map[string]string) string {
	for _, k := range strings.Split(p["delete"], ",") {
		if strings.HasPrefix(strings.TrimSpace(k), "unused") {
			return m["vmid"]
		}
	}
	return ""
}

// allowed is het oordeel over één aanroep.
type allowed struct {
	Title   string
	Confirm string
	Secret  []string
	// Node is de host uit het pad, als het pad over een host gaat.
	Node string
}

// check zoekt de regel voor een aanroep.
func check(method, path string, params map[string]string) (allowed, error) {
	for _, x := range rules {
		if !slices.Contains(strings.Split(x.method, "|"), method) {
			continue
		}
		sm := x.path.FindStringSubmatch(path)
		if sm == nil {
			continue
		}
		m := map[string]string{}
		for i, n := range x.path.SubexpNames() {
			if n != "" {
				m[n] = sm[i]
			}
		}
		for k := range params {
			if slices.Contains(deniedKeys, k) {
				return allowed{}, fmt.Errorf("het veld %s kan niet via ClusterForge (voert code uit op de host)", k)
			}
			if !x.keys.MatchString(k) {
				return allowed{}, fmt.Errorf("het veld %s hoort niet bij %s %s", k, method, path)
			}
		}
		a := allowed{Title: x.title(m, params), Node: m["node"]}
		if x.confirm != nil {
			a.Confirm = x.confirm(m, params)
		}
		for _, k := range x.secret {
			if _, ok := params[k]; ok {
				a.Secret = append(a.Secret, k)
			}
		}
		if path == "/access/users/root@pam" && method == "delete" {
			return allowed{}, fmt.Errorf("root@pam kun je niet verwijderen")
		}
		return a, nil
	}
	return allowed{}, fmt.Errorf("ClusterForge kent %s %s niet", method, path)
}

// readDenied zegt of een kijker (geen beheerder) een pad niet mag lezen:
// daar kunnen geheimen in staan.
func readDenied(path string) bool {
	return strings.HasPrefix(path, "/access") || strings.HasPrefix(path, "/cluster/acme/plugins") || strings.Contains(path, "/certificates/")
}
