package protocol

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// TrueNASSince is de eerste protocolversie die TrueNAS SCALE beheert
// (fase 37): de facts truenas, het leesverzoek TrueNASOpGet en de
// run-actie TrueNASCall. Net als bij PBS geeft de agent de API door, hier
// de middleware met midclt; welke methodes de GUI gebruikt, beslist de
// server (internal/truenas).
const TrueNASSince = 53

// TrueNASOpGet is midclt call <Method> <Args...> met een leesmethode, over
// het onderwerp pve (PVERequest). De agent weigert elke methode die iets
// kan veranderen (TrueNASReadMethod).
const TrueNASOpGet = "truenas_get"

// TrueNASCall is midclt call met een willekeurige methode, als run pve;
// een taak (job) van de middleware wacht de agent af.
const TrueNASCall = "truenas_call"

func init() {
	PVEActions = append(PVEActions, TrueNASCall)
}

// TrueNASFacts staat in de facts van een agent op TrueNAS SCALE.
type TrueNASFacts struct {
	// Version zoals 25.04.2 (uit /etc/version).
	Version  string `json:"version"`
	Hostname string `json:"hostname"`
	// Pools zijn de ZFS-pools buiten boot-pool, met hun toestand (ONLINE,
	// DEGRADED, …).
	Pools []TrueNASPool `json:"pools,omitempty"`
	// Persistent: de agent staat in /data/clusterforge en start na een
	// update vanzelf (het POSTINIT-script bestaat).
	Persistent bool `json:"persistent"`
}

type TrueNASPool struct {
	Name   string `json:"name"`
	Health string `json:"health"`
}

// TrueNASMethodCall is één aanroep van de middleware.
type TrueNASMethodCall struct {
	Method string `json:"method"`
	// Args is een JSON-lijst; elk element wordt één argument van midclt.
	Args json.RawMessage `json:"args,omitempty"`
	// Job: de methode is een taak; de agent wacht op het einde (-job).
	Job bool `json:"job,omitempty"`
	// Secret: de argumenten bevatten een geheim en komen niet in het log.
	Secret bool `json:"secret,omitempty"`
}

var trueNASMethod = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,3}$`)

// trueNASReadSuffix zijn de laatste delen van een methode die alleen lezen.
var trueNASReadSuffix = []string{"query", "get_instance", "config", "info", "choices"}

// trueNASReadOnly zijn andere methodes die alleen lezen.
var trueNASReadOnly = map[string]bool{
	"system.info": true, "system.version": true, "system.ready": true, "system.state": true,
	"system.host_id": true, "system.boot_id": true, "system.product_type": true,
	"disk.temperatures": true, "disk.temperature_agg": true, "disk.get_unused": true,
	"alert.list": true, "alert.list_categories": true,
	"pool.dataset.details": true, "pool.dataset.processes": true,
	"pool.scrub.query": true, "pool.get_disks": true,
	"update.check_available": true, "update.get_pending": true, "update.get_trains": true, "update.status": true,
	"smart.test.results": true, "smart.test.disk_choices": true,
	"app.available": true, "app.latest": true, "app.upgrade_summary": true,
	"service.started": true, "interface.query": true, "reporting.netdata_get_data": true,
	"boot.get_state": true, "boot.get_disks": true, "core.get_jobs": true,
	"replication.list_datasets": true, "zfs.snapshot.query": true,
}

// TrueNASReadMethod is true voor een methode die niets verandert.
func TrueNASReadMethod(m string) bool {
	if !trueNASMethod.MatchString(m) {
		return false
	}
	if trueNASReadOnly[m] {
		return true
	}
	i := strings.LastIndexByte(m, '.')
	for _, s := range trueNASReadSuffix {
		if m[i+1:] == s {
			return true
		}
	}
	return false
}

func validTrueNASArgs(a json.RawMessage) error {
	if len(a) == 0 {
		return nil
	}
	if len(a) > 256<<10 {
		return fmt.Errorf("argumenten te groot")
	}
	var list []json.RawMessage
	if err := json.Unmarshal(a, &list); err != nil {
		return fmt.Errorf("argumenten zijn geen JSON-lijst")
	}
	if len(list) > 8 {
		return fmt.Errorf("te veel argumenten")
	}
	return nil
}

// Validate controleert de vorm van een aanroep.
func (c *TrueNASMethodCall) Validate() error {
	if c == nil {
		return fmt.Errorf("truenas_call zonder aanroep")
	}
	if !trueNASMethod.MatchString(c.Method) {
		return fmt.Errorf("ongeldige methode %q", c.Method)
	}
	return validTrueNASArgs(c.Args)
}

// ValidateTrueNASGet controleert een leesverzoek: Path is de methode,
// Args de argumenten.
func (r PVERequest) ValidateTrueNASGet() error {
	if !TrueNASReadMethod(r.Path) {
		return fmt.Errorf("%q is geen leesmethode van TrueNAS", r.Path)
	}
	return validTrueNASArgs(r.Args)
}
