package templates

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// postfixContext is een relaycluster van twee nodes (testlijst 3, N30).
func postfixContext(t *testing.T, input map[string]any) (*Template, Context) {
	t.Helper()
	tpl, ok := Latest("postfix-relay")
	if !ok {
		t.Fatal("geen template postfix-relay")
	}
	in := map[string]any{"vip": "192.0.2.60", "vrid": 62, "auth_pass": "geheim12", "relayhost": "smtp.example.org",
		"relay_user": "relay@example.org", "relay_password": "api-sleutel_123", "sender": "servers@example.org",
		"mynetworks": "192.0.2.0/24,198.51.100.0/24"}
	for k, v := range input {
		in[k] = v
	}
	params, err := tpl.Validate(in)
	if err != nil {
		t.Fatal(err)
	}
	c := Context{Params: params, Cluster: ClusterInfo{Name: "Mailrelay", Slug: "mailrelay", Environment: "prod"}}
	for i := 1; i <= 2; i++ {
		c.Nodes = append(c.Nodes, NodeInfo{Hostname: fmt.Sprintf("relay-%02d", i), Role: "relay", Index: i,
			Address: fmt.Sprintf("192.0.2.%d", 60+i), Prefix: 24, Interface: "eth0"})
	}
	c.Node = &c.Nodes[0]
	return tpl, c
}

func postfixFiles(t *testing.T, tpl *Template, c Context) map[string]string {
	t.Helper()
	steps, err := tpl.Steps("relay", c)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, s := range steps {
		if s.File != nil {
			files[s.File.Path] = s.File.Content
		}
	}
	return files
}

func TestRenderPostfixRelay(t *testing.T) {
	tpl, c := postfixContext(t, nil)
	if c.Params["relay_port"] != 587 || c.Params["relay_tls"] != true || c.Params["queue_warn"] != 20 || c.Params["memory"] != "512M" {
		t.Fatalf("standaarden: %+v", c.Params)
	}
	files := postfixFiles(t, tpl, c)
	for path, want := range map[string][]string{
		"/etc/postfix/main.cf": {"mynetworks = 127.0.0.0/8, 192.0.2.0/24,198.51.100.0/24", "relayhost = [smtp.example.org]:587",
			"smtp_tls_security_level = encrypt", "smtp_tls_wrappermode = no", "smtp_sasl_auth_enable = yes",
			"smtpd_relay_restrictions = permit_mynetworks, reject_unauth_destination", "mydestination =\n",
			"sender_canonical_maps = regexp:/etc/postfix/sender_canonical", "message_size_limit = 26214400", "myhostname = relay-01"},
		"/etc/cf-postfix/relay.conf":                 {"RELAY_PASSWORD='api-sleutel_123'", "SENDER='servers@example.org'", "VIP='192.0.2.60'"},
		"/etc/keepalived/keepalived.conf":            {"virtual_router_id 62", "192.0.2.60/24 dev eth0", "postfix-ctl.sh check", "192.0.2.62"},
		"/usr/local/lib/clusterforge/postfix-ctl.sh": {"(cluster mailrelay)", "testmail-if-vip"},
	} {
		got, ok := files[path]
		if !ok {
			t.Errorf("%s ontbreekt", path)
			continue
		}
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s mist %q:\n%s", path, w, got)
			}
		}
	}
	if strings.Contains(files["/etc/postfix/main.cf"], "0.0.0.0/0") {
		t.Error("open relay in mynetworks")
	}

	// Poort 465 zonder gebruiker: wrappermode, geen SASL.
	_, c2 := postfixContext(t, map[string]any{"relay_port": 465, "relay_user": ""})
	main2 := postfixFiles(t, tpl, c2)["/etc/postfix/main.cf"]
	for _, w := range []string{"smtp_tls_wrappermode = yes", "smtp_sasl_auth_enable = no", "relayhost = [smtp.example.org]:465"} {
		if !strings.Contains(main2, w) {
			t.Errorf("465 mist %q", w)
		}
	}

	// Nooit 0.0.0.0/0 of rommel in mynetworks.
	for _, bad := range []string{"0.0.0.0/0", "10.0.0.0/4", "192.168.1.0/24; rm -rf /", "alle"} {
		if _, err := tpl.Validate(map[string]any{"vip": "192.0.2.60", "relayhost": "smtp.example.org", "sender": "a@example.org", "mynetworks": bad}); err == nil {
			t.Errorf("mynetworks %q aanvaard", bad)
		}
	}
}

// Het script: kaarten, master.cf en de relaytest met een nep-Postfix.
func TestPostfixCtl(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("geen bash")
	}
	tpl, c := postfixContext(t, map[string]any{"sender_name": "Servers thuis"})
	files := postfixFiles(t, tpl, c)
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	pf := filepath.Join(dir, "postfix")
	for _, d := range []string{bin, pf} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ctl := filepath.Join(dir, "postfix-ctl.sh")
	conf := filepath.Join(dir, "relay.conf")
	write := func(p, s string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(ctl, files["/usr/local/lib/clusterforge/postfix-ctl.sh"], 0o755)
	write(conf, files["/etc/cf-postfix/relay.conf"], 0o600)
	write(filepath.Join(pf, "main.cf"), files["/etc/postfix/main.cf"], 0o644)
	log := filepath.Join(dir, "calls")
	// postconf -M bewaart de diensten in een bestand.
	write(filepath.Join(bin, "postconf"), `#!/bin/bash
echo "postconf $*" >>`+log+`
M=`+dir+`/master
touch $M
case "$1" in
  -MX) grep -v "^${2%/inet} " $M >$M.n; mv $M.n $M ;;
  -M)
    if [ $# -eq 1 ]; then cat $M; exit 0; fi
    case "$2" in
      *=*) svc=${2%%=*}; grep -v "^${svc%/inet} " $M >$M.n; mv $M.n $M; echo "${2#*=}" >>$M ;;
      *) grep "^${2%/inet} " $M ;;
    esac ;;
esac
`, 0o755)
	for _, b := range []string{"postmap", "systemctl", "sysctl", "chmod"} {
		write(filepath.Join(bin, b), "#!/bin/sh\necho \""+b+" $*\" >>"+log+"\n", 0o755)
	}
	write(filepath.Join(bin, "postfix"), "#!/bin/sh\nexit 0\n", 0o755)
	write(filepath.Join(bin, "ip"), "#!/bin/sh\necho '2: eth0    inet 192.0.2.61/24'\n", 0o755)
	write(filepath.Join(dir, "master"), "smtp inet n - y - - smtpd\n", 0o644)
	env := append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "CF_PF_CONF="+conf, "CF_PF_STATE="+filepath.Join(dir, "state"), "CF_PF_DIR="+pf)
	run := func(args ...string) (string, error) {
		cmd := exec.Command("bash", append([]string{ctl}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if _, err := run("configured"); err == nil {
		t.Fatal("configured zonder configure")
	}
	if out, err := run("configure"); err != nil {
		t.Fatalf("configure: %v\n%s", err, out)
	}
	if out, err := run("configured"); err != nil {
		t.Fatalf("configured na configure: %v\n%s", err, out)
	}
	master, _ := os.ReadFile(filepath.Join(dir, "master"))
	if strings.Contains(string(master), "smtp inet n - y") || !strings.Contains(string(master), "127.0.0.1:smtp inet") ||
		!strings.Contains(string(master), "192.0.2.60:smtp inet") {
		t.Errorf("master.cf:\n%s", master)
	}
	sasl, _ := os.ReadFile(filepath.Join(pf, "sasl_passwd"))
	if string(sasl) != "[smtp.example.org]:587 relay@example.org:api-sleutel_123\n" {
		t.Errorf("sasl_passwd: %q", sasl)
	}
	hc, _ := os.ReadFile(filepath.Join(pf, "header_checks"))
	if string(hc) != "/^From:.*/ REPLACE From: Servers thuis <servers@example.org>\n" {
		t.Errorf("header_checks: %q", hc)
	}
	sc, _ := os.ReadFile(filepath.Join(pf, "sender_canonical"))
	if string(sc) != "/.+/ servers@example.org\n" {
		t.Errorf("sender_canonical: %q", sc)
	}
	// Een ander wachtwoord: niet meer ingesteld.
	write(conf, strings.Replace(files["/etc/cf-postfix/relay.conf"], "api-sleutel_123", "ander-wachtwoord", 1), 0o600)
	if _, err := run("configured"); err == nil {
		t.Error("configured na een ander wachtwoord")
	}
}
