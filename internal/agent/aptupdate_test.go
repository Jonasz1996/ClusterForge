package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestRunAptSmartRepairs(t *testing.T) {
	upgrades := 0
	var root string
	h := &fakeHost{reply: func(c string) (string, int) {
		switch {
		case strings.HasPrefix(c, "apt-get update"):
			return "", 0
		case c == "dpkg --audit":
			if upgrades == 0 {
				return "The following packages are only half configured:\n openssl\n", 0
			}
			return "", 0
		case strings.HasPrefix(c, "apt-get full-upgrade"):
			upgrades++
			if upgrades == 1 {
				return "E: Sub-process /usr/bin/dpkg returned an error code (1)\n", 100
			}
			// dpkg zette een nieuwe standaardconfiguratie naast de eigen.
			_ = os.MkdirAll(filepath.Join(root, "etc/ssh"), 0o755)
			_ = os.WriteFile(filepath.Join(root, "etc/ssh/sshd_config.dpkg-dist"), nil, 0o644)
			return "", 0
		}
		return "", 0
	}}
	a, r := runAgent(t, h)
	root = r
	res := a.runStart(context.Background(), protocol.RunSpec{ID: "apt-2", Kind: protocol.RunApt, Apt: &protocol.AptRun{Full: true, Smart: true}})
	st := res.Status
	if st == nil || st.State != protocol.RunOK {
		t.Fatalf("%+v\n%s", res, res.Output)
	}
	if upgrades != 2 {
		t.Errorf("upgrade %d keer, verwacht 2 (herstellen en opnieuw)", upgrades)
	}
	if got := h.called("dpkg --configure -a"); len(got) != 2 {
		t.Errorf("dpkg --configure -a: %v", got)
	}
	if got := h.called("apt-get -f install"); len(got) != 2 {
		t.Errorf("apt-get -f install: %v", got)
	}
	for _, c := range h.called("apt-get ") {
		if !strings.Contains(c, "DPkg::Lock::Timeout=600") || (!strings.HasPrefix(c, "apt-get update") && !strings.Contains(c, "--force-confold")) {
			t.Errorf("zonder slot of confold: %s", c)
		}
	}
	if len(st.ConfNew) != 1 || st.ConfNew[0] != "/etc/ssh/sshd_config.dpkg-dist" {
		t.Errorf("nieuwe configuratie %v", st.ConfNew)
	}
}

func TestRunAptSmartStillBroken(t *testing.T) {
	h := &fakeHost{reply: func(c string) (string, int) {
		if c == "dpkg --audit" {
			return "half configured: foo\n", 0
		}
		return "", 0
	}}
	a, _ := runAgent(t, h)
	res := a.runStart(context.Background(), protocol.RunSpec{ID: "apt-3", Kind: protocol.RunApt, Apt: &protocol.AptRun{Smart: true}})
	if res.Status == nil || res.Status.State != protocol.RunFailed || !strings.Contains(res.Status.Error, "dpkg is na de update niet in orde") {
		t.Fatalf("%+v", res.Status)
	}
}
