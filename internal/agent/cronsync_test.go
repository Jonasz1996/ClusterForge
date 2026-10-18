package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// TestMain laat het testprogramma ook sync-receive spelen: het nep-ssh van
// een test start het met CF_TEST_SYNC_RECEIVE, zoals sshd de forced
// command van authorized_keys start.
func TestMain(m *testing.M) {
	if root := os.Getenv("CF_TEST_SYNC_RECEIVE"); root != "" {
		addrs := strings.Split(os.Getenv("CF_TEST_ADDRS"), ",")
		os.Exit((&SyncReceiver{Root: root, Addresses: func() []string { return addrs }}).Main(os.Getenv("CF_TEST_FROM"), os.Getenv("SSH_ORIGINAL_COMMAND")))
	}
	os.Exit(m.Run())
}

// FakeSyncSSH schrijft een ssh voor rsync -e dat sync-receive van het
// testprogramma start in de plaats van sshd op het doel.
func FakeSyncSSH(t *testing.T, targetRoot, from string, addrs ...string) string {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "ssh")
	script := fmt.Sprintf(`#!/bin/sh
# rsync start: ssh -l root <adres> rsync --server ...
[ "$1" = "-l" ] && shift 2
shift
SSH_ORIGINAL_COMMAND="$*" CF_TEST_SYNC_RECEIVE=%q CF_TEST_FROM=%q CF_TEST_ADDRS=%q exec %q -test.run='^$'
`, targetRoot, from, strings.Join(addrs, ","), bin)
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

const (
	nodeSrc = "6a1f0000-0000-4000-8000-0000000000a1"
	nodeDst = "6a1f0000-0000-4000-8000-0000000000b2"
)

// TestSync kopieert met echte rsync van een bron naar een doel via
// sync-receive: een gewone sync, een doel met het VIP dat weigert, --delete
// zonder toestemming, en een proefrun.
func TestSync(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync staat niet op deze machine")
	}
	ctx := context.Background()
	src, dst := t.TempDir()+"/data/", t.TempDir()+"/kopie met spatie/"
	writeTree(t, "/", map[string]string{src + "a.txt": "hallo", src + "map/b.txt": "wereld", src + "c.part": "half"}, 0o644)
	srcRoot, dstRoot := t.TempDir(), t.TempDir()
	source := &Agent{Root: srcRoot, Exec: func(context.Context, string, ...string) ([]byte, error) { return nil, nil }}
	target := &Agent{Root: dstRoot, Exec: source.Exec}
	writeTree(t, dstRoot, map[string]string{"/etc/ssh/ssh_host_ed25519_key.pub": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOTe root@doel\n",
		"/root/.ssh/authorized_keys": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEigen jonas@pc\n"}, 0o600)

	// De sleutels: de bron maakt de zijne, het doel geeft zijn hostsleutel.
	keys := source.cronKeys(ctx, []string{src})
	if keys.Error != "" || !strings.HasPrefix(keys.Keys.PublicKey, "ssh-ed25519 ") {
		t.Fatalf("sleutel: %+v", keys)
	}
	if again := source.cronKeys(ctx, nil); again.Keys.PublicKey != keys.Keys.PublicKey {
		t.Error("een tweede keer een andere sleutel")
	}
	if keys.Keys.Free[src] <= 0 {
		t.Errorf("vrije ruimte: %v", keys.Keys.Free)
	}
	hostKeys := target.cronKeys(ctx, nil).Keys.HostKeys
	if len(hostKeys) != 1 {
		t.Fatalf("hostsleutels: %v", hostKeys)
	}

	job := testJob(jobA, "")
	job.User, job.Kind, job.RunOn, job.VIPs = "root", protocol.CronKindSync, protocol.CronOnNode, nil
	job.Sync = &protocol.SyncSpec{Source: src, Target: dst, Exclude: []string{"*.part"},
		Targets: []protocol.SyncTarget{{NodeID: nodeDst, Hostname: "nc2", Address: "127.0.0.1", HostKeys: hostKeys}}}
	if res := source.cronApply(ctx, []protocol.ManagedCron{job}, nil); res.Error != "" {
		t.Fatalf("bron: %+v", res)
	}
	known, _ := os.ReadFile(filepath.Join(srcRoot, protocol.SyncDir, "known_hosts"))
	if string(known) != "127.0.0.1 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOTe\n" {
		t.Errorf("known_hosts: %q", known)
	}
	recv := protocol.SyncReceive{JobID: jobA, From: nodeSrc, FromHost: "nc1", Addresses: []string{"192.0.2.1", "10.0.0.185"},
		PublicKey: keys.Keys.PublicKey, Path: dst, VIPs: []string{"10.0.0.185"}}
	if res := target.cronApply(ctx, nil, []protocol.SyncReceive{recv}); res.Error != "" || res.Hash != protocol.CronApplyHash(nil, []protocol.SyncReceive{recv}) {
		t.Fatalf("doel: %+v", res)
	}
	auth, _ := os.ReadFile(filepath.Join(dstRoot, protocol.SyncAuthorizedKeys))
	want := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEigen jonas@pc\n" + protocol.SyncBlockBegin + "\n" +
		`restrict,from="10.0.0.185,192.0.2.1",command="/usr/local/bin/cf-agent sync-receive --from ` + nodeSrc + `" ` +
		keys.Keys.PublicKey + " cf-sync-nc1\n" + protocol.SyncBlockEnd + "\n"
	if string(auth) != want {
		t.Errorf("authorized_keys:\n%s\nwil:\n%s", auth, want)
	}

	// De sync: twee bestanden, de .part niet.
	writeJob(t, srcRoot, job)
	r := &CronRunner{Root: srcRoot, SSH: FakeSyncSSH(t, dstRoot, nodeSrc, "192.0.2.2")}
	if code := r.Main(ctx, jobA); code != 0 {
		t.Fatalf("sync: %d %+v", code, spooled(t, srcRoot))
	}
	runs := spooled(t, srcRoot)
	if len(runs) != 1 || runs[0].Result != protocol.CronOK || len(runs[0].Sync) != 1 {
		t.Fatalf("run: %+v", runs)
	}
	if s := runs[0].Sync[0]; s.Result != protocol.SyncOK || s.Files != 2 || s.Bytes != 11 || s.Hostname != "nc2" {
		t.Errorf("uitkomst: %+v", s)
	}
	if b, _ := os.ReadFile(dst + "map/b.txt"); string(b) != "wereld" {
		t.Errorf("b.txt: %q", b)
	}
	if _, err := os.Stat(dst + "c.part"); !os.IsNotExist(err) {
		t.Error("c.part is meegekopieerd")
	}
	(cronSpool{filepath.Join(srcRoot, protocol.CronStateDir, "spool")}).sent(runs)

	// Een proefrun zegt wat er zou veranderen.
	writeTree(t, "/", map[string]string{src + "nieuw.txt": "nieuw"}, 0o644)
	source.SyncSSH = r.SSH
	dry := source.cronDryRun(ctx, jobA, nodeDst)
	if dry.Error != "" || !slices.ContainsFunc(dry.Output, func(l string) bool { return strings.HasSuffix(l, " nieuw.txt") }) {
		t.Errorf("proefrun: %+v", dry)
	}
	if _, err := os.Stat(dst + "nieuw.txt"); !os.IsNotExist(err) {
		t.Error("de proefrun kopieerde")
	}

	// --delete zonder toestemming op het doel: geweigerd.
	job.Sync.Delete = true
	writeJob(t, srcRoot, job)
	if code := r.Main(ctx, jobA); code == 0 {
		t.Error("--delete zonder toestemming lukte")
	}
	runs = spooled(t, srcRoot)
	if len(runs) != 1 || runs[0].Result != protocol.CronFailed || runs[0].Sync[0].Result != protocol.SyncRefusal ||
		!strings.Contains(runs[0].Sync[0].Error, "niets weghalen") {
		t.Fatalf("delete: %+v", runs)
	}
	(cronSpool{filepath.Join(srcRoot, protocol.CronStateDir, "spool")}).sent(runs)

	// Het doel heeft het VIP: geweigerd.
	job.Sync.Delete = false
	writeJob(t, srcRoot, job)
	r.SSH = FakeSyncSSH(t, dstRoot, nodeSrc, "192.0.2.2", "10.0.0.185")
	r.Main(ctx, jobA)
	runs = spooled(t, srcRoot)
	if len(runs) != 1 || runs[0].Sync[0].Result != protocol.SyncRefusal || !strings.Contains(runs[0].Sync[0].Error, "VIP") {
		t.Fatalf("VIP: %+v", runs)
	}

	// Zonder syncs gaat het blok weg; de eigen sleutel van Jonas blijft.
	if res := target.cronApply(ctx, nil, nil); res.Error != "" {
		t.Fatal(res.Error)
	}
	auth, _ = os.ReadFile(filepath.Join(dstRoot, protocol.SyncAuthorizedKeys))
	if string(auth) != "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEigen jonas@pc\n" {
		t.Errorf("na het weghalen:\n%s", auth)
	}
}

// TestSyncReceiveRefuses: wat sync-receive niet toelaat.
func TestSyncReceiveRefuses(t *testing.T) {
	root := t.TempDir()
	// De doelmap maakt sync-receive zelf; in CI draait de test niet als root.
	data := t.TempDir() + "/data/"
	recv := []protocol.SyncReceive{{JobID: jobA, From: nodeSrc, Path: data, Addresses: []string{"192.0.2.1"}}}
	js, _ := json.Marshal(recv)
	writeTree(t, root, map[string]string{protocol.SyncDir + "/receive.json": string(js)}, 0o600)
	var ran []string
	r := &SyncReceiver{Root: root, Addresses: func() []string { return nil }, Stderr: &strings.Builder{}, Exec: func(argv []string) error {
		ran = argv
		return nil
	}}
	for name, tc := range map[string]struct{ from, cmd string }{
		"andere bron":   {nodeDst, "rsync --server -logDtpre.iLsfxCIvu . " + data},
		"andere map":    {nodeSrc, "rsync --server -logDtpre.iLsfxCIvu . /etc/"},
		"omweg":         {nodeSrc, "rsync --server -logDtpre.iLsfxCIvu . " + data + "../../etc/"},
		"lezen":         {nodeSrc, "rsync --server --sender -logDtpre.iLsfxCIvu . " + data},
		"delete":        {nodeSrc, "rsync --server -logDtpre.iLsfxCIvu --delete . " + data},
		"optie":         {nodeSrc, "rsync --server --rsync-path=/bin/sh . " + data},
		"shell":         {nodeSrc, "rsync --server . " + data + "; rm -rf /"},
		"ander program": {nodeSrc, "sh -c id"},
	} {
		ran = nil
		if code := r.Main(tc.from, tc.cmd); code != 3 || ran != nil {
			t.Errorf("%s: %d %v", name, code, ran)
		}
	}
	if code := r.Main(nodeSrc, "rsync --server -logDtpre.iLsfxCIvu --partial --bwlimit=100 . "+data); code != 0 || len(ran) != 7 {
		t.Errorf("toegestaan: %d %v", code, ran)
	}
}
