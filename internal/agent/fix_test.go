package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestFixCatalogComplete(t *testing.T) {
	for _, r := range protocol.FixableRules {
		_, ssh := fixSSH[r]
		if !ssh && fixSysctl[r] == nil && fixFiles[r].path == "" && !fixServices[r] {
			t.Errorf("regel %s staat in FixableRules maar de agent kent geen oplossing", r)
		}
	}
}

func TestMergeConf(t *testing.T) {
	got := mergeConf("", []kv{{"PermitRootLogin", "prohibit-password"}, {"X11Forwarding", "no"}}, false)
	if !strings.HasPrefix(got, "# Beheerd door ClusterForge") || !strings.Contains(got, "\nPermitRootLogin prohibit-password\nX11Forwarding no\n") {
		t.Fatalf("nieuw bestand: %q", got)
	}
	in := "# eigen\npermitrootlogin yes\nAllowUsers jonas\nPermitRootLogin no\n"
	got = mergeConf(in, []kv{{"PermitRootLogin", "prohibit-password"}}, false)
	if got != "# eigen\nPermitRootLogin prohibit-password\nAllowUsers jonas\n" {
		t.Fatalf("vervangen: %q", got)
	}
	got = mergeConf("net.ipv4.tcp_syncookies=0\n", []kv{{"net.ipv4.tcp_syncookies", "1"}, {"kernel.dmesg_restrict", "1"}}, true)
	if got != "net.ipv4.tcp_syncookies = 1\nkernel.dmesg_restrict = 1\n" {
		t.Fatalf("sysctl: %q", got)
	}
}

func TestDropWeak(t *testing.T) {
	kept, removed := dropWeak("chacha20-poly1305@openssh.com,aes128-cbc,aes256-gcm@openssh.com,3des-cbc", []string{"cbc", "3des"})
	if !slices.Equal(kept, []string{"chacha20-poly1305@openssh.com", "aes256-gcm@openssh.com"}) || len(removed) != 2 {
		t.Fatalf("kept %v removed %v", kept, removed)
	}
}

type fakeSys struct {
	calls  []string
	sshdT  string
	timer  string
	failOn string
}

func (f *fakeSys) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	c := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, c)
	if f.failOn != "" && strings.HasPrefix(c, f.failOn) {
		return []byte("kapot"), errors.New("exit 1")
	}
	switch {
	case c == "sshd -T":
		return []byte(f.sshdT), nil
	case strings.HasPrefix(c, "systemctl is-active "+fixRollbackUnit):
		return []byte(f.timer + "\n"), nil
	case strings.HasPrefix(c, "systemctl is-active"):
		return []byte("inactive\n"), errors.New("exit 3")
	case strings.HasPrefix(c, "systemd-run"):
		f.timer = "active"
	}
	return nil, nil
}

func fixAgent(t *testing.T) (*Agent, *fakeSys, string) {
	t.Helper()
	root := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("/etc/ssh/sshd_config", "Include /etc/ssh/sshd_config.d/*.conf\nPermitRootLogin yes\n")
	write("/etc/passwd", "root:x:0:0:root:/root:/bin/bash\njonas:x:1000:1000::/home/jonas:/bin/bash\n")
	f := &fakeSys{sshdT: "permitrootlogin yes\nx11forwarding yes\npasswordauthentication yes\n", timer: "inactive"}
	return &Agent{Root: root, Exec: f.exec}, f, root
}

func TestFixSSHWithRollbackTimer(t *testing.T) {
	a, f, root := fixAgent(t)
	f.sshdT = "permitrootlogin yes\nx11forwarding yes\npasswordauthentication yes\n"
	// Na het herladen ziet sshd de nieuwe waarden.
	orig := f.exec
	a.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "reload" {
			f.sshdT = "permitrootlogin without-password\nx11forwarding no\npasswordauthentication yes\n"
		}
		return orig(ctx, name, args...)
	}
	res, err := a.fix(context.Background(), &protocol.FixSpec{Rules: []string{"ssh-root-login", "ssh-x11", "ssh-password-auth"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.SSHPending || res.RollbackAt == nil {
		t.Fatalf("geen timer: %+v", res)
	}
	by := map[string]protocol.FixOutcome{}
	for _, o := range res.Outcomes {
		by[o.Rule] = o
	}
	if !by["ssh-root-login"].OK || !by["ssh-x11"].OK {
		t.Fatalf("uitkomsten: %+v", res.Outcomes)
	}
	// Geen authorized_keys: wachtwoorden blijven aan.
	if by["ssh-password-auth"].OK || !strings.Contains(by["ssh-password-auth"].Error, "authorized_keys") {
		t.Fatalf("password-auth zonder sleutel: %+v", by["ssh-password-auth"])
	}
	b, err := os.ReadFile(filepath.Join(root, fixSSHDropIn))
	if err != nil || !strings.Contains(string(b), "PermitRootLogin prohibit-password") || strings.Contains(string(b), "PasswordAuthentication") {
		t.Fatalf("drop-in: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(root, fixSSHScript)); err != nil {
		t.Fatalf("geen terugdraaiscript: %v", err)
	}
	iRun, iReload := -1, -1
	for i, c := range f.calls {
		if strings.HasPrefix(c, "systemd-run --unit="+fixRollbackUnit+" --on-active=600s") {
			iRun = i
		}
		if c == "systemctl reload ssh" {
			iReload = i
		}
	}
	if iRun < 0 || iReload < iRun {
		t.Fatalf("timer moet vóór het herladen: %v", f.calls)
	}
	// Tweede wijziging terwijl er een wacht: geweigerd.
	res, _ = a.fix(context.Background(), &protocol.FixSpec{Rules: []string{"ssh-max-auth-tries"}})
	if res.SSHPending || res.Outcomes[0].OK {
		t.Fatalf("tweede wijziging toegelaten: %+v", res)
	}
	if err := a.fixConfirm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, fixSSHScript)); !os.IsNotExist(err) {
		t.Fatalf("script na bevestigen nog daar: %v", err)
	}
}

func TestFixSSHRejectedBySshdT(t *testing.T) {
	a, f, root := fixAgent(t)
	f.failOn = "sshd -t"
	res, err := a.fix(context.Background(), &protocol.FixSpec{Rules: []string{"ssh-x11"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.SSHPending || res.Outcomes[0].OK || !strings.Contains(res.Outcomes[0].Error, "sshd -t") {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, fixSSHDropIn)); !os.IsNotExist(err) {
		t.Fatalf("drop-in niet weggehaald: %v", err)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "systemd-run") || strings.HasPrefix(c, "systemctl reload") {
			t.Fatalf("na een afgekeurde config toch %q", c)
		}
	}
}

func TestFixSSHNoTimerNoChange(t *testing.T) {
	a, f, root := fixAgent(t)
	f.failOn = "systemd-run"
	res, _ := a.fix(context.Background(), &protocol.FixSpec{Rules: []string{"ssh-x11"}})
	if res.SSHPending || res.Outcomes[0].OK {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, fixSSHDropIn)); !os.IsNotExist(err) {
		t.Fatalf("drop-in zonder timer blijven staan: %v", err)
	}
}

func TestFixSysctlFilesAndUnknown(t *testing.T) {
	a, f, root := fixAgent(t)
	f.failOn = "sysctl -w net.ipv6"
	res, err := a.fix(context.Background(), &protocol.FixSpec{Rules: []string{"net-syncookies", "net-redirects6", "net-forward", "file-passwd"}})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]protocol.FixOutcome{}
	for _, o := range res.Outcomes {
		by[o.Rule] = o
	}
	if !by["net-syncookies"].OK || by["net-redirects6"].OK || by["net-forward"].OK {
		t.Fatalf("%+v", res.Outcomes)
	}
	b, _ := os.ReadFile(filepath.Join(root, fixSysctlFile))
	if !strings.Contains(string(b), "net.ipv4.tcp_syncookies = 1") {
		t.Fatalf("sysctl-bestand: %q", b)
	}
	// file-passwd: alleen strenger (0644 blijft 0644, 0600 wordt nooit 0644).
	p := filepath.Join(root, "/etc/passwd")
	_ = os.Chmod(p, 0o600)
	o := a.fixFile("file-passwd", fixFiles["file-passwd"])
	fi, _ := os.Stat(p)
	if o.Error == "" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("rechten ruimer gemaakt: %o", fi.Mode().Perm())
	}
	if _, err := a.fix(context.Background(), &protocol.FixSpec{Rules: []string{"ssh-x11"}, RollbackAfter: 30}); err == nil {
		t.Fatal("te korte timer aanvaard")
	}
}
