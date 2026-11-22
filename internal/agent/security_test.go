package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseDpkg(t *testing.T) {
	out := []byte("ii \topenssl\t3.5.1-1+deb13u1\tamd64\topenssl\t3.5.1-1+deb13u1\n" +
		"ii \tlibssl3t64\t3.5.1-1+deb13u1\tamd64\topenssl\t3.5.1-1+deb13u1\n" +
		"rc \toud\t1.0\tamd64\toud\t1.0\n" +
		"hi \tbash\t5.2.37-2\tamd64\t\t\n" +
		"kapotte regel\n")
	pkgs, trunc := parseDpkg(out)
	if trunc || len(pkgs) != 3 {
		t.Fatalf("pakketten = %+v, afgekapt %v", pkgs, trunc)
	}
	if pkgs[1].Source != "openssl" || pkgs[1].Name != "libssl3t64" {
		t.Errorf("bron = %+v", pkgs[1])
	}
	if pkgs[2].Source != "bash" || pkgs[2].SourceVersion != "5.2.37-2" {
		t.Errorf("terugval op het pakket zelf = %+v", pkgs[2])
	}
}

func TestAudit(t *testing.T) {
	root := t.TempDir()
	write := func(p, body string, mode os.FileMode) {
		t.Helper()
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(full, mode); err != nil {
			t.Fatal(err)
		}
	}
	write("/etc/passwd", "root:x:0:0:root:/root:/bin/bash\ntoor:x:0:0::/root:/bin/sh\nwww-data:x:33:33::/var/www:/usr/sbin/nologin\njonas:x:1000:1000::/home/jonas:/bin/bash\n", 0o644)
	write("/etc/group", "root:x:0:\n", 0o644)
	write("/etc/shadow", "root:$y$abc:1::::::\ngast::1::::::\n", 0o640)
	write("/etc/sudoers.d/jonas", "jonas ALL=(ALL) NOPASSWD: ALL\n", 0o440)
	write("/etc/open.conf", "x", 0o666)
	write("/usr/bin/sudo", "", 0o755)
	_ = os.Chmod(filepath.Join(root, "/usr/bin/sudo"), os.ModeSetuid|0o755)
	write("/proc/sys/net/ipv4/ip_forward", "1\n", 0o644)
	write("/proc/sys/kernel/randomize_va_space", "2\n", 0o644)
	write("/proc/mounts", "tmpfs /tmp tmpfs rw,nosuid,nodev 0 0\nproc /proc proc rw 0 0\n", 0o644)
	write("/etc/apt/apt.conf.d/20auto-upgrades", "APT::Periodic::Update-Package-Lists \"1\";\nAPT::Periodic::Unattended-Upgrade \"1\";\n", 0o644)
	write("/var/log/lynis-report.dat", "hardening_index=67\nwarning[]=SSH-7408|Consider hardening SSH|AllowTcpForwarding|-|\nlynis_version=3.1.4\n", 0o640)
	a := &Agent{Root: root, Collector: &Collector{Root: root}, Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "sshd":
			return []byte("port 22\npermitrootlogin without-password\npasswordauthentication yes\nlistenaddress 0.0.0.0:22\nlistenaddress [::]:22\n"), nil
		case "systemctl":
			out := make([]string, len(args)-1)
			for i := range out {
				out[i] = "inactive"
			}
			out[0] = "active"
			return []byte(strings.Join(out, "\n")), errors.New("exit status 3")
		}
		return nil, nil
	}}
	au := a.audit(context.Background())
	if au.SSHD["permitrootlogin"] != "without-password" || au.SSHD["listenaddress"] != "0.0.0.0:22,[::]:22" {
		t.Errorf("sshd = %v", au.SSHD)
	}
	if au.Sysctl["net.ipv4.ip_forward"] != "1" || au.Sysctl["kernel.randomize_va_space"] != "2" {
		t.Errorf("sysctl = %v", au.Sysctl)
	}
	if !slices.Equal(au.UID0, []string{"root", "toor"}) || !slices.Equal(au.EmptyPasswords, []string{"gast"}) {
		t.Errorf("uid0 %v, leeg %v", au.UID0, au.EmptyPasswords)
	}
	if !slices.Equal(au.LoginUsers, []string{"root", "toor", "jonas"}) || !slices.Equal(au.SudoNopasswd, []string{"/etc/sudoers.d/jonas"}) {
		t.Errorf("login %v, sudo %v", au.LoginUsers, au.SudoNopasswd)
	}
	if au.Files["/etc/shadow"].Mode != "0640" || !slices.Contains(au.WorldWritable, "/etc/open.conf") || !slices.Contains(au.SUID, "/usr/bin/sudo") {
		t.Errorf("bestanden %v, ww %v, suid %v", au.Files, au.WorldWritable, au.SUID)
	}
	if au.Mounts["/tmp"] != "rw,nosuid,nodev" || au.Services["auditd"] != "active" || au.AutoUpgrades != "1" {
		t.Errorf("mounts %v, diensten %v, auto %q", au.Mounts, au.Services, au.AutoUpgrades)
	}
	if au.Lynis == nil || au.Lynis.HardeningIdx != 67 || au.Lynis.Warnings[0] != "SSH-7408: Consider hardening SSH (AllowTcpForwarding)" {
		t.Errorf("lynis = %+v", au.Lynis)
	}
}
