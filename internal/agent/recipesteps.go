package agent

// Stappen van recepten (fase 9, protocol 24): een pakketbron via haar
// release-pakket of via een eigen sleutel, en één sleutel=waarde in een
// configuratiebestand.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// maxRepoDownload is de grootste download van een release-pakket of
// sleutel.
const maxRepoDownload = 64 << 20

var (
	repoName   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	debName    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+._~-]*\.deb$`)
	iniKey     = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	suiteField = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
)

func (a *Agent) fetchRepo(ctx context.Context, raw string, httpsOnly bool) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && (httpsOnly || u.Scheme != "http")) {
		if httpsOnly {
			return nil, fmt.Errorf("%q is geen https-URL", raw)
		}
		return nil, fmt.Errorf("%q is geen http(s)-URL", raw)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	client := a.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", raw, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", raw, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxRepoDownload+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", raw, err)
	}
	if len(b) > maxRepoDownload {
		return nil, fmt.Errorf("download %s: groter dan 64 MiB", raw)
	}
	return b, nil
}

func (a *Agent) applyRepoDeb(ctx context.Context, r *protocol.RepoDebStep) (bool, []string, error) {
	u, err := url.Parse(r.URL)
	if err != nil {
		return false, nil, fmt.Errorf("URL %q: %w", r.URL, err)
	}
	base := path.Base(u.Path)
	if !debName.MatchString(base) {
		return false, nil, fmt.Errorf("%q wijst niet naar een .deb", r.URL)
	}
	data, err := a.fetchRepo(ctx, r.URL, false)
	if err != nil {
		return false, nil, err
	}
	dir, err := a.path("/var/lib/clusterforge/repo")
	if err != nil {
		return false, nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, nil, err
	}
	file := filepath.Join(dir, base)
	if err := os.WriteFile(file, data, 0o644); err != nil {
		return false, nil, err
	}
	defer func() { _ = os.Remove(file) }()
	out, err := a.exec(ctx, "dpkg-deb", "-f", file, "Package", "Version")
	if err != nil {
		return false, tail(out), fmt.Errorf("%s is geen geldig pakket: %w", base, err)
	}
	fields := map[string]string{}
	for line := range strings.SplitSeq(string(out), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			fields[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	pkg, version := fields["Package"], fields["Version"]
	if !packageName.MatchString(pkg) || version == "" {
		return false, tail(out), fmt.Errorf("%s: geen pakketnaam of versie", base)
	}
	if states, err := a.packageStates(ctx, []string{pkg}); err == nil && states[pkg].Installed && states[pkg].Version == version {
		return false, []string{pkg + " " + version + ": al geïnstalleerd"}, nil
	}
	out, err = a.execTimeout(ctx, 5*time.Minute, "env", "DEBIAN_FRONTEND=noninteractive", "dpkg", "-i",
		"--force-confdef", "--force-confold", file)
	if err != nil {
		return false, tail(out), aptError("dpkg -i "+base, out, err)
	}
	lines := append(tail(out), "geïnstalleerd: "+pkg+" "+version)
	upd, err := a.aptUpdate(ctx, nil)
	lines = append(lines, upd...)
	if err != nil {
		return true, lines, err
	}
	return true, append(lines, "apt-get update"), nil
}

func (a *Agent) applyRepo(ctx context.Context, r *protocol.RepoStep) (bool, []string, error) {
	if !repoName.MatchString(r.Name) {
		return false, nil, fmt.Errorf("ongeldige naam %q", r.Name)
	}
	if !suiteField.MatchString(r.Suite) || len(r.Components) == 0 {
		return false, nil, errors.New("een bron heeft een suite en minstens één onderdeel nodig")
	}
	for _, c := range r.Components {
		if !suiteField.MatchString(c) {
			return false, nil, fmt.Errorf("ongeldig onderdeel %q", c)
		}
	}
	if u, err := url.Parse(r.URL); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || strings.ContainsAny(r.URL, " \n") {
		return false, nil, fmt.Errorf("%q is geen http(s)-URL", r.URL)
	}
	key, err := a.fetchRepo(ctx, r.Key, true)
	if err != nil {
		return false, nil, err
	}
	keyPath := "/etc/apt/keyrings/cf-" + r.Name + ".asc"
	if !strings.HasPrefix(string(key), "-----BEGIN PGP PUBLIC KEY BLOCK-----") {
		keyPath = "/etc/apt/keyrings/cf-" + r.Name + ".gpg"
	}
	kc, klines, err := a.applyFile(ctx, &protocol.FileStep{Path: keyPath, Content: string(key), Mode: "0644"})
	if err != nil {
		return false, klines, err
	}
	src := fmt.Sprintf("Types: deb\nURIs: %s\nSuites: %s\nComponents: %s\nSigned-By: %s\n", r.URL, r.Suite, strings.Join(r.Components, " "), keyPath)
	sc, slines, err := a.applyFile(ctx, &protocol.FileStep{Path: "/etc/apt/sources.list.d/cf-" + r.Name + ".sources", Content: src, Mode: "0644"})
	lines := append(klines, slines...)
	if err != nil {
		return kc, lines, err
	}
	if !kc && !sc {
		return false, lines, nil
	}
	out, err := a.execTimeout(ctx, 5*time.Minute, "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "update", "-q")
	if err != nil {
		return true, append(lines, tail(out)...), aptError("apt-get update", out, err)
	}
	return true, append(lines, "apt-get update"), nil
}

func (a *Agent) applyIniSet(ctx context.Context, s *protocol.IniSetStep) (bool, []string, error) {
	if !iniKey.MatchString(s.Key) {
		return false, nil, fmt.Errorf("ongeldige sleutel %q", s.Key)
	}
	if strings.ContainsAny(s.Value, "\n\r") {
		return false, nil, errors.New("een waarde past op één regel")
	}
	p, err := a.path(s.Path)
	if err != nil {
		return false, nil, err
	}
	old, err := os.ReadFile(p)
	mode := s.Mode
	switch {
	case err == nil:
		if mode == "" {
			if fi, err := os.Stat(p); err == nil {
				mode = fmt.Sprintf("%04o", fi.Mode().Perm())
			}
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return false, nil, err
	}
	content := iniSet(string(old), s.Key, s.Value)
	changed, lines, err := a.applyFile(ctx, &protocol.FileStep{Path: s.Path, Content: content, Mode: mode, Owner: s.Owner, Group: s.Group})
	// De uitvoer zegt welke sleutel, nooit de waarde: die kan een geheim
	// zijn.
	switch {
	case err != nil:
	case !changed:
		lines = []string{s.Key + " in " + s.Path + ": ongewijzigd"}
	default:
		lines = []string{s.Key + " in " + s.Path + ": gezet"}
	}
	return changed, lines, err
}

// iniSet zet key=value in de tekst van een configuratiebestand. Een actieve
// regel met die sleutel krijgt de nieuwe waarde (de eerste; latere
// verdwijnen, zodat er geen tweede waarde wint); anders komt de regel na
// een uitgecommentarieerde regel met die sleutel, of onderaan. De stijl van
// de bestaande regel (spaties rond =) blijft.
func iniSet(text, key, value string) string {
	active := regexp.MustCompile(`^(\s*)` + regexp.QuoteMeta(key) + `(\s*)=(\s*)`)
	commented := regexp.MustCompile(`^\s*[#;]\s*` + regexp.QuoteMeta(key) + `\s*=`)
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	set, comment := -1, -1
	var out []string
	for _, l := range lines {
		if m := active.FindStringSubmatch(l); m != nil {
			if set >= 0 {
				continue
			}
			set = len(out)
			out = append(out, m[1]+key+m[2]+"="+m[3]+value)
			continue
		}
		if comment < 0 && commented.MatchString(l) {
			comment = len(out)
		}
		out = append(out, l)
	}
	if set < 0 {
		line := key + "=" + value
		if comment >= 0 {
			out = append(out[:comment+1], append([]string{line}, out[comment+1:]...)...)
		} else {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n") + "\n"
}
