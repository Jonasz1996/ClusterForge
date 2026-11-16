package protocol

import "time"

// Beveiliging (fase 27): de agent vertelt wat er op zijn node staat, zodat
// de server bekende kwetsbaarheden kan vaststellen. De agent leest alleen;
// hij installeert, scant of verandert in dit onderwerp niets. Het matchen
// met de Debian Security Tracker en CISA KEV gebeurt op de server.

// SecuritySince is de eerste protocolversie met het subject security.
const SecuritySince = 40

// SubjectSecurity draagt de beveiligingsverzoeken: cf.node.<id>.security.
const SubjectSecurity = "security"

const (
	TypeSecurityRequest = "security_request"
	TypeSecurityResult  = "security_result"
)

// Soorten beveiligingsverzoek.
const (
	// SecurityOpInventory geeft alle geïnstalleerde Debian-pakketten met
	// versie en bronpakket.
	SecurityOpInventory = "inventory"
)

// SecurityRequestTimeout is hoe lang een beveiligingsverzoek mag duren.
const SecurityRequestTimeout = 90 * time.Second

// MaxSecurityPackages begrenst de pakketlijst (een volle desktop heeft er
// zo'n 3000).
const MaxSecurityPackages = 20000

type SecurityRequest struct {
	ID       string    `json:"id"`
	Op       string    `json:"op"`
	Deadline time.Time `json:"deadline"`
}

type SecurityResponse struct {
	Error     string             `json:"error,omitempty"`
	Inventory *SecurityInventory `json:"inventory,omitempty"`
}

// SecurityInventory is de pakketlijst van een node.
type SecurityInventory struct {
	OS     OSInfo `json:"os"`
	Kernel string `json:"kernel"`
	// Dpkg is false op een node zonder dpkg; Packages is dan leeg.
	Dpkg     bool         `json:"dpkg"`
	Packages []DebPackage `json:"packages"`
	// Truncated is true als er meer dan MaxSecurityPackages waren.
	Truncated bool `json:"truncated,omitempty"`
}

// DebPackage is één geïnstalleerd pakket. Source en SourceVersion zijn die
// van het bronpakket; de Security Tracker werkt met bronpakketten.
type DebPackage struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	Arch          string `json:"arch,omitempty"`
	Source        string `json:"source"`
	SourceVersion string `json:"source_version"`
}
