package crowdsec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// De lokale API mag niet blind op 8080 blijven: daar luistert vaak de
// ClusterForge-server of een container (storing 2026-11-17).
func TestScriptMovesLAPIOffTakenPort(t *testing.T) {
	s := Script(Plan{Hash: "h", Bouncer: true, Files: map[string]string{bouncerLocal: "x"}})
	for _, want := range []string{"== Poort van de lokale API", "HostPort", "seq 8081 8099", "BOUNCER_PORT_CHANGED", "INSTALL_RETRY=1", "dpkg --configure -a"} {
		if !strings.Contains(s, want) {
			t.Errorf("script mist %q", want)
		}
	}
	if strings.Index(s, "Poort van de lokale API") > strings.Index(s, "Configuratie testen") {
		t.Error("de poort moet vóór de configuratietest en de herstart goed staan")
	}
}

// De nginx-bouncer zonder lua-modules breekt nginx en daarmee elke apt-run
// (testronde 2026-11-22): het script zet zijn config opzij en werkt dpkg af.
func TestScriptNginxGuard(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("geen bash")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	conf := filepath.Join(root, "etc/nginx/conf.d")
	log := filepath.Join(root, "calls.log")
	for _, d := range []string{bin, conf} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fake := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/bash\necho \""+name+" $*\" >> "+log+"\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// nginx -t faalt zolang crowdsec_nginx.conf er staat, of als "kapot" bestaat.
	fake("nginx", `[ -e "`+conf+`/crowdsec_nginx.conf" ] && { echo 'unknown directive "access_by_lua_block"' >&2; exit 1; }; [ -e "`+root+`/kapot" ] && exit 1; exit 0`)
	fake("systemctl", "exit 0")
	fake("dpkg", "exit 0")
	run := func() string {
		script := "set -euo pipefail\n" + strings.ReplaceAll(nginxGuardScript, "/etc/nginx", root+"/etc/nginx") + "cf_nginx_guard\n"
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return string(out)
	}
	if err := os.WriteFile(filepath.Join(conf, "crowdsec_nginx.conf"), []byte("access_by_lua_block {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := run()
	if !strings.Contains(out, "lua-modules ontbreken") {
		t.Errorf("uitvoer: %s", out)
	}
	if _, err := os.Stat(filepath.Join(conf, "crowdsec_nginx.conf.cf-uit")); err != nil {
		t.Error("config niet opzij gezet")
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "dpkg --configure -a") || !strings.Contains(string(calls), "systemctl reload nginx") {
		t.Errorf("aanroepen: %s", calls)
	}

	// nginx breekt om iets anders: niets aanraken.
	if err := os.Rename(filepath.Join(conf, "crowdsec_nginx.conf.cf-uit"), filepath.Join(conf, "crowdsec_nginx.conf")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "kapot"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if out := run(); !strings.Contains(out, "niets gewijzigd") {
		t.Errorf("uitvoer: %s", out)
	}
	if _, err := os.Stat(filepath.Join(conf, "crowdsec_nginx.conf")); err != nil {
		t.Error("config moest terug op zijn plaats")
	}
	s := Script(Plan{Hash: "h"})
	if strings.Count(s, "\ncf_nginx_guard\n") != 2 || strings.Index(s, "cf_nginx_guard\n") > strings.Index(s, "apt-get") {
		t.Error("de nginx-controle moet vóór apt en na het toepassen lopen")
	}
}
