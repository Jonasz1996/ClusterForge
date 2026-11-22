package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// k3sConfigInfo is wat de agent uit /etc/rancher/k3s/config.yaml leest.
type k3sConfigInfo struct {
	NodeName string
	// Init: cluster-init staat aan; deze server begon het cluster.
	Init bool
	// Join: server: staat erin; deze node meldt zich aan bij een ander.
	Join bool
}

// readK3sConfig leest de config van k3s regel voor regel. ClusterForge
// schrijft ze zelf, met één sleutel per regel.
func readK3sConfig(root string) (k3sConfigInfo, error) {
	var c k3sConfigInfo
	b, err := os.ReadFile(filepath.Join(root, k3sConfig))
	if err != nil {
		return c, err
	}
	for line := range strings.Lines(string(b)) {
		key, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || strings.HasPrefix(key, "#") {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch key {
		case "node-name":
			c.NodeName = v
		case "cluster-init":
			c.Init = v == "true"
		case "server":
			c.Join = v != ""
		}
	}
	return c, nil
}

// snapshotOutput is hoeveel regels van k3s of systemctl de agent
// terugstuurt.
const snapshotOutput = 20

// snapshot maakt een etcd-snapshot of doet een stap van het terugzetten.
// De agent kent alleen de vaste commando's uit protocol: de server kiest
// alleen de naam van een nieuwe snapshot en het bestand om terug te zetten,
// en beide hebben een vaste vorm. Wat de database van etcd raakt, doet hij
// alleen als k3s stilstaat.
func (a *Agent) snapshot(ctx context.Context, req protocol.KubeRequest) protocol.KubeResponse {
	s := req.Snapshot
	if s == nil {
		return protocol.KubeResponse{Error: "ongeldig snapshotverzoek"}
	}
	root := rootOr(a.kubeRoot())
	if !exists(root, k3sServerUnit) && !exists(root, K3sKubeconfig) {
		return protocol.KubeResponse{Error: "deze node is geen k3s-server"}
	}
	var res protocol.KubeSnapshotResult
	var err error
	switch s.Action {
	case protocol.KubeSnapshotCheck:
		cfg, cerr := readK3sConfig(root)
		if cerr != nil {
			return protocol.KubeResponse{Error: "de config van k3s is niet te lezen: " + cerr.Error()}
		}
		res.Init, res.Join = cfg.Init, cfg.Join
		res.Active = a.k3sActive(ctx)
	case protocol.KubeSnapshotSave:
		if !protocol.KubeSnapshotNamePattern.MatchString(s.Name) {
			return protocol.KubeResponse{Error: fmt.Sprintf("ongeldige naam van een snapshot %q", s.Name)}
		}
		res.Output, err = a.k3sRun(ctx, "k3s", "etcd-snapshot", "save", "--name", s.Name)
	case protocol.KubeSnapshotStop:
		if res.Output, err = a.k3sRun(ctx, "systemctl", "stop", "k3s"); err == nil {
			err = a.k3sStopped(ctx)
		}
	case protocol.KubeSnapshotStart:
		res.Output, err = a.k3sRun(ctx, "systemctl", "start", "k3s")
	case protocol.KubeSnapshotReset:
		res.Output, err = a.snapshotReset(ctx, root, s.File)
	case protocol.KubeSnapshotRejoin:
		res, err = a.snapshotRejoin(ctx, root)
	default:
		return protocol.KubeResponse{Error: fmt.Sprintf("onbekende snapshotstap %q", s.Action)}
	}
	if err != nil {
		msg := err.Error()
		if n := len(res.Output); n > 0 {
			msg += ": " + res.Output[n-1]
		}
		return protocol.KubeResponse{Error: msg}
	}
	body, err := json.Marshal(res)
	if err != nil {
		return protocol.KubeResponse{Error: err.Error()}
	}
	return protocol.KubeResponse{Status: http.StatusOK, Body: body}
}

func exists(root, p string) bool {
	_, err := os.Stat(filepath.Join(root, p))
	return err == nil
}

// k3sRun voert een vast commando uit en geeft de laatste regels van de
// uitvoer.
func (a *Agent) k3sRun(ctx context.Context, name string, args ...string) ([]string, error) {
	out, err := a.execTimeout(ctx, protocol.KubeSnapshotTimeout, name, args...)
	var lines []string
	for l := range strings.Lines(string(out)) {
		if l = strings.TrimSpace(l); l != "" {
			if len(l) > 300 {
				l = l[:300] + "…"
			}
			lines = append(lines, l)
		}
	}
	if len(lines) > snapshotOutput {
		lines = lines[len(lines)-snapshotOutput:]
	}
	if err != nil {
		// De naam van het commando zonder vlaggen, zoals k3s server.
		cmd := []string{name}
		for _, arg := range args {
			if strings.HasPrefix(arg, "-") || len(cmd) == 3 {
				break
			}
			cmd = append(cmd, arg)
		}
		return lines, fmt.Errorf("%s mislukte (%v)", strings.Join(cmd, " "), err)
	}
	return lines, nil
}

// k3sActive is wat systemctl is-active k3s zegt, zoals active of
// inactive. Het geeft een exitcode als k3s niet draait; de tekst telt.
func (a *Agent) k3sActive(ctx context.Context) string {
	out, _ := a.execTimeout(ctx, 30*time.Second, "systemctl", "is-active", "k3s")
	return strings.TrimSpace(string(out))
}

// k3sStopped is nil als k3s stilstaat.
func (a *Agent) k3sStopped(ctx context.Context) error {
	switch st := a.k3sActive(ctx); st {
	case "inactive", "failed":
		return nil
	default:
		return fmt.Errorf("k3s staat niet stil (systemctl is-active k3s: %s); stop k3s eerst", st)
	}
}

// snapshotReset zet het cluster terug naar een snapshot: k3s maakt een
// nieuw etcd met één lid uit het bestand, en zet de oude database opzij.
// Dat mag alleen op de server die het cluster begon: de andere melden zich
// via het VIP aan, en een server met cluster-init zou met een lege database
// een nieuw cluster beginnen.
func (a *Agent) snapshotReset(ctx context.Context, root, file string) ([]string, error) {
	if !protocol.KubeSnapshotFilePattern.MatchString(file) {
		return nil, fmt.Errorf("ongeldige naam van een snapshot %q", file)
	}
	cfg, err := readK3sConfig(root)
	if err != nil {
		return nil, fmt.Errorf("de config van k3s is niet te lezen: %w", err)
	}
	if !cfg.Init || cfg.Join {
		return nil, errors.New("terugzetten kan alleen op de server die het cluster begon (cluster-init in " + k3sConfig + ")")
	}
	if err := a.k3sStopped(ctx); err != nil {
		return nil, err
	}
	p := path.Join(protocol.KubeSnapshotDir, file)
	st, err := os.Lstat(filepath.Join(root, p))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%s bestaat niet op deze node", p)
	case err != nil:
		return nil, err
	case !st.Mode().IsRegular():
		return nil, fmt.Errorf("%s is geen gewoon bestand", p)
	}
	return a.k3sRun(ctx, "k3s", "server", "--cluster-reset", "--cluster-reset-restore-path="+p)
}

// snapshotRejoin zet de database van etcd opzij op een server die zich
// aanmeldt via server:, zoals k3s het na een cluster-reset vraagt. De
// snapshots gaan mee naar de nieuwe map, zodat ze blijven bestaan.
func (a *Agent) snapshotRejoin(ctx context.Context, root string) (protocol.KubeSnapshotResult, error) {
	var res protocol.KubeSnapshotResult
	cfg, err := readK3sConfig(root)
	if err != nil {
		return res, fmt.Errorf("de config van k3s is niet te lezen: %w", err)
	}
	switch {
	case cfg.Init:
		return res, errors.New("deze server begon het cluster; zijn database blijft staan")
	case !cfg.Join:
		return res, errors.New("in " + k3sConfig + " staat geen server:, dus k3s weet niet bij wie deze server zich opnieuw aanmeldt")
	}
	if err := a.k3sStopped(ctx); err != nil {
		return res, err
	}
	db := filepath.Join(root, protocol.KubeDBDir)
	if _, err := os.Stat(filepath.Join(db, "etcd")); errors.Is(err, fs.ErrNotExist) {
		res.Output = []string{"geen database van etcd in " + protocol.KubeDBDir + "; niets opzij te zetten"}
		return res, nil
	}
	moved := protocol.KubeDBDir + "-voor-terugzetten-" + time.Now().Format("20060102-150405")
	if err := os.Rename(db, filepath.Join(root, moved)); err != nil {
		return res, err
	}
	if err := os.MkdirAll(db, 0o700); err != nil {
		return res, err
	}
	res.Moved = moved
	res.Output = []string{"de oude database staat nu in " + moved}
	old := filepath.Join(root, moved, "snapshots")
	if _, err := os.Stat(old); err == nil {
		if err := os.Rename(old, filepath.Join(db, "snapshots")); err != nil {
			return res, err
		}
		res.Output = append(res.Output, "de snapshots staan weer in "+protocol.KubeSnapshotDir)
	}
	return res, nil
}
