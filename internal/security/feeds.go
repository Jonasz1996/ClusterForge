package security

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// De feeds. Ze komen van de server zelf (uitgaand HTTPS); de nodes hebben
// er niets mee te maken.
const (
	FeedDebian = "debian"
	FeedKEV    = "kev"

	DefaultDebianURL = "https://security-tracker.debian.org/tracker/data/json"
	DefaultKEVURL    = "https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json"
)

// DefaultReleases zijn de Debian-releases die altijd ingelezen worden,
// ook als nog geen node ze meldt.
var DefaultReleases = []string{"bookworm", "trixie"}

// DebianEntry is één regel van de Security Tracker: een CVE in een
// bronpakket in een release.
type DebianEntry struct {
	Release      string
	Package      string
	CVE          string
	Status       string // resolved, open, undetermined
	FixedVersion string
	Urgency      string
}

// CVEInfo is de beschrijving van een CVE.
type CVEInfo struct {
	CVE         string
	Description string
	Scope       string
}

type debianCVE struct {
	Description string `json:"description"`
	Scope       string `json:"scope"`
	Releases    map[string]struct {
		Status       string `json:"status"`
		FixedVersion string `json:"fixed_version"`
		Urgency      string `json:"urgency"`
	} `json:"releases"`
}

// ParseDebian leest de JSON van de Security Tracker pakket per pakket (het
// bestand is zo'n 70 MB). Alleen de gevraagde releases tellen. Weggelaten:
// "niet getroffen" (opgelost in versie 0) en urgentie unimportant, want
// Debian zelf noemt die geen beveiligingsprobleem.
func ParseDebian(r io.Reader, releases []string, entry func(DebianEntry) error, cve func(CVEInfo) error) error {
	want := map[string]bool{}
	for _, rel := range releases {
		want[rel] = true
	}
	dec := json.NewDecoder(r)
	if err := expectDelim(dec, '{'); err != nil {
		return err
	}
	seen := map[string]bool{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		pkg, ok := t.(string)
		if !ok {
			return errors.New("Security Tracker: onverwachte inhoud")
		}
		var cves map[string]debianCVE
		if err := dec.Decode(&cves); err != nil {
			return fmt.Errorf("Security Tracker, pakket %s: %w", pkg, err)
		}
		for id, c := range cves {
			if !strings.HasPrefix(id, "CVE-") {
				continue // TEMP-… zonder CVE-nummer
			}
			used := false
			for rel, st := range c.Releases {
				if !want[rel] {
					continue
				}
				if st.Urgency == "unimportant" || (st.Status == "resolved" && (st.FixedVersion == "0" || st.FixedVersion == "")) {
					continue
				}
				used = true
				if err := entry(DebianEntry{Release: rel, Package: pkg, CVE: id, Status: st.Status,
					FixedVersion: st.FixedVersion, Urgency: st.Urgency}); err != nil {
					return err
				}
			}
			if used && !seen[id] {
				seen[id] = true
				if err := cve(CVEInfo{CVE: id, Description: trimText(c.Description, 400), Scope: c.Scope}); err != nil {
					return err
				}
			}
		}
	}
	return expectDelim(dec, '}')
}

func expectDelim(dec *json.Decoder, d json.Delim) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if t != d {
		return fmt.Errorf("onverwachte JSON: %v in plaats van %v", t, d)
	}
	return nil
}

// KEVEntry is een kwetsbaarheid uit de CISA-catalogus.
type KEVEntry struct {
	CVE        string
	Vendor     string
	Product    string
	Name       string
	DateAdded  *time.Time
	Ransomware bool
}

// ParseKEV leest de catalogus van CISA.
func ParseKEV(r io.Reader) ([]KEVEntry, error) {
	var doc struct {
		Vulnerabilities []struct {
			CVE        string `json:"cveID"`
			Vendor     string `json:"vendorProject"`
			Product    string `json:"product"`
			Name       string `json:"vulnerabilityName"`
			DateAdded  string `json:"dateAdded"`
			Ransomware string `json:"knownRansomwareCampaignUse"`
		} `json:"vulnerabilities"`
	}
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("CISA KEV: %w", err)
	}
	if len(doc.Vulnerabilities) == 0 {
		return nil, errors.New("CISA KEV: lege catalogus")
	}
	out := make([]KEVEntry, 0, len(doc.Vulnerabilities))
	for _, v := range doc.Vulnerabilities {
		if !strings.HasPrefix(v.CVE, "CVE-") {
			continue
		}
		e := KEVEntry{CVE: v.CVE, Vendor: v.Vendor, Product: v.Product, Name: trimText(v.Name, 200),
			Ransomware: strings.EqualFold(v.Ransomware, "Known")}
		if d, err := time.Parse("2006-01-02", v.DateAdded); err == nil {
			e.DateAdded = &d
		}
		out = append(out, e)
	}
	return out, nil
}

func trimText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	if i := strings.LastIndexByte(cut, ' '); i > n/2 {
		cut = cut[:i]
	}
	return cut + "…"
}
