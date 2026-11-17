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
	for _, p := range []string{"keychaincredential", "cloudsync.credentials", "api_key", "auth", "user", "group", "ssh", "kmip", "certificate", "certificateauthority", "acme", "directoryservices", "activedirectory", "ldap", "mail", "system.general.config", "tunable"} {
		if method == p || strings.HasPrefix(method, p+".") {
			return true
		}
	}
	return false
}
