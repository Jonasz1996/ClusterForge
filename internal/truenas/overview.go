package truenas

// Het overzicht van een TrueNAS (fase 37 M1): de machine, pools met
// gebruik en scrub, schijven met temperatuur, meldingen, lopende en
// mislukte taken, en wat aandacht vraagt. Elk deel wordt apart gelezen;
// wat niet lukt (een methode die een versie niet kent), komt in Errors.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Overview is wat de pagina TrueNAS toont. De delen zijn de antwoorden van
// de middleware zoals ze zijn; de webinterface typt ze in web/src/lib/truenas.ts.
type Overview struct {
	Server   Server                     `json:"server"`
	Parts    map[string]json.RawMessage `json:"parts"`
	Warnings []Warning                  `json:"warnings"`
	Errors   map[string]string          `json:"errors"`
}

// Warning is iets dat aandacht vraagt.
type Warning struct {
	Level string `json:"level"` // critical, warning
	Text  string `json:"text"`
}

type part struct {
	method string
	args   string
	// alt is een tweede poging zonder extra opties (oudere versies).
	alt string
}

var overviewParts = map[string]part{
	"system":  {method: "system.info"},
	"pools":   {method: "pool.query"},
	"disks":   {method: "disk.query", args: `[[],{"extra":{"pools":true}}]`, alt: `[]`},
	"temps":   {method: "disk.temperatures"},
	"alerts":  {method: "alert.list"},
	"running": {method: "core.get_jobs", args: `[[["state","=","RUNNING"]]]`},
	"failed":  {method: "core.get_jobs", args: `[[["state","=","FAILED"]],{"order_by":["-id"],"limit":15}]`},
	"smart":   {method: "smart.test.results", args: `[[],{"limit":200}]`, alt: `[]`},
	"boot":    {method: "boot.get_state"},
	// Taken met hun laatste uitkomst (M4: wat mislukte, vraagt aandacht).
	"snaptasks": {method: "pool.snapshottask.query"},
	"cloudsync": {method: "cloudsync.query"},
	"repl":      {method: "replication.query"},
}

// Overview leest alle delen tegelijk.
func (s *Service) Overview(ctx context.Context, id uuid.UUID) (Overview, error) {
	v, err := s.Server(ctx, id)
	if err != nil {
		return Overview{}, err
	}
	o := Overview{Server: v, Parts: map[string]json.RawMessage{}, Errors: map[string]string{}, Warnings: []Warning{}}
	if v.Why != "" {
		return o, ValidationError{v.Why}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, p := range overviewParts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, err := s.get(ctx, id, protocol.PVERequest{Op: protocol.TrueNASOpGet, Path: p.method, Args: json.RawMessage(p.args)})
			if err != nil && p.alt != "" {
				data, err = s.get(ctx, id, protocol.PVERequest{Op: protocol.TrueNASOpGet, Path: p.method, Args: json.RawMessage(p.alt)})
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				o.Errors[name] = err.Error()
				return
			}
			o.Parts[name] = data
		}()
	}
	wg.Wait()
	o.Warnings = warnings(o.Parts)
	return o, nil
}

type poolInfo struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Healthy   bool   `json:"healthy"`
	Size      int64  `json:"size"`
	Allocated int64  `json:"allocated"`
	Scan      *struct {
		Function string `json:"function"`
		State    string `json:"state"`
		Errors   int64  `json:"errors"`
	} `json:"scan"`
}

type alertInfo struct {
	Level     string `json:"level"`
	Formatted string `json:"formatted"`
	Dismissed bool   `json:"dismissed"`
}

// warnings haalt uit de delen wat aandacht vraagt.
func warnings(parts map[string]json.RawMessage) []Warning {
	out := []Warning{}
	var pools []poolInfo
	_ = json.Unmarshal(parts["pools"], &pools)
	for _, p := range pools {
		if p.Status != "ONLINE" || !p.Healthy {
			out = append(out, Warning{"critical", fmt.Sprintf("pool %s is %s", p.Name, p.Status)})
		}
		if p.Size > 0 {
			pct := float64(p.Allocated) * 100 / float64(p.Size)
			switch {
			case pct >= 90:
				out = append(out, Warning{"critical", fmt.Sprintf("pool %s is %.0f%% vol", p.Name, pct)})
			case pct >= 80:
				out = append(out, Warning{"warning", fmt.Sprintf("pool %s is %.0f%% vol", p.Name, pct)})
			}
		}
		if p.Scan != nil && p.Scan.Errors > 0 {
			out = append(out, Warning{"critical", fmt.Sprintf("laatste %s van %s vond %d fouten", p.Scan.Function, p.Name, p.Scan.Errors)})
		}
	}
	temps := diskTemps(parts["temps"])
	names := make([]string, 0, len(temps))
	for n := range temps {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		switch t := temps[n]; {
		case t >= 55:
			out = append(out, Warning{"critical", fmt.Sprintf("schijf %s is %.0f °C", n, t)})
		case t >= 48:
			out = append(out, Warning{"warning", fmt.Sprintf("schijf %s is %.0f °C", n, t)})
		}
	}
	var alerts []alertInfo
	_ = json.Unmarshal(parts["alerts"], &alerts)
	for _, a := range alerts {
		if a.Dismissed {
			continue
		}
		switch a.Level {
		case "CRITICAL", "ALERT", "EMERGENCY", "ERROR":
			out = append(out, Warning{"critical", "TrueNAS: " + a.Formatted})
		}
	}
	out = append(out, taskWarnings(parts)...)
	var boot struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(parts["boot"], &boot) == nil && boot.Status != "" && boot.Status != "ONLINE" {
		out = append(out, Warning{"critical", "boot-pool is " + boot.Status})
	}
	return out
}

// diskTemps leest disk.temperatures: {"sda": 34} of, met drempels,
// {"sda": {"temperature": 34, ...}}.
func diskTemps(raw json.RawMessage) map[string]float64 {
	out := map[string]float64{}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return out
	}
	for k, v := range m {
		var f float64
		if json.Unmarshal(v, &f) == nil {
			out[k] = f
			continue
		}
		var o struct {
			Temperature *float64 `json:"temperature"`
		}
		if json.Unmarshal(v, &o) == nil && o.Temperature != nil {
			out[k] = *o.Temperature
		}
	}
	return out
}

type taskInfo struct {
	ID          int64  `json:"id"`
	Dataset     string `json:"dataset"`
	Description string `json:"description"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	State       *struct {
		State string `json:"state"`
		Error string `json:"error"`
	} `json:"state"`
	Job *struct {
		State string `json:"state"`
		Error string `json:"error"`
	} `json:"job"`
}

// taskWarnings meldt aangezette taken waarvan de laatste keer mislukte, en
// een TrueNAS zonder enige snapshot-taak.
func taskWarnings(parts map[string]json.RawMessage) []Warning {
	var out []Warning
	for _, k := range []struct{ part, what string }{{"snaptasks", "snapshot-taak"}, {"cloudsync", "cloud-sync"}, {"repl", "replicatie"}} {
		var list []taskInfo
		if json.Unmarshal(parts[k.part], &list) != nil {
			continue
		}
		if k.part == "snaptasks" && parts[k.part] != nil && len(list) == 0 {
			out = append(out, Warning{"warning", "geen enkele snapshot-taak: een verwijderd bestand is niet terug te halen"})
		}
		for _, t := range list {
			if !t.Enabled {
				continue
			}
			st, msg := "", ""
			if t.State != nil {
				st, msg = t.State.State, t.State.Error
			}
			if st == "" && t.Job != nil {
				st, msg = t.Job.State, t.Job.Error
			}
			if st != "ERROR" && st != "FAILED" {
				continue
			}
			name := t.Description
			if name == "" {
				name = t.Name
			}
			if name == "" {
				name = t.Dataset
			}
			text := fmt.Sprintf("%s %s mislukte", k.what, name)
			if msg != "" {
				if i := strings.IndexByte(msg, '\n'); i > 0 {
					msg = msg[:i]
				}
				text += ": " + msg
			}
			out = append(out, Warning{"critical", text})
		}
	}
	return out
}
