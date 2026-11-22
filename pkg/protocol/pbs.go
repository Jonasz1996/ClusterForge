package protocol

import (
	"fmt"
	"regexp"
	"strings"
)

// PBSSince is de eerste protocolversie die een Proxmox Backup Server
// beheert (fase 32): de facts pbs, het leesverzoek PBSOpGet en de run-actie
// PBSAPI. Net als bij Proxmox VE (fase 26) geeft de agent de API door,
// hier met proxmox-backup-debug api; welke paden en velden de GUI mag
// gebruiken, beslist de server (internal/pbs).
const PBSSince = 49

// PBSOpGet is proxmox-backup-debug api get <Path> met Params, over het
// onderwerp pve (PVERequest).
const PBSOpGet = "pbs_get"

// PBSAPI is proxmox-backup-debug api create|set|delete op een pad, met
// wachten op de taak (UPID) als PBS er een start. Het is een actie van een
// run pve (PVERun.API).
const PBSAPI = "pbs_api"

func init() {
	PVEActions = append(PVEActions, PBSAPI)
}

// PBSFacts staat in de facts van een agent op een Proxmox Backup Server.
type PBSFacts struct {
	// Version is de versie van proxmox-backup-server, zoals 4.0.14-1.
	Version string `json:"version"`
	// Node is de hostnaam van de server.
	Node string `json:"node"`
	// Datastores zijn de namen uit /etc/proxmox-backup/datastore.cfg.
	Datastores []string `json:"datastores,omitempty"`
}

var (
	// Een PBS-pad kan een UPID met \x2d-escapes bevatten.
	pbsAPIPath = regexp.MustCompile(`^(/[A-Za-z0-9_.:@%+=,~\\!-]+)+$`)
	pbsAPIKey  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
)

// ValidPBSAPIPath is true voor een pad van de PBS-API.
func ValidPBSAPIPath(p string) bool {
	if !pbsAPIPath.MatchString(p) || len(p) > 512 {
		return false
	}
	for _, seg := range strings.Split(p[1:], "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// ValidatePBS controleert de vorm van een aanroep van de PBS-API.
func (c *PVEAPICall) ValidatePBS() error {
	switch c.Method {
	case "create", "set", "delete":
	default:
		return fmt.Errorf("methode %q is geen create, set of delete", c.Method)
	}
	if !ValidPBSAPIPath(c.Path) {
		return fmt.Errorf("ongeldig API-pad %q", c.Path)
	}
	if len(c.Params) > 200 {
		return fmt.Errorf("te veel velden")
	}
	for k, v := range c.Params {
		if !pbsAPIKey.MatchString(k) {
			return fmt.Errorf("ongeldig veld %q", k)
		}
		if len(v) > 65536 || strings.ContainsRune(v, 0) {
			return fmt.Errorf("ongeldige waarde voor %s", k)
		}
	}
	for _, k := range c.Secret {
		if _, ok := c.Params[k]; !ok {
			return fmt.Errorf("geheim veld %q staat niet in de velden", k)
		}
	}
	return nil
}

// ValidatePBSGet controleert een leesverzoek van de PBS-API.
func (r PVERequest) ValidatePBSGet() error {
	if !ValidPBSAPIPath(r.Path) {
		return fmt.Errorf("ongeldig API-pad %q", r.Path)
	}
	for k, v := range r.Params {
		if !pbsAPIKey.MatchString(k) || len(v) > 4096 || strings.ContainsAny(v, "\x00\n") {
			return fmt.Errorf("ongeldig veld %q", k)
		}
	}
	return nil
}
