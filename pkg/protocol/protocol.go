// Package protocol bevat de berichten tussen clusterforge-server en cf-agent.
// Server en agent gebruiken dezelfde structs; een wijziging hier raakt beide.
package protocol

import (
	"encoding/json"
	"strings"
	"time"
)

// Version is de protocolversie. Een agent meldt die bij elke heartbeat, zodat
// de server geen berichten stuurt die de agent niet kent.
const Version = 1

// Berichttypes.
const (
	TypeHeartbeat = "heartbeat"
	TypeFacts     = "facts"
	TypeAck       = "ack"
)

// Envelope is de buitenkant van elk bericht.
type Envelope struct {
	V    int             `json:"v"`
	ID   string          `json:"id"`
	Type string          `json:"type"`
	TS   time.Time       `json:"ts"`
	Body json.RawMessage `json:"body"`
}

// Subjects per node. De agent mag alleen publiceren onder NodePrefix en
// alleen luisteren op zijn eigen inbox.
const (
	SubjectHeartbeat = "hb"
	SubjectFacts     = "facts"
	SubjectEvents    = "events"
	SubjectCommands  = "cmd"
)

func NodePrefix(nodeID string) string { return "cf.node." + nodeID }

func Subject(nodeID, kind string) string { return NodePrefix(nodeID) + "." + kind }

// InboxPrefix is het inboxprefix van een agent; replies van de server komen
// daaronder binnen.
func InboxPrefix(nodeID string) string { return "_INBOX." + nodeID }

// ParseSubject haalt node-id en soort uit een subject als cf.node.<id>.hb.
func ParseSubject(subject string) (nodeID, kind string, ok bool) {
	rest, found := strings.CutPrefix(subject, "cf.node.")
	if !found {
		return "", "", false
	}
	nodeID, kind, ok = strings.Cut(rest, ".")
	return nodeID, kind, ok && nodeID != "" && kind != ""
}

// Heartbeat stuurt de agent elke HeartbeatInterval.
type Heartbeat struct {
	AgentVersion    string     `json:"agent_version"`
	ProtocolVersion int        `json:"protocol_version"`
	UptimeSeconds   int64      `json:"uptime_seconds"`
	Load            [3]float64 `json:"load"`
	// Addresses zijn alle IP-adressen op de interfaces, zonder prefix. Daaruit
	// leidt de server af welke node een VIP heeft.
	Addresses []string `json:"addresses"`
	// Services is de toestand (systemctl is-active) van de gevolgde units.
	Services map[string]string `json:"services"`
}

const (
	HeartbeatInterval = 10 * time.Second
	FactsInterval     = 15 * time.Minute
)

// Facts beschrijven de node. De agent stuurt ze bij start, na een herverbinding
// en elke FactsInterval; de server bewaart alleen een gewijzigde set.
type Facts struct {
	Hostname       string       `json:"hostname"`
	MachineID      string       `json:"machine_id"`
	OS             OSInfo       `json:"os"`
	Kernel         string       `json:"kernel"`
	Arch           string       `json:"arch"`
	Virtualization string       `json:"virtualization"`
	CPUs           int          `json:"cpus"`
	MemoryBytes    uint64       `json:"memory_bytes"`
	SwapBytes      uint64       `json:"swap_bytes"`
	BootTime       time.Time    `json:"boot_time"`
	PrimaryAddress string       `json:"primary_address"`
	Interfaces     []Interface  `json:"interfaces"`
	Filesystems    []Filesystem `json:"filesystems"`
	Services       []Service    `json:"services"`
	Docker         *Docker      `json:"docker"`
	Keepalived     *Keepalived  `json:"keepalived"`
	Upgrades       *Upgrades    `json:"upgrades"`
}

type OSInfo struct {
	ID         string `json:"id"`
	VersionID  string `json:"version_id"`
	Codename   string `json:"codename"`
	PrettyName string `json:"pretty_name"`
}

type Interface struct {
	Name      string   `json:"name"`
	MAC       string   `json:"mac"`
	Up        bool     `json:"up"`
	Addresses []string `json:"addresses"`
}

type Filesystem struct {
	Mount     string `json:"mount"`
	Device    string `json:"device"`
	Type      string `json:"type"`
	SizeBytes uint64 `json:"size_bytes"`
	UsedBytes uint64 `json:"used_bytes"`
}

type Service struct {
	Name    string `json:"name"`
	Active  string `json:"active"`
	Enabled string `json:"enabled"`
}

type Docker struct {
	Version    string `json:"version"`
	Containers int    `json:"containers"`
}

// Keepalived beschrijft wat er in keepalived.conf staat.
type Keepalived struct {
	Active string   `json:"active"`
	VIPs   []string `json:"vips"`
}

type Upgrades struct {
	Total    int      `json:"total"`
	Security int      `json:"security"`
	Packages []string `json:"packages"`
}

// Ack is het antwoord van de server op een request.
type Ack struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// EnrollRequest stuurt de agent naar POST /api/v1/agents/enroll.
type EnrollRequest struct {
	Token           string `json:"token"`
	NkeyPublic      string `json:"nkey_public"`
	Hostname        string `json:"hostname"`
	MachineID       string `json:"machine_id"`
	AgentVersion    string `json:"agent_version"`
	ProtocolVersion int    `json:"protocol_version"`
}

type EnrollResponse struct {
	AgentID        string `json:"agent_id"`
	NodeID         string `json:"node_id"`
	NatsURL        string `json:"nats_url"`
	NatsCertSHA256 string `json:"nats_cert_sha256"`
}

// Stable geeft een kopie zonder velden die bij elke meting veranderen, zoals
// schijfgebruik. Daarover rekent de server de hash, zodat alleen een echte
// wijziging een nieuwe snapshot en een event oplevert.
func (f Facts) Stable() Facts {
	out := f
	out.Filesystems = make([]Filesystem, len(f.Filesystems))
	for i, fs := range f.Filesystems {
		fs.UsedBytes = 0
		out.Filesystems[i] = fs
	}
	if f.Docker != nil {
		d := *f.Docker
		d.Containers = 0
		out.Docker = &d
	}
	return out
}
