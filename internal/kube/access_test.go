package kube

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestAccountName(t *testing.T) {
	id := uuid.MustParse("1a2b3c4d-0000-4000-8000-000000000000")
	for in, want := range map[string]string{
		"jonas":         "cf-jonas-1a2b3c",
		"Jonas.Bogaert": "cf-jonas-bogaert-1a2b3c",
		"__beheer__":    "cf-beheer-1a2b3c",
		"élodie":        "cf-lodie-1a2b3c",
		"!!!":           "cf-gebruiker-1a2b3c",
		"een-heel-lange-naam-die-nooit-ophoudt-en-maar-doorgaat": "cf-een-heel-lange-naam-die-nooit-ophoudt-en-1a2b3c",
	} {
		got := AccountName(in, id)
		if got != want || !protocol.KubeAccountPattern.MatchString(got) {
			t.Errorf("%q: %q, verwacht %q", in, got, want)
		}
	}
}

func TestValidateAccess(t *testing.T) {
	ok := AccessInput{Name: "laptop", Role: protocol.KubeAccessAdmin, TTL: 7 * 24 * time.Hour}
	if err := ValidateAccess(ok); err != nil {
		t.Fatal(err)
	}
	for _, in := range []AccessInput{
		{Name: "", Role: "admin", TTL: ok.TTL},
		{Name: "  ", Role: "admin", TTL: ok.TTL},
		{Name: "a\nb", Role: "admin", TTL: ok.TTL},
		{Name: "een naam die veel te lang is voor een kubeconfig", Role: "admin", TTL: ok.TTL},
		{Name: "laptop", Role: "edit", TTL: ok.TTL},
		{Name: "laptop", Role: "admin", TTL: time.Hour},
		{Name: "laptop", Role: "admin", TTL: 31 * 24 * time.Hour},
	} {
		if err := ValidateAccess(in); err == nil {
			t.Errorf("%+v: geen fout", in)
		}
	}
}

// De kubeconfig is geldige YAML, ook met een naam die in YAML iets
// betekent.
func TestKubeconfig(t *testing.T) {
	ca := []byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n")
	out := Kubeconfig("https://[fd00::10]:6443", ca, "leren", `jonas: "x"@leren`, "abc.def")
	var kc struct {
		Clusters []struct {
			Name    string `yaml:"name"`
			Cluster struct {
				Server string `yaml:"server"`
				CA     string `yaml:"certificate-authority-data"`
			} `yaml:"cluster"`
		} `yaml:"clusters"`
		Users []struct {
			Name string `yaml:"name"`
			User struct {
				Token string `yaml:"token"`
			} `yaml:"user"`
		} `yaml:"users"`
		Current string `yaml:"current-context"`
	}
	if err := yaml.Unmarshal(out, &kc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ca2, _ := base64.StdEncoding.DecodeString(kc.Clusters[0].Cluster.CA)
	if kc.Clusters[0].Cluster.Server != "https://[fd00::10]:6443" || string(ca2) != string(ca) || kc.Users[0].Name != `jonas: "x"@leren` ||
		kc.Users[0].User.Token != "abc.def" || kc.Current != "leren" {
		t.Fatalf("kubeconfig: %+v", kc)
	}
}
