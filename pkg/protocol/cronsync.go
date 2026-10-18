package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"slices"
	"strings"
)

// Bestandssync (fase 6, mijlpaal 6): een beheerde job van de soort sync
// kopieert met rsync over SSH een map van de bron naar de doelen. De bron
// heeft een eigen sleutel; op een doel mag die sleutel alleen
// `cf-agent sync-receive`, dat alleen rsync naar de doelmap toelaat.

// CronSyncSince is de eerste protocolversie met bestandssync: CronKeys,
// CronDryRun, Receives bij CronApply en `cf-agent sync-receive`.
const CronSyncSince = 21

// Soorten beheerde job.
const (
	CronKindCommand = "command"
	CronKindSync    = "sync"
)

// Waar de agent de sync bewaart.
const (
	// SyncDir heeft de sleutel id_ed25519, known_hosts met de hostsleutels
	// van de doelen, en receive.json met wat deze node ontvangt.
	SyncDir = "/etc/clusterforge/sync"
	// SyncAuthorizedKeys krijgt een blok tussen SyncBlockBegin en
	// SyncBlockEnd; de rest van het bestand blijft zoals het is.
	SyncAuthorizedKeys = "/root/.ssh/authorized_keys"
	SyncBlockBegin     = "# clusterforge-sync begin"
	SyncBlockEnd       = "# clusterforge-sync einde"
	// SyncRefused staat in de foutmelding van sync-receive als het doel
	// weigert; de bron herkent daaraan een geweigerd doel.
	SyncRefused = "ClusterForge weigert"
	// MaxSyncDryRun is het grootste aantal regels van een proefrun.
	MaxSyncDryRun = 500
)

// SyncSpec is een sync zoals de bron hem draait.
type SyncSpec struct {
	// Source is de map op de bron, Target die op elk doel; allebei met een
	// / op het einde.
	Source  string   `json:"source"`
	Target  string   `json:"target"`
	Delete  bool     `json:"delete,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
	// BwLimitKB is de bandbreedte in KiB/s; 0 is onbeperkt.
	BwLimitKB int          `json:"bwlimit_kb,omitempty"`
	Targets   []SyncTarget `json:"targets"`
}

// SyncTarget is een doel van een sync.
type SyncTarget struct {
	NodeID   string `json:"node_id"`
	Hostname string `json:"hostname"`
	Address  string `json:"address"`
	// HostKeys zijn de publieke hostsleutels van het doel, zoals
	// "ssh-ed25519 AAAA…", voor known_hosts.
	HostKeys []string `json:"host_keys"`
}

// SyncReceive is een sync die deze node ontvangt, van één bron-node.
type SyncReceive struct {
	JobID string `json:"job_id"`
	// From is de node-id van de bron, FromHost zijn naam.
	From     string `json:"from"`
	FromHost string `json:"from_host"`
	// Addresses zijn de adressen waarvan de bron mag komen.
	Addresses []string `json:"addresses"`
	PublicKey string   `json:"public_key"`
	// Path is de doelmap, met een / op het einde.
	Path   string `json:"path"`
	Delete bool   `json:"delete,omitempty"`
	// VIPs zijn de VIP's van het cluster van dit doel: heeft het doel er
	// één, dan weigert het.
	VIPs []string `json:"vips,omitempty"`
}

// SyncKeys is het antwoord op CronKeys.
type SyncKeys struct {
	PublicKey string   `json:"public_key"`
	HostKeys  []string `json:"host_keys"`
	// Rsync en SSH: de programma's staan op de node; SSHD: sshd draait.
	Rsync bool `json:"rsync"`
	SSH   bool `json:"ssh"`
	SSHD  bool `json:"sshd"`
	// PermitRootLogin zoals sshd hem leest; leeg als dat niet lukte.
	PermitRootLogin string `json:"permit_root_login"`
	// Free is per map in Paths de vrije ruimte in bytes, van de dichtstbij
	// bestaande map; -1 als het niet lukte.
	Free map[string]int64 `json:"free,omitempty"`
}

// Uitkomst per doel van een sync.
const (
	SyncOK      = "ok"
	SyncFailed  = "failed"
	SyncRefusal = "refused"
)

// SyncResult is de uitkomst van een sync naar één doel.
type SyncResult struct {
	NodeID   string `json:"node_id"`
	Hostname string `json:"hostname"`
	Result   string `json:"result"`
	Exit     int    `json:"exit"`
	// Files is het aantal gekopieerde bestanden, Bytes hun grootte, Deleted
	// het aantal weggehaalde bestanden.
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
	Deleted int    `json:"deleted"`
	Error   string `json:"error,omitempty"`
}

// CronApplyHash is de hash van wat CronApply op een node zet. Zonder
// Receives is het CronSetHash, zoals agents van voor protocol 21 hem geven.
func CronApplyHash(jobs []ManagedCron, receives []SyncReceive) string {
	if len(receives) == 0 {
		return CronSetHash(jobs)
	}
	j := slices.Clone(jobs)
	slices.SortFunc(j, func(a, b ManagedCron) int { return strings.Compare(a.ID, b.ID) })
	r := slices.Clone(receives)
	slices.SortFunc(r, func(a, b SyncReceive) int { return strings.Compare(a.JobID+a.From, b.JobID+b.From) })
	data, _ := json.Marshal(struct {
		Jobs     []ManagedCron `json:"jobs"`
		Receives []SyncReceive `json:"receives"`
	}{j, r})
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:16])
}

// ValidSyncPath: een absoluut pad met een / op het einde, zonder .., niet
// / zelf, en zonder tekens die ssh of rsync anders lezen.
func ValidSyncPath(p string) bool {
	return strings.HasPrefix(p, "/") && strings.HasSuffix(p, "/") && p != "/" && path.Clean(p)+"/" == p &&
		!strings.ContainsAny(p, "\\\n\r\t'\"`$*?[]{}:;&|<>")
}
