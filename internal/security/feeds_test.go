package security

import (
	"strings"
	"testing"
)

const debianSample = `{
 "openssl": {
  "CVE-2025-0001": {"description": "Een  fout\nin openssl", "scope": "remote", "releases": {
     "trixie": {"status": "resolved", "fixed_version": "3.5.1-1+deb13u1", "urgency": "high", "repositories": {"trixie": "3.5.1-1"}},
     "bookworm": {"status": "resolved", "fixed_version": "0", "urgency": "not yet assigned"},
     "sid": {"status": "resolved", "fixed_version": "3.5.2-1", "urgency": "high"}}},
  "CVE-2025-0002": {"description": "onbelangrijk", "releases": {"trixie": {"status": "open", "urgency": "unimportant"}}},
  "TEMP-0000000-ABCDEF": {"releases": {"trixie": {"status": "open", "urgency": "low"}}}
 },
 "bash": {
  "CVE-2025-0003": {"description": "nog open", "releases": {"trixie": {"status": "open", "urgency": "low"}, "bookworm": {"status": "undetermined", "urgency": "not yet assigned"}}}
 }
}`

func TestParseDebian(t *testing.T) {
	var got []DebianEntry
	var cves []CVEInfo
	err := ParseDebian(strings.NewReader(debianSample), []string{"bookworm", "trixie"},
		func(e DebianEntry) error { got = append(got, e); return nil },
		func(c CVEInfo) error { cves = append(cves, c); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("regels = %+v", got)
	}
	if len(cves) != 2 {
		t.Fatalf("cve's = %+v", cves)
	}
	for _, c := range cves {
		if c.CVE == "CVE-2025-0001" && c.Description != "Een fout in openssl" {
			t.Errorf("beschrijving = %q", c.Description)
		}
	}
}

func TestParseKEV(t *testing.T) {
	list, err := ParseKEV(strings.NewReader(`{"vulnerabilities":[{"cveID":"CVE-2024-3094","vendorProject":"XZ","product":"XZ Utils","vulnerabilityName":"XZ backdoor","dateAdded":"2024-04-01","knownRansomwareCampaignUse":"Unknown"},{"cveID":"CVE-2023-1","knownRansomwareCampaignUse":"Known"}]}`))
	if err != nil || len(list) != 2 {
		t.Fatalf("%v %+v", err, list)
	}
	if list[0].DateAdded == nil || list[0].Ransomware || !list[1].Ransomware {
		t.Errorf("%+v", list)
	}
	if _, err := ParseKEV(strings.NewReader(`{"vulnerabilities":[]}`)); err == nil {
		t.Error("lege catalogus moet een fout geven")
	}
}
