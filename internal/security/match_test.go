package security

import (
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestMatchPackages(t *testing.T) {
	cat := Catalog{
		Debian: map[string][]DebianEntry{
			"openssl": {
				{Package: "openssl", CVE: "CVE-1", Status: "resolved", FixedVersion: "3.5.1-1+deb13u2", Urgency: "high"},
				{Package: "openssl", CVE: "CVE-2", Status: "resolved", FixedVersion: "3.5.1-1+deb13u1", Urgency: "medium"},
				{Package: "openssl", CVE: "CVE-3", Status: "open", Urgency: "low"},
			},
			"bash":     {{Package: "bash", CVE: "CVE-4", Status: "resolved", FixedVersion: "5.2.37-1", Urgency: "high"}},
			"xz-utils": {{Package: "xz-utils", CVE: "CVE-2024-3094", Status: "resolved", FixedVersion: "5.6.1+really5.4.5-1", Urgency: "not yet assigned"}},
		},
		KEV: map[string]KEVEntry{"CVE-2024-3094": {CVE: "CVE-2024-3094"}},
	}
	pkgs := []protocol.DebPackage{
		{Name: "openssl", Source: "openssl", SourceVersion: "3.5.1-1+deb13u1"},
		{Name: "libssl3t64", Source: "openssl", SourceVersion: "3.5.1-1+deb13u1"},
		{Name: "bash", Source: "bash", SourceVersion: "5.2.37-2"},
		{Name: "xz-utils", Source: "xz-utils", SourceVersion: "5.6.0-0.2"},
	}
	ds := MatchPackages("trixie", pkgs, cat)
	if len(ds) != 2 {
		t.Fatalf("bevindingen = %+v", ds)
	}
	ssl := ds[0].Detail.(CVEDetail)
	if ds[0].Key != "openssl" || ssl.Fixable != 1 || ssl.NoFix != 1 || ssl.FixVersion != "3.5.1-1+deb13u2" || ds[0].Severity != SevHigh {
		t.Errorf("openssl = %+v %+v", ds[0], ssl)
	}
	if len(ssl.Binaries) != 2 {
		t.Errorf("binaries = %v", ssl.Binaries)
	}
	if ds[1].Key != "xz-utils" || ds[1].Severity != SevCritical || !ds[1].KEV {
		t.Errorf("xz = %+v", ds[1])
	}
}
