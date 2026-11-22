package truenas

// Welke methodes van de TrueNAS-middleware ClusterForge aanroept (fase 37).
// De agent voert elke goed gevormde aanroep uit; hier staat wat de GUI mag:
// de methode, de vorm van de argumenten, de titel van de taak, of het een
// job is en wat een overgetikte bevestiging vraagt.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	poolName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)
	alertID  = regexp.MustCompile(`^[A-Za-z0-9_.:;=-]{1,200}$`)
	diskName = regexp.MustCompile(`^[A-Za-z0-9{}_.:-]{1,128}$`)
)

type rule struct {
	// args controleert de argumenten en geeft titel en bevestiging.
	args func(a []json.RawMessage) (title, confirm string, err error)
	job  bool
}

func str(a []json.RawMessage, i int) (string, error) {
	if i >= len(a) {
		return "", fmt.Errorf("argument %d ontbreekt", i+1)
	}
	var s string
	if err := json.Unmarshal(a[i], &s); err != nil {
		return "", fmt.Errorf("argument %d is geen tekst", i+1)
	}
	return s, nil
}

func count(a []json.RawMessage, n int) error {
	if len(a) != n {
		return fmt.Errorf("%d argumenten verwacht, %d gekregen", n, len(a))
	}
	return nil
}

var scrubWord = map[string]string{"START": "starten", "STOP": "stoppen", "PAUSE": "pauzeren"}

var rules = map[string]rule{
	// --- Pools --------------------------------------------------------
	"pool.scrub.scrub": {job: true, args: func(a []json.RawMessage) (string, string, error) {
		if err := count(a, 2); err != nil {
			return "", "", err
		}
		p, err := str(a, 0)
		if err != nil || !poolName.MatchString(p) {
			return "", "", fmt.Errorf("ongeldige pool")
		}
		act, err := str(a, 1)
		if err != nil || scrubWord[act] == "" {
			return "", "", fmt.Errorf("actie is START, STOP of PAUSE")
		}
		return "scrub van " + p + " " + scrubWord[act], "", nil
	}},

	// --- Snapshots (M2) -----------------------------------------------
	"pool.snapshot.create":     {args: snapCreate},
	"zfs.snapshot.create":      {args: snapCreate},
	"pool.snapshot.delete":     {args: snapDelete},
	"zfs.snapshot.delete":      {args: snapDelete},
	"pool.snapshot.rollback":   {args: snapRollback},
	"zfs.snapshot.rollback":    {args: snapRollback},
	"pool.snapshottask.run":    {args: taskID("snapshot-taak %d nu uitvoeren")},
	"pool.snapshottask.update": {args: taskEnable("snapshot-taak")},
	"cloudsync.sync":           {job: true, args: taskID("cloud-sync-taak %d nu uitvoeren")},
	"cloudsync.update":         {args: taskEnable("cloud-sync-taak")},
	"rsynctask.run":            {job: true, args: taskID("rsync-taak %d nu uitvoeren")},
	"rsynctask.update":         {args: taskEnable("rsync-taak")},
	"replication.run":          {job: true, args: taskID("replicatie %d nu uitvoeren")},
	"replication.update":       {args: taskEnable("replicatie")},

	// --- Shares en diensten (M3) ---------------------------------------
	"sharing.smb.update": {args: taskEnable("SMB-share")},
	"sharing.nfs.update": {args: taskEnable("NFS-share")},
	"service.control":    {job: true, args: serviceControl},
	// Fase 43b: alleen de SNMP-community vervangen (pentest: "public").
	"snmp.update":    {args: snmpCommunity},
	"service.update": {args: serviceAutostart},

	// --- Updates en herstarten (M4) ------------------------------------
	"update.download": {job: true, args: func(a []json.RawMessage) (string, string, error) {
		if len(a) > 1 {
			return "", "", fmt.Errorf("te veel argumenten")
		}
		return "TrueNAS-update downloaden", "", nil
	}},
	"update.update": {job: true, args: updateApply},
	"update.run":    {job: true, args: updateApply},
	"system.reboot": {job: true, args: func(a []json.RawMessage) (string, string, error) {
		if len(a) > 2 {
			return "", "", fmt.Errorf("te veel argumenten")
		}
		if len(a) >= 1 {
			if r, err := str(a, 0); err != nil || len(r) > 200 {
				return "", "", fmt.Errorf("reden is een korte tekst")
			}
		}
		return "TrueNAS herstarten", "herstarten", nil
	}},

	// --- Meldingen ----------------------------------------------------
	"alert.dismiss": {args: func(a []json.RawMessage) (string, string, error) {
		if err := count(a, 1); err != nil {
			return "", "", err
		}
		if id, err := str(a, 0); err != nil || !alertID.MatchString(id) {
			return "", "", fmt.Errorf("ongeldige melding")
		}
		return "melding wegklikken", "", nil
	}},
	"alert.restore": {args: func(a []json.RawMessage) (string, string, error) {
		if err := count(a, 1); err != nil {
			return "", "", err
		}
		if id, err := str(a, 0); err != nil || !alertID.MatchString(id) {
			return "", "", fmt.Errorf("ongeldige melding")
		}
		return "melding terugzetten", "", nil
	}},

	// --- Schijven -----------------------------------------------------
	"smart.test.manual_test": {job: true, args: func(a []json.RawMessage) (string, string, error) {
		if err := count(a, 1); err != nil {
			return "", "", err
		}
		var list []struct {
			Identifier string `json:"identifier"`
			Type       string `json:"type"`
		}
		if err := json.Unmarshal(a[0], &list); err != nil || len(list) == 0 || len(list) > 64 {
			return "", "", fmt.Errorf("lijst met schijven verwacht")
		}
		names := []string{}
		for _, d := range list {
			if !diskName.MatchString(d.Identifier) {
				return "", "", fmt.Errorf("ongeldige schijf %q", d.Identifier)
			}
			if d.Type != "SHORT" && d.Type != "LONG" && d.Type != "CONVEYANCE" && d.Type != "OFFLINE" {
				return "", "", fmt.Errorf("soort test is SHORT, LONG, CONVEYANCE of OFFLINE")
			}
			names = append(names, d.Identifier)
		}
		return "SMART-test " + strings.ToLower(list[0].Type) + " op " + strings.Join(names, ", "), "", nil
	}},

	// --- Taken --------------------------------------------------------
	"core.job_abort": {args: func(a []json.RawMessage) (string, string, error) {
		if err := count(a, 1); err != nil {
			return "", "", err
		}
		var id int64
		if err := json.Unmarshal(a[0], &id); err != nil || id <= 0 {
			return "", "", fmt.Errorf("ongeldig taaknummer")
		}
		return fmt.Sprintf("TrueNAS-taak %d afbreken", id), "", nil
	}},
}

// Allowed is wat de server over een aanroep moet weten.
type Allowed struct {
	Title   string
	Confirm string
	Job     bool
}

// check zoekt de regel van een aanroep en controleert zijn argumenten.
func check(method string, args json.RawMessage) (Allowed, error) {
	x, ok := rules[method]
	if !ok {
		return Allowed{}, fmt.Errorf("ClusterForge roept %s niet aan", method)
	}
	var list []json.RawMessage
	if len(args) > 0 {
		if err := json.Unmarshal(args, &list); err != nil {
			return Allowed{}, fmt.Errorf("argumenten zijn geen JSON-lijst")
		}
	}
	title, confirm, err := x.args(list)
	if err != nil {
		return Allowed{}, fmt.Errorf("%s: %w", method, err)
	}
	return Allowed{Title: title, Confirm: confirm, Job: x.job}, nil
}

// readDenied zijn methodes die een kijker niet leest: sleutels,
// wachtwoorden en gebruikers.
func readDenied(method string) bool {
	for _, p := range []string{"keychaincredential", "cloudsync.credentials", "api_key", "auth", "user", "group", "ssh", "kmip", "certificate", "certificateauthority", "acme", "directoryservices", "activedirectory", "ldap", "mail", "system.general.config", "tunable", "snmp"} {
		if method == p || strings.HasPrefix(method, p+".") {
			return true
		}
	}
	return false
}

var (
	datasetName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/ -]{0,250}$`)
	snapName    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,120}$`)
)

func validDataset(d string) bool {
	return datasetName.MatchString(d) && !strings.Contains(d, "..") && !strings.Contains(d, "//") && !strings.HasSuffix(d, "/")
}

// splitSnap deelt pool/data@naam.
func splitSnap(id string) (string, string, bool) {
	ds, name, ok := strings.Cut(id, "@")
	return ds, name, ok && validDataset(ds) && snapName.MatchString(name)
}

func snapCreate(a []json.RawMessage) (string, string, error) {
	if err := count(a, 1); err != nil {
		return "", "", err
	}
	var in map[string]json.RawMessage
	if err := json.Unmarshal(a[0], &in); err != nil {
		return "", "", fmt.Errorf("object verwacht")
	}
	var ds, name string
	var rec bool
	for k, v := range in {
		switch k {
		case "dataset":
			_ = json.Unmarshal(v, &ds)
		case "name":
			_ = json.Unmarshal(v, &name)
		case "recursive":
			if json.Unmarshal(v, &rec) != nil {
				return "", "", fmt.Errorf("recursive is waar of onwaar")
			}
		default:
			return "", "", fmt.Errorf("veld %s mag niet", k)
		}
	}
	if !validDataset(ds) || !snapName.MatchString(name) {
		return "", "", fmt.Errorf("ongeldige dataset of naam")
	}
	t := "snapshot " + ds + "@" + name + " maken"
	if rec {
		t += " (met onderliggende datasets)"
	}
	return t, "", nil
}

func snapOpts(a []json.RawMessage, allowed ...string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(a) < 2 {
		return out, nil
	}
	var in map[string]json.RawMessage
	if err := json.Unmarshal(a[1], &in); err != nil {
		return nil, fmt.Errorf("opties zijn geen object")
	}
	for k, v := range in {
		ok := false
		for _, x := range allowed {
			ok = ok || x == k
		}
		var b bool
		if !ok || json.Unmarshal(v, &b) != nil {
			return nil, fmt.Errorf("optie %s mag niet", k)
		}
		out[k] = b
	}
	return out, nil
}

func snapDelete(a []json.RawMessage) (string, string, error) {
	if len(a) < 1 || len(a) > 2 {
		return "", "", fmt.Errorf("1 of 2 argumenten verwacht")
	}
	id, err := str(a, 0)
	if err != nil {
		return "", "", err
	}
	if _, _, ok := splitSnap(id); !ok {
		return "", "", fmt.Errorf("ongeldige snapshot")
	}
	o, err := snapOpts(a, "defer", "recursive")
	if err != nil {
		return "", "", err
	}
	if o["recursive"] {
		return "snapshot " + id + " verwijderen (ook onderliggend)", id, nil
	}
	return "snapshot " + id + " verwijderen", "", nil
}

// snapRollback gooit alles na de snapshot weg; daarom de naam van de
// dataset overtikken.
func snapRollback(a []json.RawMessage) (string, string, error) {
	if len(a) < 1 || len(a) > 2 {
		return "", "", fmt.Errorf("1 of 2 argumenten verwacht")
	}
	id, err := str(a, 0)
	if err != nil {
		return "", "", err
	}
	ds, _, ok := splitSnap(id)
	if !ok {
		return "", "", fmt.Errorf("ongeldige snapshot")
	}
	if _, err := snapOpts(a, "recursive", "recursive_clones", "force", "recursive_rollback"); err != nil {
		return "", "", err
	}
	return "dataset " + ds + " terugrollen naar " + id, ds, nil
}

func taskID(title string) func(a []json.RawMessage) (string, string, error) {
	return func(a []json.RawMessage) (string, string, error) {
		if err := count(a, 1); err != nil {
			return "", "", err
		}
		var id int64
		if err := json.Unmarshal(a[0], &id); err != nil || id <= 0 {
			return "", "", fmt.Errorf("ongeldig nummer")
		}
		return fmt.Sprintf(title, id), "", nil
	}
}

// taskEnable laat bij een update alleen enabled toe: aan- of uitzetten.
func taskEnable(what string) func(a []json.RawMessage) (string, string, error) {
	return func(a []json.RawMessage) (string, string, error) {
		if err := count(a, 2); err != nil {
			return "", "", err
		}
		var id int64
		if err := json.Unmarshal(a[0], &id); err != nil || id <= 0 {
			return "", "", fmt.Errorf("ongeldig nummer")
		}
		var in map[string]json.RawMessage
		if err := json.Unmarshal(a[1], &in); err != nil || len(in) != 1 {
			return "", "", fmt.Errorf("alleen enabled mag veranderen")
		}
		var on bool
		if v, ok := in["enabled"]; !ok || json.Unmarshal(v, &on) != nil {
			return "", "", fmt.Errorf("alleen enabled mag veranderen")
		}
		if on {
			return fmt.Sprintf("%s %d aanzetten", what, id), "", nil
		}
		return fmt.Sprintf("%s %d uitzetten", what, id), "", nil
	}
}

var (
	serviceName = regexp.MustCompile(`^[a-z][a-z0-9_]{1,30}$`)
	verbWord    = map[string]string{"START": "starten", "STOP": "stoppen", "RESTART": "herstarten", "RELOAD": "herladen"}
)

// serviceControl: service.control(verb, dienst, opties) van 24.10+.
// Stoppen van de middleware of ssh zou ClusterForge of Jonas buitensluiten;
// middlewared is daarom geen dienst die hier mag.
func serviceControl(a []json.RawMessage) (string, string, error) {
	if len(a) < 2 || len(a) > 3 {
		return "", "", fmt.Errorf("2 of 3 argumenten verwacht")
	}
	verb, err := str(a, 0)
	if err != nil || verbWord[verb] == "" {
		return "", "", fmt.Errorf("actie is START, STOP, RESTART of RELOAD")
	}
	svc, err := str(a, 1)
	if err != nil || !serviceName.MatchString(svc) || svc == "middlewared" {
		return "", "", fmt.Errorf("ongeldige dienst")
	}
	if len(a) == 3 {
		var o map[string]any
		if json.Unmarshal(a[2], &o) != nil {
			return "", "", fmt.Errorf("opties zijn geen object")
		}
		for k := range o {
			if k != "ha_propagate" && k != "silent" && k != "timeout" {
				return "", "", fmt.Errorf("optie %s mag niet", k)
			}
		}
	}
	confirm := ""
	if verb == "STOP" && svc == "ssh" {
		confirm = "ssh"
	}
	return "dienst " + svc + " " + verbWord[verb], confirm, nil
}

// serviceAutostart: service.update(id, {enable}) zet het automatisch
// starten aan of uit.
func serviceAutostart(a []json.RawMessage) (string, string, error) {
	if err := count(a, 2); err != nil {
		return "", "", err
	}
	var id any
	if json.Unmarshal(a[0], &id) != nil {
		return "", "", fmt.Errorf("ongeldige dienst")
	}
	switch v := id.(type) {
	case float64:
		if v <= 0 {
			return "", "", fmt.Errorf("ongeldige dienst")
		}
	case string:
		if !serviceName.MatchString(v) {
			return "", "", fmt.Errorf("ongeldige dienst")
		}
	default:
		return "", "", fmt.Errorf("ongeldige dienst")
	}
	var in map[string]json.RawMessage
	if err := json.Unmarshal(a[1], &in); err != nil || len(in) != 1 {
		return "", "", fmt.Errorf("alleen enable mag veranderen")
	}
	var on bool
	if v, ok := in["enable"]; !ok || json.Unmarshal(v, &on) != nil {
		return "", "", fmt.Errorf("alleen enable mag veranderen")
	}
	if on {
		return fmt.Sprintf("dienst %v automatisch laten starten", id), "", nil
	}
	return fmt.Sprintf("dienst %v niet meer automatisch starten", id), "", nil
}

// updateApply installeert de update; met reboot herstart TrueNAS meteen
// in de nieuwe boot-omgeving (de vorige blijft om terug te gaan).
func updateApply(a []json.RawMessage) (string, string, error) {
	if len(a) > 1 {
		return "", "", fmt.Errorf("te veel argumenten")
	}
	reboot := false
	if len(a) == 1 {
		var o map[string]json.RawMessage
		if json.Unmarshal(a[0], &o) != nil {
			return "", "", fmt.Errorf("opties zijn geen object")
		}
		for k, v := range o {
			var b bool
			if (k != "reboot" && k != "resume") || json.Unmarshal(v, &b) != nil {
				return "", "", fmt.Errorf("optie %s mag niet", k)
			}
			if k == "reboot" {
				reboot = b
			}
		}
	}
	if reboot {
		return "TrueNAS bijwerken en herstarten", "update", nil
	}
	return "TrueNAS-update installeren (actief na herstart)", "update", nil
}

var community = regexp.MustCompile(`^[A-Za-z0-9]{16,64}$`)

// snmpCommunity: snmp.update({community}) en niets anders.
func snmpCommunity(a []json.RawMessage) (string, string, error) {
	if err := count(a, 1); err != nil {
		return "", "", err
	}
	var in map[string]json.RawMessage
	if err := json.Unmarshal(a[0], &in); err != nil || len(in) != 1 {
		return "", "", fmt.Errorf("alleen community mag veranderen")
	}
	var c string
	if v, ok := in["community"]; !ok || json.Unmarshal(v, &c) != nil || !community.MatchString(c) {
		return "", "", fmt.Errorf("community moet 16 tot 64 letters of cijfers zijn")
	}
	return "SNMP-community vervangen", "", nil
}
