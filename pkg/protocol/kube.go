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
)

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
	// labelSelector, fieldSelector, limit en continue.
	Query map[string]string `json:"query,omitempty"`
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
)

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
