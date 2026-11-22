package security

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Ernst, van hoog naar laag.
const (
	SevCritical = "kritiek"
	SevHigh     = "hoog"
	SevMedium   = "middel"
	SevLow      = "laag"
	SevInfo     = "info"
)

var sevRank = map[string]int{SevInfo: 0, SevLow: 1, SevMedium: 2, SevHigh: 3, SevCritical: 4}

// SeverityRank geeft de rang van een ernst (hoger is erger).
func SeverityRank(s string) int { return sevRank[s] }

// Soorten bevinding.
const (
	KindCVE = "cve"
)

// CVEHit is één kwetsbaarheid in een pakket op een node.
type CVEHit struct {
	CVE          string `json:"cve"`
	Urgency      string `json:"urgency"`
	Status       string `json:"status"`
	FixedVersion string `json:"fixed_version"`
	Fixable      bool   `json:"fixable"`
	KEV          bool   `json:"kev"`
	Ransomware   bool   `json:"ransomware"`
	Description  string `json:"description"`
	Scope        string `json:"scope"`
	Severity     string `json:"severity"`
}

// CVEDetail is de inhoud van een cve-bevinding: één bronpakket op één node.
type CVEDetail struct {
	Package  string   `json:"package"`
	Version  string   `json:"version"`
	Release  string   `json:"release"`
	Binaries []string `json:"binaries"`
	// FixVersion is de hoogste versie die alle oplosbare CVE's dicht.
	FixVersion string   `json:"fix_version"`
	Fixable    int      `json:"fixable"`
	NoFix      int      `json:"no_fix"`
	KEV        int      `json:"kev"`
	CVEs       []CVEHit `json:"cves"`
}

// Draft is een bevinding zoals een controle hem maakt, vóór de opslag.
type Draft struct {
	Kind     string
	Key      string
	Severity string
	Title    string
	Summary  string
	Fix      string
	Detail   any
	KEV      bool
}

// Catalog is wat de feeds weten, voor één release.
type Catalog struct {
	// Debian: bronpakket → regels.
	Debian map[string][]DebianEntry
	KEV    map[string]KEVEntry
	CVEs   map[string]CVEInfo
}

// MatchPackages stelt vast welke bekende kwetsbaarheden gelden voor de
// pakketten van een node. Eén bevinding per bronpakket.
func MatchPackages(release string, pkgs []protocol.DebPackage, cat Catalog) []Draft {
	type src struct {
		version  string
		binaries []string
	}
	sources := map[string]*src{}
	for _, p := range pkgs {
		s := sources[p.Source]
		if s == nil {
			s = &src{version: p.SourceVersion}
			sources[p.Source] = s
		} else if CompareDeb(p.SourceVersion, s.version) > 0 {
			// Meerdere versies (zoals oude kernels): de nieuwste telt.
			s.version = p.SourceVersion
		}
		if !slices.Contains(s.binaries, p.Name) {
			s.binaries = append(s.binaries, p.Name)
		}
	}
	names := make([]string, 0, len(sources))
	for n := range sources {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []Draft
	for _, name := range names {
		s := sources[name]
		var hits []CVEHit
		fix := ""
		for _, e := range cat.Debian[name] {
			h := CVEHit{CVE: e.CVE, Urgency: e.Urgency, Status: e.Status, FixedVersion: e.FixedVersion}
			switch e.Status {
			case "resolved":
				if CompareDeb(s.version, e.FixedVersion) >= 0 {
					continue
				}
				h.Fixable = true
				if fix == "" || CompareDeb(e.FixedVersion, fix) > 0 {
					fix = e.FixedVersion
				}
			case "open", "undetermined":
			default:
				continue
			}
			if k, ok := cat.KEV[e.CVE]; ok {
				h.KEV, h.Ransomware = true, k.Ransomware
			}
			if info, ok := cat.CVEs[e.CVE]; ok {
				h.Description, h.Scope = info.Description, info.Scope
			}
			h.Severity = hitSeverity(h)
			hits = append(hits, h)
		}
		if len(hits) == 0 {
			continue
		}
		sort.Slice(hits, func(i, j int) bool {
			if a, b := sevRank[hits[i].Severity], sevRank[hits[j].Severity]; a != b {
				return a > b
			}
			return hits[i].CVE > hits[j].CVE
		})
		d := CVEDetail{Package: name, Version: s.version, Release: release, Binaries: s.binaries, FixVersion: fix, CVEs: hits}
		sev := SevInfo
		for _, h := range hits {
			if h.Fixable {
				d.Fixable++
			} else {
				d.NoFix++
			}
			if h.KEV {
				d.KEV++
			}
			if sevRank[h.Severity] > sevRank[sev] {
				sev = h.Severity
			}
		}
		out = append(out, Draft{Kind: KindCVE, Key: name, Severity: sev, Title: cveTitle(d), Summary: cveSummary(d),
			Fix: cveFix(d), Detail: d, KEV: d.KEV > 0})
	}
	return out
}

func hitSeverity(h CVEHit) string {
	if h.KEV {
		return SevCritical
	}
	switch h.Urgency {
	case "high":
		return SevHigh
	case "medium", "end-of-life":
		return SevMedium
	case "low":
		return SevLow
	}
	// Nog niet ingeschat: van op afstand uitbuitbaar telt zwaarder.
	if h.Scope == "remote" && h.Fixable {
		return SevMedium
	}
	return SevLow
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func cveTitle(d CVEDetail) string {
	t := fmt.Sprintf("%s %s: %s", d.Package, d.Version, plural(len(d.CVEs), "kwetsbaarheid", "kwetsbaarheden"))
	if d.KEV > 0 {
		t += fmt.Sprintf(", waarvan %d actief misbruikt", d.KEV)
	}
	return t
}

func cveSummary(d CVEDetail) string {
	var parts []string
	if d.Fixable > 0 {
		parts = append(parts, plural(d.Fixable, "met een fix", "met een fix"))
	}
	if d.NoFix > 0 {
		parts = append(parts, plural(d.NoFix, "nog zonder fix in Debian", "nog zonder fix in Debian"))
	}
	ids := []string{}
	for i, h := range d.CVEs {
		if i == 3 {
			ids = append(ids, "…")
			break
		}
		ids = append(ids, h.CVE)
	}
	return strings.Join(parts, ", ") + " (" + strings.Join(ids, ", ") + ")"
}

func cveFix(d CVEDetail) string {
	if d.FixVersion == "" {
		return "Debian heeft nog geen fix. Beperk de blootstelling (firewall, dienst niet op alle adressen) en kijk na een volgende controle opnieuw."
	}
	f := fmt.Sprintf("Werk %s bij naar %s of nieuwer (Updates → beveiligingsupdates, of apt-get install --only-upgrade %s).",
		d.Package, d.FixVersion, strings.Join(d.Binaries, " "))
	if d.Package == "linux" || strings.HasPrefix(d.Package, "linux-signed") || strings.HasPrefix(d.Package, "proxmox-kernel") {
		f += " Herstart daarna de node, anders blijft de oude kernel draaien."
	}
	return f
}
