package protocol

import "time"

// AppsSince is de eerste protocolversie die de stand van apps meestuurt
// (fase 29): wat een ctl-script van een template, zoals platform-ctl.sh
// status, in /var/lib/clusterforge/app-status/<app>.json zet.
const AppsSince = 48

// AppStatusDir is de map waarin een template de stand van zijn app zet.
const AppStatusDir = "/var/lib/clusterforge/app-status"

// Grenzen voor wat de agent uit AppStatusDir leest.
const (
	MaxAppStatusFiles = 8
	MaxAppStatusBytes = 64 << 10
	MaxAppChecks      = 30
	MaxAppValues      = 30
)

// AppStatus is de stand van één app op een node.
type AppStatus struct {
	// App is de naam, zoals media-platform.
	App   string `json:"app"`
	Title string `json:"title,omitempty"`
	// Cluster is de slug van het cluster volgens het script.
	Cluster string `json:"cluster,omitempty"`
	// Version is de commit die op de node gebouwd is, Ref wat gevraagd is.
	Version string `json:"version,omitempty"`
	Ref     string `json:"ref,omitempty"`
	// Role: actief (deze node draait de app), stand-by of voor-overstap.
	Role      string     `json:"role,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
	Checks    []AppCheck `json:"checks,omitempty"`
	Values    []AppValue `json:"values,omitempty"`
}

// AppCheck is één controle. Severity is red of amber.
type AppCheck struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	OK       bool   `json:"ok"`
	Severity string `json:"severity,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// AppValue is een getal of tekst om te tonen; Bytes voor een grootte.
type AppValue struct {
	ID    string  `json:"id"`
	Label string  `json:"label"`
	Value string  `json:"value"`
	Bytes *uint64 `json:"bytes,omitempty"`
}

// StableApps houdt alleen app, versie en rol: een failover of nieuwe versie
// is een wijziging, schijfgebruik en controles niet.
func StableApps(in []AppStatus) []AppStatus {
	if len(in) == 0 {
		return nil
	}
	out := make([]AppStatus, len(in))
	for i, a := range in {
		out[i] = AppStatus{App: a.App, Title: a.Title, Cluster: a.Cluster, Version: a.Version, Ref: a.Ref, Role: a.Role}
	}
	return out
}
