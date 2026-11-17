package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPBSFactsAndGet(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"usr/sbin", "etc/proxmox-backup"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h := pveHost{root: root}
	if h.isPBS() {
		t.Fatal("zonder proxmox-backup-manager geen PBS")
	}
	_ = os.WriteFile(filepath.Join(root, "usr/sbin/proxmox-backup-manager"), nil, 0o755)
	_ = os.WriteFile(filepath.Join(root, "etc/proxmox-backup/datastore.cfg"), []byte("datastore: store2\n\tpath /mnt/b\n\ndatastore: store1\n\tpath /mnt/a\n"), 0o644)
	var calls []string
	h.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name == "dpkg-query" {
			return []byte("4.0.14-1"), nil
		}
		return []byte(`[{"store":"store1"}]` + "\n"), nil
	}
	f := h.pbsFacts(context.Background())
	if f == nil || f.Version != "4.0.14-1" || !slices.Equal(f.Datastores, []string{"store1", "store2"}) {
		t.Fatalf("facts %+v", f)
	}
	data, err := h.pbsGet(context.Background(), "/admin/datastore", map[string]string{"ns": "-x"})
	if err != nil || string(data) != `[{"store":"store1"}]` {
		t.Fatalf("get %s %v", data, err)
	}
	if last := calls[len(calls)-1]; last != "proxmox-backup-debug api get /admin/datastore --output-format json --ns=-x" {
		t.Fatalf("aanroep %q", last)
	}
}

func TestPBSUPID(t *testing.T) {
	out := "Result: \"UPID:pbs1:000012AB:0001A2B3:00000003:65F0A1B2:garbage_collection:store1:root@pam:\"\n"
	if got := pbsUPIDRe.FindString(out); got != "UPID:pbs1:000012AB:0001A2B3:00000003:65F0A1B2:garbage_collection:store1:root@pam:" {
		t.Fatalf("upid %q", got)
	}
}

func TestPBSFindUPIDEscaped(t *testing.T) {
	out := `"UPID:pbs1:000012AB:0001A2B3:00000003:65F0A1B2:garbage_collection:backup\\x2d1:root@pam:"`
	want := `UPID:pbs1:000012AB:0001A2B3:00000003:65F0A1B2:garbage_collection:backup\x2d1:root@pam:`
	if got := pbsFindUPID(out); got != want {
		t.Fatalf("upid %q, verwacht %q", got, want)
	}
}
