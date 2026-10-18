package agent

// Bestandssync (fase 6, agentprotocol 21). Een beheerde job van de soort
// sync draait rsync over SSH van deze node naar elk doel. De sleutel van de
// bron staat in protocol.SyncDir en verlaat de node nooit; op een doel mag
// hij alleen `cf-agent sync-receive` (zie SyncReceiver), dat alleen rsync
// naar de doelmap van een sync van die bron toelaat.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// syncSSH is het ssh-commando van rsync, met de sleutel en known_hosts in
// root.
func syncSSH(root string) string {
	p := func(name string) string { return filepath.Join(root, protocol.SyncDir, name) }
	return "ssh -i " + p("id_ed25519") + " -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes" +
		" -o UserKnownHostsFile=" + p("known_hosts") + " -o ConnectTimeout=15 -o ServerAliveInterval=30"
}

// syncArgv is het rsync-commando van een sync naar één doel.
func syncArgv(spec *protocol.SyncSpec, t protocol.SyncTarget, sshCmd string, extra ...string) []string {
	argv := []string{"rsync", "-a", "--no-human-readable", "-e", sshCmd}
	argv = append(argv, extra...)
	if spec.Delete {
		argv = append(argv, "--delete")
	}
	for _, ex := range spec.Exclude {
		argv = append(argv, "--exclude="+ex)
	}
	if spec.BwLimitKB > 0 {
		argv = append(argv, "--bwlimit="+strconv.Itoa(spec.BwLimitKB))
	}
	host := t.Address
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	// rsync zet zelf een backslash voor spaties in het pad op het doel.
	return append(argv, spec.Source, "root@"+host+":"+spec.Target)
}

var (
	statFiles   = regexp.MustCompile(`(?m)^Number of regular files transferred: (\d+)`)
	statBytes   = regexp.MustCompile(`(?m)^Total transferred file size: (\d+)`)
	statDeleted = regexp.MustCompile(`(?m)^Number of deleted files: (\d+)`)
)

func statInt(re *regexp.Regexp, s string) int64 {
	if m := re.FindStringSubmatch(s); m != nil {
		v, _ := strconv.ParseInt(m[1], 10, 64)
		return v
	}
	return 0
}

// runSync draait een sync naar elk doel na elkaar. Een doel dat weigert
// (het heeft het VIP van zijn cluster) telt niet als fout, tenzij elk doel
// weigert.
func (r *CronRunner) runSync(ctx context.Context, job protocol.ManagedCron, out io.Writer) (int, []protocol.SyncResult, error) {
	spec := job.Sync
	if spec == nil || len(spec.Targets) == 0 {
		_, _ = fmt.Fprintln(out, "Deze sync heeft geen doelen.")
		return 1, nil, nil
	}
	sshCmd := r.SSH
	if sshCmd == "" {
		sshCmd = syncSSH(r.Root)
	}
	var results []protocol.SyncResult
	exit, refused := 0, 0
	for _, t := range spec.Targets {
		if ctx.Err() != nil {
			break
		}
		_, _ = fmt.Fprintf(out, "== %s naar %s (%s) ==\n", spec.Source, t.Hostname, t.Address)
		var buf bytes.Buffer
		code, err := runCronArgv(ctx, "", syncArgv(spec, t, sshCmd, "--stats"), io.MultiWriter(out, &buf))
		text := buf.String()
		res := protocol.SyncResult{NodeID: t.NodeID, Hostname: t.Hostname, Exit: code}
		switch {
		case err != nil:
			res.Result, res.Error = protocol.SyncFailed, err.Error()
		case strings.Contains(text, protocol.SyncRefused):
			res.Result, res.Error = protocol.SyncRefusal, lastLineWith(text, protocol.SyncRefused)
			refused++
		// 24: bestanden verdwenen tijdens de kopie; de rest kwam over.
		case code == 0 || code == 24:
			res.Result = protocol.SyncOK
			res.Files, res.Bytes, res.Deleted = int(statInt(statFiles, text)), statInt(statBytes, text), int(statInt(statDeleted, text))
		default:
			res.Result, res.Error = protocol.SyncFailed, lastLineWith(text, "rsync")
		}
		if res.Result == protocol.SyncFailed && exit == 0 {
			exit = max(code, 1)
		}
		results = append(results, res)
		_, _ = fmt.Fprintln(out)
	}
	if exit == 0 && refused == len(spec.Targets) {
		exit = 1
	}
	return exit, results, nil
}

// lastLineWith is de laatste regel van s met needle, zonder de rest.
func lastLineWith(s, needle string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], needle) {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}

// validSync controleert de sync van een job.
func validSync(j protocol.ManagedCron) error {
	s := j.Sync
	switch {
	case s == nil:
		return fmt.Errorf("job %s: een sync zonder mappen", j.ID)
	case j.User != "root":
		return fmt.Errorf("job %s: een sync draait als root", j.ID)
	case !protocol.ValidSyncPath(s.Source) || !protocol.ValidSyncPath(s.Target):
		return fmt.Errorf("job %s: ongeldige map", j.ID)
	}
	for _, ex := range s.Exclude {
		if ex == "" || strings.ContainsAny(ex, "\n\r") {
			return fmt.Errorf("job %s: ongeldige uitzondering", j.ID)
		}
	}
	for _, t := range s.Targets {
		if net.ParseIP(t.Address) == nil {
			return fmt.Errorf("job %s: ongeldig adres %q", j.ID, t.Address)
		}
		if _, err := uuid.Parse(t.NodeID); err != nil {
			return fmt.Errorf("job %s: ongeldig doel", j.ID)
		}
	}
	return nil
}

// syncState zet wat deze node voor syncs nodig heeft: known_hosts met de
// hostsleutels van de doelen van zijn jobs, en voor wat hij ontvangt
// receive.json en het blok in authorized_keys. Zonder syncs gaat alles weg.
func (a *Agent) syncState(jobs []protocol.ManagedCron, receives []protocol.SyncReceive) ([]string, error) {
	var out []string
	dir, err := a.path(protocol.SyncDir)
	if err != nil {
		return nil, err
	}
	put := func(name string, data []byte, perm os.FileMode) error {
		p := filepath.Join(dir, name)
		if len(data) == 0 {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return nil
		}
		if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, data) {
			return nil
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		return writeFileAtomic(p, data, perm)
	}

	var hosts []string
	for _, j := range jobs {
		if j.Sync == nil {
			continue
		}
		for _, t := range j.Sync.Targets {
			for _, k := range t.HostKeys {
				if f := strings.Fields(k); len(f) >= 2 {
					hosts = append(hosts, t.Address+" "+f[0]+" "+f[1])
				}
			}
		}
	}
	sort.Strings(hosts)
	hosts = slices.Compact(hosts)
	known := ""
	if len(hosts) > 0 {
		known = strings.Join(hosts, "\n") + "\n"
	}
	if err := put("known_hosts", []byte(known), 0o644); err != nil {
		return nil, err
	}

	for _, rc := range receives {
		if _, err := uuid.Parse(rc.From); err != nil || !protocol.ValidSyncPath(rc.Path) {
			return nil, fmt.Errorf("ongeldige sync naar deze node van %q", rc.FromHost)
		}
		if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(rc.PublicKey)); err != nil {
			return nil, fmt.Errorf("ongeldige sleutel van %s: %v", rc.FromHost, err)
		}
		for _, ad := range rc.Addresses {
			if net.ParseIP(ad) == nil {
				return nil, fmt.Errorf("ongeldig adres %q van %s", ad, rc.FromHost)
			}
		}
	}
	var recv []byte
	if len(receives) > 0 {
		recv, _ = json.MarshalIndent(receives, "", "  ")
		recv = append(recv, '\n')
	}
	if err := put("receive.json", recv, 0o600); err != nil {
		return nil, err
	}
	changed, err := a.syncAuthorizedKeys(receives)
	if err != nil {
		return nil, err
	}
	if changed {
		out = append(out, fmt.Sprintf("authorized_keys: %d bron(nen) voor syncs", len(syncSources(receives))))
	}
	return out, nil
}

type syncSource struct {
	from, host, key string
	addrs           []string
}

// syncSources voegt de syncs per bron samen: één sleutel per bron-node.
func syncSources(receives []protocol.SyncReceive) []syncSource {
	by := map[string]*syncSource{}
	var order []string
	for _, rc := range receives {
		s := by[rc.From]
		if s == nil {
			s = &syncSource{from: rc.From, host: rc.FromHost, key: strings.TrimSpace(rc.PublicKey)}
			by[rc.From] = s
			order = append(order, rc.From)
		}
		s.addrs = append(s.addrs, rc.Addresses...)
	}
	sort.Strings(order)
	out := make([]syncSource, 0, len(order))
	for _, f := range order {
		s := by[f]
		sort.Strings(s.addrs)
		s.addrs = slices.Compact(s.addrs)
		out = append(out, *s)
	}
	return out
}

// syncAuthorizedKeys zet het blok van ClusterForge in authorized_keys van
// root; de rest van het bestand blijft zoals het is.
func (a *Agent) syncAuthorizedKeys(receives []protocol.SyncReceive) (bool, error) {
	p, err := a.path(protocol.SyncAuthorizedKeys)
	if err != nil {
		return false, err
	}
	old, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	var keep []string
	in := false
	for _, l := range strings.Split(strings.TrimRight(string(old), "\n"), "\n") {
		switch {
		case strings.TrimSpace(l) == protocol.SyncBlockBegin:
			in = true
		case strings.TrimSpace(l) == protocol.SyncBlockEnd:
			in = false
		case !in && (l != "" || len(keep) > 0):
			keep = append(keep, l)
		}
	}
	if srcs := syncSources(receives); len(srcs) > 0 {
		keep = append(keep, protocol.SyncBlockBegin)
		for _, s := range srcs {
			f := strings.Fields(s.key)
			keep = append(keep, fmt.Sprintf(`restrict,from="%s",command="%s sync-receive --from %s" %s %s cf-sync-%s`,
				strings.Join(s.addrs, ","), protocol.VerifyPath, s.from, f[0], f[1], s.host))
		}
		keep = append(keep, protocol.SyncBlockEnd)
	}
	data := ""
	if len(keep) > 0 {
		data = strings.Join(keep, "\n") + "\n"
	}
	if data == string(old) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return false, err
	}
	if data == "" {
		return true, os.Remove(p)
	}
	return true, writeFileAtomic(p, []byte(data), 0o600)
}

// cronKeys geeft de sleutel voor syncs (en maakt hem), de hostsleutels en
// het plan van de node.
func (a *Agent) cronKeys(ctx context.Context, paths []string) protocol.CronResponse {
	res := protocol.CronResponse{Entries: []protocol.CronEntry{}}
	dir, err := a.path(protocol.SyncDir)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	pub, err := ensureSyncKey(filepath.Join(dir, "id_ed25519"))
	if err != nil {
		res.Error = "sleutel voor syncs: " + err.Error()
		return res
	}
	k := &protocol.SyncKeys{PublicKey: pub, HostKeys: []string{}, Free: map[string]int64{}}
	hostDir, _ := a.path("/etc/ssh")
	files, _ := filepath.Glob(filepath.Join(hostDir, "ssh_host_*_key.pub"))
	sort.Strings(files)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if fl := strings.Fields(string(data)); len(fl) >= 2 {
			k.HostKeys = append(k.HostKeys, fl[0]+" "+fl[1])
		}
	}
	_, err = a.exec(ctx, "rsync", "--version")
	k.Rsync = err == nil
	_, err = a.exec(ctx, "ssh", "-V")
	k.SSH = err == nil
	for _, unit := range []string{"ssh", "sshd"} {
		if out, err := a.exec(ctx, "systemctl", "is-active", unit); err == nil && strings.TrimSpace(string(out)) == "active" {
			k.SSHD = true
		}
	}
	if out, err := a.exec(ctx, "sshd", "-T"); err == nil {
		k.PermitRootLogin = sshdOption(string(out), "permitrootlogin")
	}
	if k.PermitRootLogin == "" {
		if cfg, err := a.path("/etc/ssh/sshd_config"); err == nil {
			if data, err := os.ReadFile(cfg); err == nil {
				k.PermitRootLogin = sshdOption(string(data), "permitrootlogin")
				if k.PermitRootLogin == "" {
					// De standaard van OpenSSH.
					k.PermitRootLogin = "prohibit-password"
				}
			}
		}
	}
	for _, p := range paths {
		if !protocol.ValidSyncPath(p) {
			continue
		}
		k.Free[p] = a.freeBytes(p)
	}
	res.Keys = k
	return res
}

// sshdOption leest een optie uit sshd -T of sshd_config: de eerste telt.
func sshdOption(text, name string) string {
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && strings.EqualFold(f[0], name) {
			return strings.ToLower(f[1])
		}
	}
	return ""
}

// freeBytes is de vrije ruimte bij p, of bij de dichtstbij bestaande map.
func (a *Agent) freeBytes(p string) int64 {
	for {
		full, err := a.path(path.Clean(p))
		if err != nil {
			return -1
		}
		var st syscall.Statfs_t
		if err := syscall.Statfs(full, &st); err == nil {
			return int64(st.Bavail) * int64(st.Bsize)
		}
		if p == "/" {
			return -1
		}
		p = path.Dir(path.Clean(p))
	}
}

// ensureSyncKey maakt de sleutel als hij er niet is, en geeft de publieke
// sleutel.
func ensureSyncKey(p string) (string, error) {
	if data, err := os.ReadFile(p); err == nil {
		key, err := ssh.ParsePrivateKey(data)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key.PublicKey()))), nil
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	host, _ := os.Hostname()
	block, err := ssh.MarshalPrivateKey(priv, "cf-sync@"+host)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	if err := writeFileAtomic(p, pem.EncodeToMemory(block), 0o600); err != nil {
		return "", err
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp)))
	_ = writeFileAtomic(p+".pub", []byte(line+" cf-sync@"+host+"\n"), 0o644)
	return line, nil
}

// cronDryRun draait een sync als proef naar één doel.
func (a *Agent) cronDryRun(ctx context.Context, jobID, target string) protocol.CronResponse {
	res := protocol.CronResponse{Entries: []protocol.CronEntry{}}
	if _, err := uuid.Parse(jobID); err != nil {
		res.Error = "ongeldige job-id"
		return res
	}
	p, err := a.path(path.Join(protocol.CronJobDir, jobID+".json"))
	if err != nil {
		res.Error = err.Error()
		return res
	}
	data, err := os.ReadFile(p)
	if err != nil {
		res.Error = "deze sync staat nog niet op de node"
		return res
	}
	var job protocol.ManagedCron
	if err := json.Unmarshal(data, &job); err != nil || job.Sync == nil {
		res.Error = "deze job is geen sync"
		return res
	}
	i := slices.IndexFunc(job.Sync.Targets, func(t protocol.SyncTarget) bool { return t.NodeID == target })
	if i < 0 {
		res.Error = "die node is geen doel van deze sync"
		return res
	}
	sshCmd := a.SyncSSH
	if sshCmd == "" {
		sshCmd = syncSSH(a.Root)
	}
	var buf bytes.Buffer
	code, err := runCronArgv(ctx, "", syncArgv(job.Sync, job.Sync.Targets[i], sshCmd, "--dry-run", "--itemize-changes"), &buf)
	text := protocol.MaskSecrets(buf.String())
	switch {
	case err != nil:
		res.Error = err.Error()
	case strings.Contains(text, protocol.SyncRefused):
		res.Error = lastLineWith(text, protocol.SyncRefused)
	case code != 0 && code != 24:
		res.Error = fmt.Sprintf("rsync stopte met code %d: %s", code, lastLineWith(text, "rsync"))
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	if len(lines) > protocol.MaxSyncDryRun {
		lines = append(lines[:protocol.MaxSyncDryRun], fmt.Sprintf("… en nog %d regels", len(lines)-protocol.MaxSyncDryRun))
	}
	res.Output = lines
	return res
}
