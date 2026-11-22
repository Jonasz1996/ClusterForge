package protocol

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// KubeSince is de eerste protocolversie met de kube-brug: een agent op een
// k3s-server stuurt verzoeken van de server door naar de Kubernetes-API op
// zijn eigen node, en elke agent meldt in de facts hoe k3s erbij staat.
const KubeSince = 8

// SubjectKube draagt de verzoeken van de kube-brug: cf.node.<id>.kube. Het
// is een eigen subject en niet cmd, omdat de agent commando's een voor een
// uitvoert; het dashboard wacht dan niet op een lange apply.
const SubjectKube = "kube"

const (
	TypeKubeRequest = "kube_request"
	TypeKubeResult  = "kube_result"
)

// Soorten kube-verzoek. De agent kent alleen deze en controleert elk pad en
// elke queryparameter tegen een vaste lijst. exec, attach, portforward en
// proxy bestaan niet in de brug.
const (
	// KubeRead is een GET op de API: discovery, /version, /readyz, en get of
	// list van elk soort object, ook de status ervan.
	KubeRead = "read"
	// KubeLogs leest de laatste regels van een container in een pod, ook
	// van de vorige als hij herstartte. Body is de tekst als JSON-string.
	KubeLogs = "logs"
	// KubePatch wijzigt een app of node met een merge patch of een JSON
	// patch: schalen, herstarten, terugdraaien, cordon en uncordon. Alleen
	// Deployments, StatefulSets, ReplicaSets, DaemonSets en nodes, en de
	// subresource scale.
	KubePatch = "patch"
	// KubeDelete haalt één object weg.
	KubeDelete = "delete"
	// KubeEvict zet één pod uit via de Eviction-API, zodat een
	// PodDisruptionBudget geldt. Path is
	// /api/v1/namespaces/<ns>/pods/<naam>/eviction; de agent maakt de body.
	KubeEvict = "evict"
	// KubeApply doet server-side apply van één object, zoals kubectl apply
	// --server-side. Body is het object als JSON. Een veld dat een ander
	// programma beheert, neemt hij alleen over met force=true.
	KubeApply = "apply"
	// KubeToken geeft een token met een vervaldatum voor een eigen
	// ServiceAccount in de namespace KubeAccessNamespace, met de rol uit
	// KubeTokenRequest, of haalt dat ServiceAccount weer weg. Body van het
	// antwoord is een KubeTokenResult.
	KubeToken = "token"
	// KubeSnapshot maakt een etcd-snapshot op deze server, of doet een stap
	// van het terugzetten van een snapshot, zoals in KubeSnapshotRequest.
	// Body van het antwoord is een KubeSnapshotResult.
	KubeSnapshot = "snapshot"
)

// Stappen van KubeSnapshot. Terugzetten gaat zoals de documentatie van k3s
// het beschrijft: k3s stoppen op elke server, het cluster terugzetten op
// de server die het begon, k3s daar starten, en op de andere servers de
// database van etcd opzij zetten en k3s starten, zodat ze zich opnieuw
// aanmelden.
const (
	// KubeSnapshotCheck verandert niets: hij zegt of k3s draait en of deze
	// server het cluster begon. Zo weet de server voor het terugzetten dat
	// elke server mee kan.
	KubeSnapshotCheck = "check"
	// KubeSnapshotSave is k3s etcd-snapshot save --name Name.
	KubeSnapshotSave = "save"
	// KubeSnapshotStop is systemctl stop k3s.
	KubeSnapshotStop = "stop"
	// KubeSnapshotReset is k3s server --cluster-reset
	// --cluster-reset-restore-path met het bestand File in
	// KubeSnapshotDir. Alleen op de server met cluster-init, en alleen als
	// k3s stilstaat.
	KubeSnapshotReset = "reset"
	// KubeSnapshotStart is systemctl start k3s.
	KubeSnapshotStart = "start"
	// KubeSnapshotRejoin zet de database van etcd opzij op een server die
	// zich via server: aanmeldt, zodat hij bij de volgende start opnieuw
	// lid wordt. De snapshots blijven. Alleen als k3s stilstaat.
	KubeSnapshotRejoin = "rejoin"
)

// KubeSnapshotDir is de map met de snapshots van k3s, en KubeDBDir die
// van zijn database.
const (
	KubeDBDir       = "/var/lib/rancher/k3s/server/db"
	KubeSnapshotDir = KubeDBDir + "/snapshots"
)

// KubeSnapshotTimeout is hoe lang een stap van KubeSnapshot mag duren: het
// terugzetten zelf kan minuten duren.
const KubeSnapshotTimeout = 10 * time.Minute

// KubeSnapshotNamePattern is de vorm van Name bij KubeSnapshotSave; k3s
// zet er de node en het tijdstip achter.
var KubeSnapshotNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,30}[a-z0-9])?$`)

// KubeSnapshotFilePattern is de vorm van File bij KubeSnapshotReset: een
// bestandsnaam zonder map.
var KubeSnapshotFilePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,200}$`)

// KubeSnapshotRequest hoort bij KubeSnapshot.
type KubeSnapshotRequest struct {
	Action string `json:"action"`
	// Name bij KubeSnapshotSave.
	Name string `json:"name,omitempty"`
	// File bij KubeSnapshotReset.
	File string `json:"file,omitempty"`
}

// KubeSnapshotResult is het antwoord op KubeSnapshot: de laatste regels van
// wat k3s of systemctl schreef, en bij KubeSnapshotRejoin waar de oude
// database nu staat.
type KubeSnapshotResult struct {
	Output []string `json:"output,omitempty"`
	Moved  string   `json:"moved,omitempty"`
	// Bij KubeSnapshotCheck: Active is wat systemctl is-active k3s zegt,
	// Init dat cluster-init in de config staat, en Join dat server: erin
	// staat.
	Active string `json:"active,omitempty"`
	Init   bool   `json:"init,omitempty"`
	Join   bool   `json:"join,omitempty"`
}

// KubeAccessNamespace is de namespace met de ServiceAccounts van de
// kubeconfigs die ClusterForge uitgeeft.
const KubeAccessNamespace = "clusterforge"

// Rollen van een kubeconfig, en de ClusterRole van Kubernetes die erbij
// hoort. De agent kent alleen deze twee.
const (
	KubeAccessAdmin = "admin"
	KubeAccessView  = "view"
)

// KubeAccessRoles zet een rol om in de ClusterRole van Kubernetes.
var KubeAccessRoles = map[string]string{KubeAccessAdmin: "cluster-admin", KubeAccessView: "view"}

// Grenzen van een token: hoogstens dertig dagen, minstens tien minuten,
// zoals de API van Kubernetes ze ook eist.
const (
	KubeTokenMin = 10 * time.Minute
	KubeTokenMax = 30 * 24 * time.Hour
)

// KubeAccountPattern is de vorm van de naam van zo'n ServiceAccount. De
// agent weigert elke andere naam, zodat de server nooit een token kan
// vragen voor een ServiceAccount van Kubernetes zelf.
var KubeAccountPattern = regexp.MustCompile(`^cf-[a-z0-9]([-a-z0-9]{0,58}[a-z0-9])?$`)

// KubeBindingName is de naam van de ClusterRoleBinding van een
// ServiceAccount.
func KubeBindingName(account string) string { return "clusterforge:" + account }

// KubeTokenRequest hoort bij KubeToken.
type KubeTokenRequest struct {
	// Account is de naam van het ServiceAccount, volgens
	// KubeAccountPattern.
	Account string `json:"account"`
	// Role is KubeAccessAdmin of KubeAccessView.
	Role string `json:"role,omitempty"`
	// Seconds is hoe lang het token geldt.
	Seconds int64 `json:"seconds,omitempty"`
	// Owner is wie de kubeconfig kreeg; hij komt als annotatie op het
	// ServiceAccount, zodat kubectl get serviceaccounts toont van wie het
	// is.
	Owner string `json:"owner,omitempty"`
	// Revoke haalt het ServiceAccount en zijn ClusterRoleBinding weg; dan
	// werkt elk token ervan meteen niet meer.
	Revoke bool `json:"revoke,omitempty"`
}

// KubeTokenResult is het antwoord op KubeToken. CA is het CA-certificaat
// van de API als PEM, uit de kubeconfig van k3s; het is openbaar. Bij
// Revoke zijn ze leeg.
type KubeTokenResult struct {
	Token   string    `json:"token,omitempty"`
	Expires time.Time `json:"expires,omitzero"`
	CA      []byte    `json:"ca,omitempty"`
}

// Soorten patch bij KubePatch.
const (
	// KubeMergePatch is een JSON merge patch (RFC 7386).
	KubeMergePatch = "merge"
	// KubeJSONPatch is een JSON patch (RFC 6902), zoals voor terugdraaien:
	// het hele podtemplate vervangen.
	KubeJSONPatch = "json"
)

// KubeFieldManager is de naam waaronder ClusterForge in managedFields staat.
const KubeFieldManager = "clusterforge"

// KubeRequest is een verzoek aan de Kubernetes-API via de agent. De server
// stuurt het als request op cf.node.<id>.kube; de agent antwoordt met een
// KubeResponse.
type KubeRequest struct {
	ID string `json:"id"`
	Op string `json:"op"`
	// Path is het pad op de API zonder query, zoals
	// /api/v1/namespaces/demo/pods.
	Path string `json:"path"`
	// Query zijn de queryparameters. Bij lezen kent de agent alleen
	// labelSelector, fieldSelector, limit en continue; bij logs container,
	// previous, tailLines en timestamps; bij patch dryRun; bij delete
	// propagationPolicy, gracePeriodSeconds en dryRun; bij apply dryRun en
	// force.
	Query map[string]string `json:"query,omitempty"`
	// Body is de patch bij KubePatch, met PatchType als soort, en het object
	// bij KubeApply.
	Body      json.RawMessage `json:"body,omitempty"`
	PatchType string          `json:"patch_type,omitempty"`
	// Token hoort bij KubeToken.
	Token *KubeTokenRequest `json:"token,omitempty"`
	// Snapshot hoort bij KubeSnapshot.
	Snapshot *KubeSnapshotRequest `json:"snapshot,omitempty"`
	// Deadline: na dit tijdstip weigert de agent het verzoek.
	Deadline time.Time `json:"deadline"`
}

// KubeResponse is het antwoord op een KubeRequest. Status is de HTTP-status
// van de API. Is ze nul, dan kwam het verzoek niet bij de API, en staat de
// reden in Error.
type KubeResponse struct {
	Status int `json:"status"`
	// Body is het antwoord van de API als JSON, zonder managedFields en
	// zonder de waarden van Secrets. Is het antwoord groter dan
	// KubeGzipAbove, dan is Body leeg en staat het gecomprimeerd in Gzip.
	Body  json.RawMessage `json:"body,omitempty"`
	Gzip  []byte          `json:"gzip,omitempty"`
	Error string          `json:"error,omitempty"`
	// Same is bij KubeApply true als het object al zo stond. De agent
	// vergelijkt het object van voor en na, met de waarden van een Secret,
	// voordat hij die weghaalt.
	Same bool `json:"same,omitempty"`
}

// Grenzen van de kube-brug. Een NATS-bericht is hoogstens 1 MiB, en Gzip
// gaat als base64, dus een derde groter.
const (
	// KubeGzipAbove: een groter antwoord gaat gecomprimeerd.
	KubeGzipAbove = 64 << 10
	// KubeMaxBody is het grootste antwoord dat de agent terugstuurt, na
	// compressie.
	KubeMaxBody = 640 << 10
	// KubeMaxRead is hoeveel de agent hoogstens van de API leest.
	KubeMaxRead = 32 << 20
	// KubePageSize is het standaardaantal objecten per pagina van een
	// lijst, KubeMaxPage het hoogste.
	KubePageSize = 250
	KubeMaxPage  = 500
	// KubeMaxLogLines is het hoogste aantal logregels per verzoek, en
	// KubeMaxLogBytes hoeveel bytes log de agent hoogstens leest.
	KubeMaxLogLines = 5000
	KubeMaxLogBytes = 2 << 20
	// KubeMaxPatch is de grootste patch, of het grootste object bij apply,
	// die de agent doorstuurt.
	KubeMaxPatch = 256 << 10
)

// KubeLogTrimmed staat voor een log waarvan de agent de oudste regels
// wegliet, omdat hij groter was dan KubeMaxLogBytes of dan een bericht.
const KubeLogTrimmed = "[ClusterForge: oudere regels weggelaten, de log is te groot]\n"

// KubeFacts beschrijft k3s op de node; vanaf protocol 8. Ze zijn nil als er
// geen k3s op de node staat.
type KubeFacts struct {
	// Role is server of agent, zoals k3s ze noemt.
	Role string `json:"role"`
	// Version is de versie van k3s, zoals v1.35.9+k3s1; leeg als ze niet te
	// lezen is.
	Version string `json:"version,omitempty"`
	// NodeName is de naam van de node in Kubernetes.
	NodeName string `json:"node_name"`
	// APIReady: de API op deze node antwoordt op /readyz. Alleen bij een
	// server.
	APIReady bool `json:"api_ready"`
	// KubeletReady: de kubelet antwoordt op zijn healthz.
	KubeletReady bool `json:"kubelet_ready"`
	// Init: deze server begon het cluster (cluster-init in de config van
	// k3s). Alleen daar kan een snapshot terug.
	Init bool `json:"init,omitempty"`
}

// KubeHidden is wat de agent in een Secret zet in plaats van een waarde:
// alleen de lengte in bytes.
func KubeHidden(n int) string { return fmt.Sprintf("(verborgen, %d bytes)", n) }

var kubeHidden = regexp.MustCompile(`^\(verborgen, (\d+) bytes\)$`)

// KubeHiddenLength geeft de lengte uit een waarde van KubeHidden.
func KubeHiddenLength(s string) (int, bool) {
	m := kubeHidden.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}
