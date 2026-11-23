package templates

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mpContext is media-platform op drie nodes, gerenderd voor de tweede, met
// de parameters van de test plus extra.
func mpContext(t *testing.T, extra map[string]any) (*Template, Context) {
	t.Helper()
	tpl, ok := Latest("media-platform")
	if !ok {
		t.Fatal("geen template media-platform")
	}
	in := map[string]any{
		"vip": "192.168.10.150", "vrid": 50, "auth_pass": "geheim12", "db_vip": "192.168.10.144",
		"platform_ref": "claude/stappenplan-uitwerken-lu4uv3", "seaweedfs_image": "chrislusf/seaweedfs:3.97",
		"db_password": "Wachtwoord-123", "office_jwt_secret": "officegeheim1", "office_link_secret": "linkgeheim12",
		"media_worker_key": "mediasleutel1",
	}
	for k, v := range extra {
		in[k] = v
	}
	params, err := tpl.Validate(in)
	if err != nil {
		t.Fatal(err)
	}
	c := Context{Params: params, Cluster: ClusterInfo{Name: "Platform", Slug: "platform", Environment: "prod"}}
	for i := 1; i <= 3; i++ {
		c.Nodes = append(c.Nodes, NodeInfo{Hostname: fmt.Sprintf("media-%02d", i), Role: "node", Index: i,
			Address: fmt.Sprintf("192.168.10.%d", 145+i), Prefix: 24, Interface: "eth0"})
	}
	c.Node = &c.Nodes[1]
	return tpl, c
}

func TestRenderMediaPlatform(t *testing.T) {
	tpl, c := mpContext(t, nil)
	params := c.Params
	if params["replication"] != "001" || params["nodes"] != 3 || params["db_password"] == nil || params["arr"] != false ||
		params["blob_split"] != false {
		t.Fatalf("standaarden: %+v", params)
	}
	files, services := npmFiles(t, tpl, c)
	// cf-platform krijgt nooit een service-stap: alleen keepalived start het.
	if strings.Contains(strings.Join(services, ","), "cf-platform,") || strings.HasSuffix(strings.Join(services, ","), "cf-platform") {
		t.Fatalf("services: %v", services)
	}
	peers := "192.168.10.146:9333,192.168.10.147:9333,192.168.10.148:9333"
	for path, want := range map[string][]string{
		"/etc/clusterforge/seaweedfs.yaml": {"image: chrislusf/seaweedfs:3.97", "master -ip=192.168.10.147", "-peers=" + peers,
			"-defaultReplication=001", "-mserver=" + peers, "-port=8081", "-defaultReplicaPlacement=001"},
		"/etc/seaweedfs/filer.toml": {"[postgres2]", `hostname = "192.168.10.144"`, "port = 5432", `database = "seaweedfs"`, `password = "Wachtwoord-123"`},
		"/etc/clusterforge/platform.env": {"DATA_ROOT=/srv/platform-data\n", "NODE_NAME=media-02\n",
			"PLATFORM_DATABASE_URL=postgresql+asyncpg://platform:Wachtwoord-123@192.168.10.144:5432/platform\n",
			"PLATFORM_CLUSTER_NODES=media-01=192.168.10.146,media-02=192.168.10.147,media-03=192.168.10.148\n",
			"OFFICE_JWT_SECRET=officegeheim1\n", "ANALYSE=aan\n", "BEELD=aan\n", "PLAATSEN=aan\n",
			"MODELLEN_ROOT=/opt/cf-platform/modellen\n", "SLEUTELS_ROOT=/var/lib/cf-platform/sleutels/gezichten\n",
			"ANALYSE_WORKER_KEY=" + sha256hex("analyse mediasleutel1") + "\n", "\nARR=uit\n", "\nPLATFORM_BLOB_SPLIT=false\n"},
		"/etc/clusterforge/platform-arr.yaml": {"qbittorrent:\n    restart: \"no\"\n    volumes:\n      - /var/lib/cf-platform/torrents:/downloads\n",
			"prowlarr:\n    restart: \"no\"\n", "platform:\n    volumes:\n      - /var/lib/cf-platform/torrents:/data/import/torrents\n"},
		"/etc/clusterforge/platform-analyse.yaml":      {"analyse:\n    image: cf-platform:local", "analyse-omzetten:", "PLATFORM_ANALYSE_POSTGRES: \"true\""},
		"/etc/systemd/system/cf-platform-status.timer": {"OnUnitActiveSec=60"},
		"/etc/clusterforge/platform-cluster.yaml": {"PLATFORM_MODE: cluster", `restart: "no"`, "image: cf-platform:local",
			"PLATFORM_BLOB_SPLIT: ${PLATFORM_BLOB_SPLIT:-false}\n"},
		"/usr/local/lib/clusterforge/platform-src.sh": {"REF='claude/stappenplan-uitwerken-lu4uv3'", "SRC=/opt/cf-platform/src", "-p cf-platform"},
		"/usr/local/lib/clusterforge/platform-ctl.sh": {"docker image inspect -f '{{.Id}}' cf-platform:local", "-f /etc/clusterforge/platform-cluster.yaml",
			"docker compose -p cf-platform", "VIP='192.168.10.150'", "overstap proef|echt", "db-copy \"$url\" --leegmaken",
			"REPLICATIE='001'", "db_backup\n    systemctl restart cf-platform", "\"app\":\"media-platform\"", "--format '{{.Names}} {{.Status}}'",
			"sleutels-klaar) sleutels_klaar ;;", "python -m app.cli sleutels-check </dev/null",
			"docker info --format '{{.MemoryLimit}} {{.CPUCfsQuota}}'"},
		"/etc/systemd/system/cf-platform.service": {"ConditionPathExists=/srv/platform-data/.cf-platform-active", "\nBindsTo=keepalived.service\n",
			"\nAfter=docker.service cf-platform-mount.service keepalived.service\n", "\nSuccessExitStatus=3\n"},
		"/etc/systemd/system/cf-platform-mount.service": {"-dir=/srv/platform-data", "-allowOthers -cacheDir=/var/cache/weed-mount'\n"},
		"/etc/keepalived/keepalived.conf":               {"priority 140\n", "nopreempt", "notify_master", "192.168.10.146\n        192.168.10.148\n", "include /etc/keepalived/conf.d/*.conf\n"},
	} {
		got, ok := files[path]
		if !ok {
			t.Errorf("geen bestand %s", path)
			continue
		}
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s mist %q:\n%s", path, w, got)
			}
		}
	}
	// De mount geeft geen replicatie mee: de filer kiest per map (fs.configure).
	for _, l := range strings.Split(files["/etc/systemd/system/cf-platform-mount.service"], "\n") {
		if strings.HasPrefix(l, "ExecStart=") && strings.Contains(l, "-replication") {
			t.Errorf("mount met -replication: %s", l)
		}
	}
	scripts := []string{"/usr/local/lib/clusterforge/platform-src.sh", "/usr/local/lib/clusterforge/platform-ctl.sh", "/usr/local/lib/clusterforge/platform-weed.sh"}
	for _, path := range scripts {
		for _, sh := range []string{"sh", "dash"} {
			if _, err := exec.LookPath(sh); err != nil {
				continue
			}
			cmd := exec.Command(sh, "-n")
			cmd.Stdin = strings.NewReader(files[path])
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("%s -n %s: %v %s", sh, path, err, out)
			}
		}
	}
	if _, err := exec.LookPath("shellcheck"); err == nil {
		dir := t.TempDir()
		for _, path := range scripts {
			p := filepath.Join(dir, filepath.Base(path))
			must(t, os.WriteFile(p, []byte(files[path]), 0o755))
			if out, err := exec.Command("shellcheck", "-s", "sh", "-S", "warning", p).CombinedOutput(); err != nil {
				t.Errorf("shellcheck %s:\n%s", path, out)
			}
		}
	}
}

// De sleutels gaan met wissen naar de andere nodes, maar alleen na
// sleutels-klaar op de VIP-node: faalt die, dan kopieert de sync niets
// (syncBefore in de agent). De back-ups wissen nooit.
func TestMediaPlatformSyncs(t *testing.T) {
	tpl, c := mpContext(t, nil)
	syncs, err := tpl.RenderSyncs(c)
	if err != nil || len(syncs) != 2 {
		t.Fatalf("syncs: %+v %v", syncs, err)
	}
	if s := syncs[0]; s.Name != "platform-sleutels" || s.Source != "/var/lib/cf-platform/sleutels/" || s.Target != s.Source ||
		!s.Delete || s.Before != "/usr/local/lib/clusterforge/platform-ctl.sh sleutels-klaar" || !s.Alert {
		t.Errorf("sync van de sleutels: %+v", s)
	}
	if s := syncs[1]; s.Name != "platform-backups" || s.Delete || s.Before != "" {
		t.Errorf("sync van de back-ups: %+v", s)
	}
}

// mpNode is een nagespeelde node van media-platform: platform-ctl.sh met de
// paden van de node in een eigen map, en nep-commando's. Een bestand in die
// map zet een toestand aan: vip (deze node heeft het VIP), mounted,
// active-<unit> (systemctl is-active), cli-<commando> (wat app.cli
// <commando> in de container afdrukt; zonder bestand kent de versie het
// commando niet) met cli-<commando>.rc, uid (stat -c %u van elke map),
// docker-info, services (compose config --services), healthy (/health),
// vol-status en leader (de master van SeaweedFS), arr-running. Elk
// nep-commando schrijft zijn argumenten in log.
type mpNode struct {
	t           *testing.T
	dir, script string
}

func newMPNode(t *testing.T, files map[string]string) *mpNode {
	t.Helper()
	dir := t.TempDir()
	paths := strings.NewReplacer(
		"/etc/clusterforge/", dir+"/etc/",
		"/srv/platform-data", dir+"/data",
		"/opt/cf-platform/src", dir+"/src",
		"/opt/cf-platform/modellen", dir+"/modellen",
		"/run/cf-platform.hash", dir+"/hash",
		"/var/backups/cf-platform", dir+"/backups",
		"/var/lib/clusterforge", dir+"/varlib",
		"/var/lib/cf-platform", dir+"/cfp",
		"/usr/local/bin/weed", "weed",
	)
	n := &mpNode{t: t, dir: dir, script: filepath.Join(dir, "platform-ctl.sh")}
	must(t, os.MkdirAll(filepath.Join(dir, "bin"), 0o755))
	must(t, os.MkdirAll(filepath.Join(dir, "src/infra/single"), 0o755))
	must(t, os.MkdirAll(filepath.Join(dir, "data"), 0o755))
	for path, content := range files {
		if strings.HasPrefix(path, "/etc/clusterforge/") {
			write(t, map[string]string{paths.Replace(path): paths.Replace(content)})
		}
	}
	must(t, os.WriteFile(n.script, []byte(paths.Replace(files["/usr/local/lib/clusterforge/platform-ctl.sh"])), 0o755))
	realStat, err := exec.LookPath("stat")
	if err != nil {
		t.Skip("stat staat niet op deze machine")
	}
	stubs := map[string]string{
		"ip":         `[ -f "$CF_TEST_DIR/vip" ] && echo "2: eth0    inet 192.168.10.150/24 scope global secondary eth0"; exit 0`,
		"mountpoint": `[ -f "$CF_TEST_DIR/mounted" ]`,
		"systemctl": `case "$1 $2" in "is-active --quiet") [ -f "$CF_TEST_DIR/active-$3" ]; exit $?;; "is-failed --quiet") exit 1;; esac
echo "systemctl $*" >> "$CF_TEST_DIR/log"`,
		"systemd-run": `echo "systemd-run $*" >> "$CF_TEST_DIR/log"`,
		"stat": `if [ "$1 $2" = "-c %u" ] && [ -f "$CF_TEST_DIR/uid" ]; then cat "$CF_TEST_DIR/uid"; exit 0; fi
exec ` + realStat + ` "$@"`,
		"chown":   `echo "chown $*" >> "$CF_TEST_DIR/log"`,
		"setpriv": `while case "$1" in --*) true;; *) false;; esac; do shift; done; exec "$@"`,
		"sleep":   `exit 0`,
		"curl": `case "$*" in
*/health*) [ -f "$CF_TEST_DIR/healthy" ];;
*/vol/status*) cat "$CF_TEST_DIR/vol-status";;
*/cluster/status*) [ -f "$CF_TEST_DIR/leader" ] && echo '{"IsLeader":true}';;
*) exit 1;;
esac`,
		"weed": `echo "weed $*" >> "$CF_TEST_DIR/log"; cat >> "$CF_TEST_DIR/weed-in"`,
		"pg_dump": `echo "pg_dump $* PGPASSWORD=$PGPASSWORD" >> "$CF_TEST_DIR/log"
while [ $# -gt 0 ]; do [ "$1" = -f ] && echo dump > "$2"; shift; done`,
		"docker": `echo "docker $*" >> "$CF_TEST_DIR/log"
case "$1" in
info) cat "$CF_TEST_DIR/docker-info" 2>/dev/null;;
ps) [ -f "$CF_TEST_DIR/arr-running" ] || exit 0
  case "$*" in *service=qbittorrent*) echo qbit1;; *service=prowlarr*) echo prowl1;; esac;;
compose) case "$*" in
  *" config --services"*) cat "$CF_TEST_DIR/services";;
  *" exec -T platform python -m app.cli "*)
    sub=${*##*app.cli }; sub=${sub%% *}
    [ -f "$CF_TEST_DIR/cli-$sub" ] || { echo "app.cli: invalid choice: '$sub'" >&2; exit 2; }
    cat "$CF_TEST_DIR/cli-$sub"; exit "$(cat "$CF_TEST_DIR/cli-$sub.rc" 2>/dev/null || echo 0)";;
  esac;;
esac
exit 0`,
	}
	for name, body := range stubs {
		must(t, os.WriteFile(filepath.Join(dir, "bin", name), []byte("#!/bin/sh\n"+body+"\n"), 0o755))
	}
	return n
}

// set zet toestanden aan (inhoud "" is een leeg bestand) of uit (nil).
func (n *mpNode) set(states map[string]*string) {
	n.t.Helper()
	for name, v := range states {
		p := filepath.Join(n.dir, name)
		if v == nil {
			_ = os.Remove(p)
			continue
		}
		must(n.t, os.MkdirAll(filepath.Dir(p), 0o755))
		must(n.t, os.WriteFile(p, []byte(*v), 0o644))
	}
}

func (n *mpNode) run(args ...string) (string, error) {
	n.t.Helper()
	_ = os.Remove(filepath.Join(n.dir, "log"))
	cmd := exec.Command("/bin/sh", append([]string{n.script}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(n.dir, "bin")+":"+os.Getenv("PATH"), "CF_TEST_DIR="+n.dir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (n *mpNode) log() string {
	b, _ := os.ReadFile(filepath.Join(n.dir, "log"))
	return string(b)
}

func on(s string) *string { return &s }

// TestMediaPlatformSleutelsKlaar: de controle vóór de sync met wissen laat
// alleen door als alles klopt; anders kopieert en wist de sync niets.
func TestMediaPlatformSleutelsKlaar(t *testing.T) {
	tpl, c := mpContext(t, nil)
	files, _ := npmFiles(t, tpl, c)
	n := newMPNode(t, files)
	keys := filepath.Join(n.dir, "cfp/sleutels/gezichten")
	must(t, os.MkdirAll(keys, 0o700))
	ok := map[string]*string{"vip": on(""), "active-cf-platform": on(""), "uid": on("10001\n"),
		"cli-sleutels-check": on("sleutelmap: in orde\n"), "cli-sleutels-check.rc": on("0")}
	for _, tc := range []struct {
		name  string
		state map[string]*string
		setup func(t *testing.T)
		want  string
		exec  bool // sleutels-check in de container gedraaid
	}{
		{"zonder VIP", map[string]*string{"vip": nil}, nil, "heeft het VIP 192.168.10.150 niet", false},
		{"platform draait niet", map[string]*string{"active-cf-platform": nil}, nil, "het platform draait hier niet", false},
		{"andere eigenaar", map[string]*string{"uid": on("0\n")}, nil, "niet van uid 10001", false},
		{"map ontbreekt", nil, func(t *testing.T) { must(t, os.Remove(keys)) }, "is geen gewone map", false},
		{"map is een symlink", nil, func(t *testing.T) {
			must(t, os.Rename(keys, keys+".echt"))
			must(t, os.Symlink(keys+".echt", keys))
		}, "is geen gewone map", false},
		{"bovenliggende map is een symlink", nil, func(t *testing.T) {
			parent := filepath.Dir(keys)
			must(t, os.Rename(parent, parent+".echt"))
			must(t, os.Symlink(parent+".echt", parent))
		}, "is geen gewone map", false},
		{"versie zonder sleutels-check", map[string]*string{"cli-sleutels-check": nil}, nil, "niets gekopieerd of gewist", true},
		{"sleutel ontbreekt", map[string]*string{"cli-sleutels-check.rc": on("1")}, nil, "niets gekopieerd of gewist", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			must(t, os.RemoveAll(filepath.Join(n.dir, "cfp")))
			must(t, os.MkdirAll(keys, 0o700))
			n.set(ok)
			n.set(tc.state)
			if tc.setup != nil {
				tc.setup(t)
			}
			out, err := n.run("sleutels-klaar")
			if err == nil || !strings.Contains(out, tc.want) {
				t.Errorf("sleutels-klaar gaf %v:\n%s", err, out)
			}
			if got := strings.Contains(n.log(), "app.cli sleutels-check"); got != tc.exec {
				t.Errorf("sleutels-check gedraaid: %v:\n%s", got, n.log())
			}
		})
	}
	n.set(ok)
	must(t, os.RemoveAll(filepath.Join(n.dir, "cfp")))
	must(t, os.MkdirAll(keys, 0o700))
	if out, err := n.run("sleutels-klaar"); err != nil || !strings.Contains(out, "sleutelmap: in orde") {
		t.Errorf("sleutels-klaar: %v\n%s", err, out)
	}
	if l := n.log(); !strings.Contains(l, "docker compose -p cf-platform") || !strings.Contains(l, "exec -T platform python -m app.cli sleutels-check") {
		t.Errorf("docker: %s", l)
	}
}

// TestMediaPlatformBackup: de back-up van de database laat de rijen van de
// tabellen van backup-wipe-tables weg, of die van de vaste lijst als de
// versie het commando niet kent of het platform hier niet draait.
func TestMediaPlatformBackup(t *testing.T) {
	tpl, c := mpContext(t, nil)
	files, _ := npmFiles(t, tpl, c)
	n := newMPNode(t, files)
	fixed := []string{"face_person", "face", "face_scan", "face_owner", "blob_embed"}
	for _, tc := range []struct {
		name  string
		state map[string]*string
		want  []string
		exec  bool
	}{
		{"versie met het commando", map[string]*string{"active-cf-platform": on(""), "cli-backup-wipe-tables": on("face\nblob_embed\nfoo;drop\n")},
			[]string{"face", "blob_embed"}, true},
		{"versie zonder het commando", map[string]*string{"active-cf-platform": on(""), "cli-backup-wipe-tables": nil}, fixed, true},
		{"commando zegt niets bruikbaars", map[string]*string{"active-cf-platform": on(""), "cli-backup-wipe-tables": on("*\n")}, fixed, true},
		{"platform draait hier niet", map[string]*string{"active-cf-platform": nil, "cli-backup-wipe-tables": on("face\n")}, fixed, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n.set(tc.state)
			out, err := n.run("db-backup")
			if err != nil {
				t.Fatalf("db-backup: %v\n%s", err, out)
			}
			var argv string
			for _, l := range strings.Split(n.log(), "\n") {
				if strings.HasPrefix(l, "pg_dump ") {
					argv, _, _ = strings.Cut(l, " PGPASSWORD=")
				}
			}
			var excl []string
			for _, f := range strings.Fields(argv) {
				if v, ok := strings.CutPrefix(f, "--exclude-table-data="); ok {
					excl = append(excl, v)
				}
			}
			if strings.Join(excl, " ") != strings.Join(tc.want, " ") || strings.Contains(argv, "Wachtwoord-123") ||
				!strings.Contains(argv, "-h 192.168.10.144 -p 5432 -U platform -d platform") {
				t.Errorf("pg_dump %s (weggelaten: %v)", argv, excl)
			}
			if got := strings.Contains(n.log(), "app.cli backup-wipe-tables"); got != tc.exec {
				t.Errorf("backup-wipe-tables gedraaid: %v", got)
			}
		})
	}
	if dumps, _ := filepath.Glob(filepath.Join(n.dir, "backups", "platform-*-onbekend.dump")); len(dumps) == 0 {
		t.Error("geen back-up")
	}
}

// composeFiles zijn de -f-bestanden (alleen de naam) van de laatste docker
// compose in de log van de node.
func (n *mpNode) composeFiles() string {
	var files []string
	for _, l := range strings.Split(n.log(), "\n") {
		if !strings.HasPrefix(l, "docker compose ") {
			continue
		}
		files = files[:0]
		f := strings.Fields(l)
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "-f" {
				files = append(files, filepath.Base(f[i+1]))
			}
		}
	}
	return strings.Join(files, " ")
}

// TestMediaPlatformCompose: de grenzen van media, office en analyse alleen
// als Docker ze aankan en de versie ze heeft (zoals install.sh), de analyse
// alleen met ANALYSE=aan en compose.analyse.yaml.
func TestMediaPlatformCompose(t *testing.T) {
	tpl, c := mpContext(t, nil)
	files, _ := npmFiles(t, tpl, c)
	n := newMPNode(t, files)
	src := filepath.Join(n.dir, "src/infra/single")
	base := "compose.yaml compose.office.yaml compose.media.yaml"
	limits := map[string]string{"compose.office-limits.yaml": "", "compose.media-limits.yaml": "", "compose.analyse-limits.yaml": ""}
	for _, tc := range []struct {
		name  string
		state map[string]*string
		want  string
	}{
		{"0.8.0 zonder grenzen", map[string]*string{"docker-info": on("true true\n")}, base + " platform-cluster.yaml"},
		{"0.8.1, Docker zonder grenzen", map[string]*string{"src/infra/single/compose.analyse.yaml": on(""), "docker-info": on("false true\n")},
			base + " compose.analyse.yaml platform-analyse.yaml platform-cluster.yaml"},
		{"0.8.2 met grenzen", map[string]*string{"docker-info": on("true true\n")},
			base + " compose.office-limits.yaml compose.media-limits.yaml compose.analyse.yaml compose.analyse-limits.yaml platform-analyse.yaml platform-cluster.yaml"},
		{"0.8.2, docker info faalt", map[string]*string{"docker-info": nil},
			base + " compose.analyse.yaml platform-analyse.yaml platform-cluster.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if strings.HasPrefix(tc.name, "0.8.2") {
				for name, content := range limits {
					must(t, os.WriteFile(filepath.Join(src, name), []byte(content), 0o644))
				}
			}
			n.set(tc.state)
			if out, err := n.run("compose", "config"); err != nil {
				t.Fatalf("compose: %v %s", err, out)
			}
			if got := n.composeFiles(); got != tc.want {
				t.Errorf("bestanden:\n got %s\nwant %s", got, tc.want)
			}
		})
	}
	// Analyse uit: geen bestanden van de analyse, ook niet zijn grenzen.
	tpl, c = mpContext(t, map[string]any{"analyse": false})
	files, _ = npmFiles(t, tpl, c)
	n = newMPNode(t, files)
	src = filepath.Join(n.dir, "src/infra/single")
	for name := range limits {
		must(t, os.WriteFile(filepath.Join(src, name), nil, 0o644))
	}
	n.set(map[string]*string{"src/infra/single/compose.analyse.yaml": on(""), "docker-info": on("true true\n")})
	if out, err := n.run("compose", "ps"); err != nil {
		t.Fatalf("compose: %v %s", err, out)
	}
	if got, want := n.composeFiles(), base+" compose.office-limits.yaml compose.media-limits.yaml platform-cluster.yaml"; got != want {
		t.Errorf("analyse uit:\n got %s\nwant %s", got, want)
	}
}

// TestMediaPlatformUp: up start het platform alleen op de node met het VIP;
// zonder stopt het met 3, wat cf-platform.service als gewoon gestopt ziet.
func TestMediaPlatformUp(t *testing.T) {
	tpl, c := mpContext(t, nil)
	files, _ := npmFiles(t, tpl, c)
	n := newMPNode(t, files)
	hash := filepath.Join(n.dir, "hash")

	n.set(map[string]*string{"mounted": on("")})
	out, err := n.run("up")
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 3 || !strings.Contains(out, "heeft het VIP 192.168.10.150 niet") {
		t.Fatalf("up zonder VIP: %v\n%s", err, out)
	}
	if strings.Contains(n.log(), "docker") {
		t.Errorf("up zonder VIP raakte docker: %s", n.log())
	}
	if _, err := os.Stat(hash); err == nil {
		t.Error("up zonder VIP schreef de vingerafdruk")
	}

	n.set(map[string]*string{"vip": on(""), "mounted": nil})
	if out, err := n.run("up"); !errors.As(err, &ee) || ee.ExitCode() != 1 || !strings.Contains(out, "is niet gemount") {
		t.Fatalf("up zonder mount: %v\n%s", err, out)
	}

	n.set(map[string]*string{"mounted": on("")})
	if out, err := n.run("up"); err != nil {
		t.Fatalf("up: %v\n%s", err, out)
	}
	if l := n.log(); !strings.Contains(l, " up --no-build --remove-orphans\n") {
		t.Errorf("docker: %s", l)
	}
	if _, err := os.Stat(hash); err != nil {
		t.Errorf("geen vingerafdruk: %v", err)
	}
}

// De hulpteksten zeggen wat er echt gebeurt: Versie wijzigen bouwt niet.
func TestMediaPlatformHelp(t *testing.T) {
	tpl, _ := mpContext(t, nil)
	help := map[string]string{}
	for _, p := range tpl.Params {
		help[p.Name] = p.Help
	}
	for name, want := range map[string][]string{
		"platform_ref": {"Versie wijzigen bouwt niet; gebruik het commando Platform bijwerken.", "Nooit install.sh op een clusternode."},
		"analyse":      {"±4 GB vrij op de schijf", "12 tot 16 GB RAM per node"},
	} {
		for _, w := range want {
			if !strings.Contains(help[name], w) {
				t.Errorf("hulptekst van %s mist %q: %s", name, w, help[name])
			}
		}
	}
	if strings.Contains(help["platform_ref"], "bouwt de nieuwe versie") {
		t.Errorf("hulptekst van platform_ref: %s", help["platform_ref"])
	}
}

// TestMediaPlatformArr: qBittorrent en Prowlarr alleen met ARR=aan en een
// versie met compose.arr.yaml, na arr-koppel, met de downloads op de node.
func TestMediaPlatformArr(t *testing.T) {
	up := map[string]*string{"vip": on(""), "mounted": on(""), "healthy": on(""),
		"services": on("platform\noffice\nmedia\nqbittorrent\nprowlarr\n"), "cli-arr-koppel": on("qBittorrent: gekoppeld als gebruiker platform\n")}

	// Standaard uit, ook met compose.arr.yaml: up zoals altijd.
	tpl, c := mpContext(t, nil)
	files, _ := npmFiles(t, tpl, c)
	off := newMPNode(t, files)
	off.set(up)
	off.set(map[string]*string{"src/infra/single/compose.arr.yaml": on("")})
	if out, err := off.run("up"); err != nil {
		t.Fatalf("up: %v\n%s", err, out)
	}
	if l := off.log(); strings.Contains(l, "arr-koppel") || strings.Contains(l, "qbittorrent") || strings.Contains(l, "config --services") ||
		!strings.Contains(l, " up --no-build --remove-orphans\n") || strings.Contains(off.composeFiles(), "arr") {
		t.Errorf("ARR uit: %s", l)
	}
	if _, err := os.Stat(filepath.Join(off.dir, "cfp/torrents")); err == nil {
		t.Error("ARR uit maakte de map van de downloads")
	}

	tpl, c = mpContext(t, map[string]any{"arr": true})
	files, _ = npmFiles(t, tpl, c)
	if !strings.Contains(files["/etc/clusterforge/platform.env"], "\nARR=aan\n") {
		t.Errorf("platform.env: %s", files["/etc/clusterforge/platform.env"])
	}
	n := newMPNode(t, files)
	n.set(up)

	// Aan, maar een versie zonder compose.arr.yaml: up zoals altijd.
	if out, err := n.run("up"); err != nil {
		t.Fatalf("up: %v\n%s", err, out)
	}
	if l := n.log(); strings.Contains(l, "arr-koppel") || !strings.Contains(l, " up --no-build --remove-orphans\n") {
		t.Errorf("zonder compose.arr.yaml: %s", l)
	}

	// Aan, met compose.arr.yaml: eerst de app, dan arr-koppel, dan de twee.
	n.set(map[string]*string{"src/infra/single/compose.arr.yaml": on("")})
	out, err := n.run("up")
	if err != nil {
		t.Fatalf("up: %v\n%s", err, out)
	}
	l := n.log()
	app := strings.Index(l, " up --no-build --remove-orphans platform office media\n")
	koppel := strings.Index(l, " exec -T platform python -m app.cli arr-koppel\n")
	arr := strings.Index(l, " up -d --no-deps qbittorrent prowlarr\n")
	if app < 0 || koppel < 0 || arr < koppel || !strings.Contains(out, "Prowlarr op http://192.168.10.150:9696") {
		t.Errorf("volgorde: app %d, koppel %d, arr %d\n%s\n%s", app, koppel, arr, l, out)
	}
	if got, want := n.composeFiles(), "compose.yaml compose.office.yaml compose.media.yaml platform-cluster.yaml compose.arr.yaml platform-arr.yaml"; got != want {
		t.Errorf("bestanden:\n got %s\nwant %s", got, want)
	}
	torrents := filepath.Join(n.dir, "cfp/torrents")
	if fi, err := os.Stat(torrents); err != nil || fi.Mode().Perm() != 0o750 || !strings.Contains(l, "chown 10001:10001 "+torrents+"\n") {
		t.Errorf("map van de downloads: %v %v\n%s", fi, err, l)
	}
	if _, err := os.Stat(filepath.Join(n.dir, "data/import/torrents")); err != nil {
		t.Errorf("mountpunt in de importmap: %v", err)
	}

	// Koppelen lukt niet (of de app antwoordt niet): de twee starten niet.
	for name, state := range map[string]map[string]*string{
		"koppelen lukt niet": {"cli-arr-koppel": nil},
		"app antwoordt niet": {"healthy": nil},
	} {
		n.set(up)
		n.set(state)
		out, err := n.run("up")
		if err != nil || strings.Contains(n.log(), "qbittorrent prowlarr") || !strings.Contains(out, "qBittorrent en Prowlarr starten niet") {
			t.Errorf("%s: %v\n%s\n%s", name, err, out, n.log())
		}
	}

	// Een symlink als map van de downloads: niet starten.
	n.set(up)
	must(t, os.RemoveAll(torrents))
	must(t, os.Symlink(n.dir, torrents))
	if out, err := n.run("up"); err == nil || !strings.Contains(out, "is een symlink") || strings.Contains(n.log(), "compose") {
		t.Errorf("symlink: %v\n%s", err, out)
	}

	// down stopt ze, ook als ARR intussen uit staat.
	for _, node := range []*mpNode{n, off} {
		node.set(map[string]*string{"arr-running": on("")})
		if out, err := node.run("down"); err != nil {
			t.Fatalf("down: %v\n%s", err, out)
		}
		if l := node.log(); !strings.Contains(l, " stop\n") || !strings.Contains(l, "docker stop qbit1\n") || !strings.Contains(l, "docker stop prowl1\n") {
			t.Errorf("down: %s", l)
		}
	}
}

// TestMediaPlatformBlobSplit: aan zet PLATFORM_BLOB_SPLIT=true in platform.env.
func TestMediaPlatformBlobSplit(t *testing.T) {
	tpl, c := mpContext(t, map[string]any{"blob_split": true})
	files, _ := npmFiles(t, tpl, c)
	if env := files["/etc/clusterforge/platform.env"]; !strings.Contains(env, "\nPLATFORM_BLOB_SPLIT=true\n") || !strings.Contains(env, "\nARR=uit\n") {
		t.Errorf("platform.env:\n%s", env)
	}
}

// vol is een volume in /vol/status zoals een oudere SeaweedFS het schrijft
// (Id eerst); een nieuwere zet Collection eerst (hier een map).
type vol struct {
	Id               int
	Size             int
	ReplicaPlacement map[string]int
	Ttl              map[string]int
	Collection       string
}

func volStatus(t *testing.T, pretty bool, nodes map[string][]any) string {
	t.Helper()
	v := map[string]any{"Version": "30GB 3.97", "Volumes": map[string]any{"Free": 10, "Max": 30,
		"DataCenters": map[string]any{"dc1": map[string]any{"rack1": nodes}}}}
	var b []byte
	var err error
	if pretty {
		b, err = json.MarshalIndent(v, "", "  ")
	} else {
		b, err = json.Marshal(v)
	}
	must(t, err)
	return string(b)
}

// TestMediaPlatformReplicatie: de replicatie van ClusterForge geldt alleen
// voor de volumes zonder collectie; collectie media (films en series op 2
// kopieën) blijft zoals ze is, en de controle wordt er niet rood van.
func TestMediaPlatformReplicatie(t *testing.T) {
	tpl, c := mpContext(t, map[string]any{"replication": "002"})
	files, _ := npmFiles(t, tpl, c)
	n := newMPNode(t, files)
	media := func(id int) map[string]any {
		return map[string]any{"Id": id, "Collection": "media", "ReplicaPlacement": map[string]int{"node": 1}, "Ttl": nil, "DiskId": 0}
	}
	plain := func(id, copies int) map[string]any {
		return map[string]any{"Id": id, "Collection": "", "ReplicaPlacement": map[string]int{"node": copies}, "Ttl": map[string]int{"Count": 0}, "DiskId": 0}
	}
	nodes := func(two int) map[string][]any {
		return map[string][]any{
			"192.168.10.146:8081": {vol{Id: 1, ReplicaPlacement: map[string]int{"node": 2}, Ttl: map[string]int{"Count": 0, "Unit": 0}},
				vol{Id: 2, ReplicaPlacement: map[string]int{"node": two}}, vol{Id: 3, ReplicaPlacement: map[string]int{"node": 1}, Collection: "media"}},
			"192.168.10.147:8081": {media(3), plain(1, 2), plain(5, two), media(4)},
			"192.168.10.148:8081": {plain(1, 2), media(4), plain(5, two)},
		}
	}
	for _, pretty := range []bool{false, true} {
		n.set(map[string]*string{"leader": on(""), "vol-status": on(volStatus(t, pretty, nodes(1))), "weed-in": nil})
		if _, err := n.run("replicatie", "--check"); err == nil {
			t.Errorf("--check (pretty %v): volumes 2 en 5 staan op 001", pretty)
		}
		out, err := n.run("replicatie", "--nu")
		if err != nil || !strings.Contains(out, "2 volumes naar replicatie 002") {
			t.Fatalf("replicatie --nu: %v\n%s", err, out)
		}
		in, _ := os.ReadFile(filepath.Join(n.dir, "weed-in"))
		if want := "lock\nvolume.configure.replication -volumeId 2 -replication 002\n" +
			"volume.configure.replication -volumeId 5 -replication 002\nvolume.fix.replication\nunlock\n"; string(in) != want {
			t.Errorf("weed shell (pretty %v):\n%s", pretty, in)
		}
		if !strings.Contains(n.log(), "weed shell -master=127.0.0.1:9333") {
			t.Errorf("weed: %s", n.log())
		}
		// Volumes 2 en 5 omgezet: in orde, ook al staat media op 001.
		n.set(map[string]*string{"vol-status": on(volStatus(t, pretty, nodes(2)))})
		if out, err := n.run("replicatie", "--check"); err != nil {
			t.Errorf("--check na de omzetting (pretty %v): %v %s", pretty, err, out)
		}
		if out, err := n.run("replicatie"); err != nil || !strings.Contains(out, "Alle volumes staan al op 002") {
			t.Errorf("replicatie: %v %s", err, out)
		}
	}
	// Niet de leider: niets te doen.
	n.set(map[string]*string{"leader": nil, "vol-status": on(volStatus(t, false, nodes(1)))})
	if out, err := n.run("replicatie", "--check"); err != nil {
		t.Errorf("--check zonder leider: %v %s", err, out)
	}
	// Op de achtergrond, in een eigen unit.
	n.set(map[string]*string{"leader": on("")})
	if out, err := n.run("replicatie"); err != nil || !strings.Contains(n.log(), "systemd-run --unit cf-platform-replicatie") {
		t.Errorf("replicatie: %v %s\n%s", err, out, n.log())
	}
}

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
