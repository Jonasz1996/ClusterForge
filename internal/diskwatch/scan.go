package diskwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Scan is het resultaat van een meting, zoals het in disk_scans staat.
type Scan struct {
	ScannedAt time.Time  `json:"scanned_at"`
	JobID     uuid.UUID  `json:"job_id"`
	Mounts    []Mount    `json:"mounts"`
	Items     []Item     `json:"items"`
	Dirs      []Dir      `json:"dirs"`
	CleanedAt *time.Time `json:"cleaned_at,omitempty"`
	Freed     *int64     `json:"freed,omitempty"`
	Cleaned   []string   `json:"cleaned,omitempty"`
}

type Mount struct {
	Mount string `json:"mount"`
	Size  int64  `json:"size"`
	Used  int64  `json:"used"`
	Avail int64  `json:"avail"`
}

type Dir struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// Item is één onderdeel dat je kunt opruimen.
type Item struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Explain string `json:"explain"`
	Bytes   int64  `json:"bytes"`
	Safe    bool   `json:"safe"`
	Default bool   `json:"default"`
}

// catalog beschrijft elk onderdeel in gewone taal. Default is het veilige:
// opruimen raakt niets dat een dienst of jijzelf later nog nodig hebt.
type entry struct {
	title, explain string
	safe, def      bool
	// script ruimt het onderdeel op; vaste tekst, nooit invoer.
	script string
}

var catalog = map[string]entry{
	"journal": {
		title: "Systeemlogboek (journal)",
		explain: "Het logboek van alle diensten. Opruimen houdt de nieuwste 200 MB en gooit oudere regels weg. " +
			"Recente fouten blijven dus zichtbaar.",
		safe: true, def: true,
		script: `command -v journalctl >/dev/null && journalctl --vacuum-size=200M`,
	},
	"apt": {
		title: "Gedownloade updates (apt-cache)",
		explain: "Installatiebestanden van updates die al geïnstalleerd zijn. Die zijn niet meer nodig; " +
			"bij een volgende update downloadt apt opnieuw wat hij nodig heeft.",
		safe: true, def: true,
		script: `command -v apt-get >/dev/null && apt-get clean`,
	},
	"oldlogs": {
		title: "Oude, ingepakte logbestanden",
		explain: "Logbestanden die al geroteerd zijn (zoals syslog.2.gz) en ouder dan 7 dagen. " +
			"De huidige logbestanden blijven staan.",
		safe: true, def: true,
		script: `find /var/log -xdev -type f \( -name '*.gz' -o -name '*.[0-9]' -o -name '*.[0-9].log' -o -name '*.old' \) -mtime +7 -delete`,
	},
	"coredumps": {
		title:   "Crashbestanden (core dumps)",
		explain: "Geheugenafdrukken die achterblijven als een programma crasht. Alleen nuttig om een crash te onderzoeken.",
		safe:    true, def: true,
		script: `[ -d /var/lib/systemd/coredump ] && find /var/lib/systemd/coredump -type f -delete`,
	},
	"tmp": {
		title: "Oude tijdelijke bestanden",
		explain: "Bestanden in /tmp en /var/tmp die al 10 dagen niemand opende. Meestal vergeten, " +
			"maar een programma kan er bewust iets laten staan; daarom staat dit niet standaard aan.",
		safe: false, def: false,
		script: `find /tmp /var/tmp -xdev -type f -atime +10 -delete`,
	},
	"docker": {
		title: "Ongebruikte Docker-images en bouwcache",
		explain: "Images die geen enkele container gebruikt, ook niet een gestopte, en de cache van docker build. " +
			"Volumes (je data) blijven altijd staan. Wil je later een oude versie terug, dan moet Docker die opnieuw downloaden; " +
			"daarom staat dit niet standaard aan.",
		safe: false, def: false,
		script: `command -v docker >/dev/null && { docker image prune -af; docker builder prune -af; }`,
	},
}

// order is de volgorde in de wizard.
var order = []string{"journal", "apt", "oldlogs", "coredumps", "tmp", "docker"}

// mountRe: een mount uit de metrics die veilig in het script past.
var mountRe = regexp.MustCompile(`^/[A-Za-z0-9._/@+-]*$`)

// scanScript meet de opruimbare onderdelen en de grootste mappen op mount.
// Elke uitkomst is een regel "CFDISK ..." met tabs; de rest is gewone uitvoer.
func scanScript(mount string) string {
	if !mountRe.MatchString(mount) {
		mount = "/"
	}
	return `set -u
export LC_ALL=C
sz() { du -sxb "$@" 2>/dev/null | awk '{s+=$1} END {print s+0}'; }
fsum() { awk '{s+=$1} END {print s+0}'; }
echo "Meten wat er ruimte inneemt op $(hostname)…"
df -l -B1 --output=target,size,used,avail -x tmpfs -x devtmpfs -x squashfs -x overlay -x efivarfs 2>/dev/null | tail -n +2 | while read -r t s u a; do
  printf 'CFDISK\tmount\t%s\t%s\t%s\t%s\n' "$t" "$s" "$u" "$a"
done
printf 'CFDISK\titem\tjournal\t%s\n' "$(sz /var/log/journal /run/log/journal)"
printf 'CFDISK\titem\tapt\t%s\n' "$(find /var/cache/apt -xdev -type f -name '*.deb' -printf '%s\n' 2>/dev/null | fsum)"
printf 'CFDISK\titem\toldlogs\t%s\n' "$(find /var/log -xdev -type f \( -name '*.gz' -o -name '*.[0-9]' -o -name '*.[0-9].log' -o -name '*.old' \) -mtime +7 -printf '%s\n' 2>/dev/null | fsum)"
printf 'CFDISK\titem\tcoredumps\t%s\n' "$(sz /var/lib/systemd/coredump)"
printf 'CFDISK\titem\ttmp\t%s\n' "$(find /tmp /var/tmp -xdev -type f -atime +10 -printf '%s\n' 2>/dev/null | fsum)"
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  docker system df --format '{{.Type}}	{{.Reclaimable}}' 2>/dev/null | while IFS='	' read -r t r; do
    printf 'CFDISK\tdocker\t%s\t%s\n' "$t" "$r"
  done
fi
echo "De grootste mappen op ` + mount + ` (dit kan even duren)…"
timeout 300 du -xb -d 3 '` + mount + `' 2>/dev/null | sort -rn | head -n 25 | while read -r b p; do
  printf 'CFDISK\tdir\t%s\t%s\n' "$b" "$p"
done
echo "Klaar."
`
}

// cleanupScript ruimt de gekozen onderdelen op; elk onderdeel apart, zodat
// een fout in het ene het andere niet tegenhoudt.
func cleanupScript(items []string) string {
	var b strings.Builder
	b.WriteString("set -u\nexport LC_ALL=C\nrc=0\n")
	for _, id := range items {
		e := catalog[id]
		fmt.Fprintf(&b, "echo %q\n", "Opruimen: "+e.title)
		fmt.Fprintf(&b, "( %s ) || { echo %q; rc=1; }\n", e.script, "Dit onderdeel lukte niet helemaal; de rest gaat door.")
	}
	b.WriteString("exit 0\n")
	return b.String()
}

// parseScan leest de CFDISK-regels.
func parseScan(lines []string) (Scan, error) {
	var sc Scan
	bytes := map[string]int64{}
	var seen bool
	var dockerImages, dockerCache int64
	for _, l := range lines {
		f := strings.Split(l, "\t")
		if len(f) < 2 || f[0] != "CFDISK" {
			continue
		}
		seen = true
		switch f[1] {
		case "mount":
			if len(f) < 6 {
				continue
			}
			sz, _ := strconv.ParseInt(f[3], 10, 64)
			u, _ := strconv.ParseInt(f[4], 10, 64)
			a, _ := strconv.ParseInt(f[5], 10, 64)
			if sz < 100<<20 {
				continue
			}
			sc.Mounts = append(sc.Mounts, Mount{Mount: f[2], Size: sz, Used: u, Avail: a})
		case "item":
			if len(f) < 4 {
				continue
			}
			v, _ := strconv.ParseInt(f[3], 10, 64)
			bytes[f[2]] = v
		case "docker":
			if len(f) < 4 {
				continue
			}
			v := parseHuman(f[3])
			switch strings.ToLower(f[2]) {
			case "images":
				dockerImages = v
			case "build cache":
				dockerCache = v
			}
			bytes["docker"] = dockerImages + dockerCache
		case "dir":
			if len(f) < 4 {
				continue
			}
			v, _ := strconv.ParseInt(f[2], 10, 64)
			sc.Dirs = append(sc.Dirs, Dir{Path: f[3], Bytes: v})
		}
	}
	if !seen {
		return Scan{}, errors.New("de meting gaf geen resultaat")
	}
	for _, id := range order {
		v, ok := bytes[id]
		if !ok {
			continue
		}
		e := catalog[id]
		sc.Items = append(sc.Items, Item{ID: id, Title: e.title, Explain: e.explain, Bytes: v, Safe: e.safe, Default: e.def})
	}
	if sc.Mounts == nil {
		sc.Mounts = []Mount{}
	}
	if sc.Items == nil {
		sc.Items = []Item{}
	}
	if sc.Dirs == nil {
		sc.Dirs = []Dir{}
	}
	return sc, nil
}

var humanRe = regexp.MustCompile(`^\s*([0-9.]+)\s*([kKMGTP]?i?B)?`)

// parseHuman leest "1.2GB (40%)" zoals docker system df het toont.
func parseHuman(s string) int64 {
	m := humanRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	mult := map[string]float64{"": 1, "B": 1, "kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "PB": 1e15,
		"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40}[m[2]]
	if mult == 0 {
		mult = 1
	}
	return int64(v * mult)
}

// capture geeft de uitvoer door aan de stap en houdt de CFDISK-regels bij.
type capture struct {
	st    *jobs.Step
	lines []string
}

func (c *capture) Logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	if strings.HasPrefix(line, "CFDISK\t") {
		c.lines = append(c.lines, line)
		return
	}
	c.st.Logf("%s", line)
}

func (c *capture) Flush(ctx context.Context) error { return c.st.Flush(ctx) }

type params struct {
	NodeID   uuid.UUID `json:"node_id"`
	Hostname string    `json:"hostname"`
	Mount    string    `json:"mount"`
	Items    []string  `json:"items,omitempty"`
}

// Scan start de meting als taak.
func (s *Service) StartScan(ctx context.Context, actor events.Actor, id uuid.UUID) (uuid.UUID, error) {
	return s.start(ctx, actor, id, nil)
}

// Cleanup start het opruimen van items als taak.
func (s *Service) StartCleanup(ctx context.Context, actor events.Actor, id uuid.UUID, items []string) (uuid.UUID, error) {
	if len(items) == 0 {
		return uuid.Nil, &FieldError{"kies minstens één onderdeel om op te ruimen"}
	}
	var clean []string
	for _, it := range items {
		if _, ok := catalog[it]; !ok {
			return uuid.Nil, &FieldError{fmt.Sprintf("onbekend onderdeel %q", it)}
		}
		if !slices.Contains(clean, it) {
			clean = append(clean, it)
		}
	}
	slices.SortFunc(clean, func(a, b string) int { return slices.Index(order, a) - slices.Index(order, b) })
	return s.start(ctx, actor, id, clean)
}

func (s *Service) start(ctx context.Context, actor events.Actor, id uuid.UUID, items []string) (uuid.UUID, error) {
	n, err := s.Node(ctx, id)
	if err != nil {
		return uuid.Nil, err
	}
	switch {
	case !n.Online:
		return uuid.Nil, &FieldError{n.Hostname + " antwoordt niet. Kijk of de machine aan staat en de agent draait (Nodes → " + n.Hostname + ")."}
	case n.RunningJob != nil:
		return uuid.Nil, &FieldError{"op " + n.Hostname + " loopt al een meting of opruimbeurt; wacht tot die klaar is"}
	}
	kind, title := KindScan, "Schijfruimte meten op "+n.Hostname
	if items != nil {
		kind, title = KindCleanup, "Schijfruimte vrijmaken op "+n.Hostname
	}
	j, err := s.runner.Enqueue(ctx, jobs.Spec{Kind: kind, Title: title, NodeID: &n.NodeID, ClusterID: n.ClusterID, Actor: actor,
		Params: params{NodeID: n.NodeID, Hostname: n.Hostname, Mount: n.Mount, Items: items}})
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO disk_scans (node_id, running_job) VALUES ($1, $2)
		ON CONFLICT (node_id) DO UPDATE SET running_job = $2`, n.NodeID, j.ID); err != nil {
		return uuid.Nil, err
	}
	s.runner.Kick()
	return j.ID, nil
}

// measure draait de meting in een stap en geeft het resultaat.
func (s *Service) measure(ctx context.Context, j *jobs.Job, p params, step string) (Scan, error) {
	var sc Scan
	err := j.Step(ctx, step, func(ctx context.Context, st *jobs.Step) error {
		c := &capture{st: st}
		spec := protocol.RunSpec{ID: fmt.Sprintf("disk-%s-%s", strings.ReplaceAll(step, " ", "-"), j.ID), Kind: protocol.RunScript,
			Title: j.Title, TimeoutSeconds: 600, Script: scanScript(p.Mount)}
		res, err := s.Run.DoWith(ctx, c, st, p.NodeID, spec)
		if err != nil {
			return err
		}
		if res.State != protocol.RunOK {
			return errors.New(noderun.Describe(res))
		}
		sc, err = parseScan(c.lines)
		if err != nil {
			return fmt.Errorf("%w; probeer opnieuw", err)
		}
		var total int64
		for _, it := range sc.Items {
			total += it.Bytes
		}
		st.Logf("Opruimbaar: ongeveer %s.", Human(total))
		return nil
	})
	sc.ScannedAt, sc.JobID = s.Now().UTC(), j.ID
	return sc, err
}

func (s *Service) runScan(ctx context.Context, j *jobs.Job) error {
	var p params
	if err := j.Decode(&p); err != nil {
		return err
	}
	defer s.release(context.WithoutCancel(ctx), p.NodeID, j.ID)
	sc, err := s.measure(ctx, j, p, "meten")
	if err != nil {
		return err
	}
	// Wat de vorige opruimbeurt deed, blijft zichtbaar.
	if prev, err := s.Node(ctx, p.NodeID); err == nil && prev.Scan != nil {
		sc.CleanedAt, sc.Freed, sc.Cleaned = prev.Scan.CleanedAt, prev.Scan.Freed, prev.Scan.Cleaned
	}
	return s.save(ctx, p.NodeID, sc)
}

func (s *Service) runCleanup(ctx context.Context, j *jobs.Job) error {
	var p params
	if err := j.Decode(&p); err != nil {
		return err
	}
	defer s.release(context.WithoutCancel(ctx), p.NodeID, j.ID)
	before, err := s.measure(ctx, j, p, "voor")
	if err != nil {
		return err
	}
	err = j.Step(ctx, "opruimen", func(ctx context.Context, st *jobs.Step) error {
		spec := protocol.RunSpec{ID: "disk-clean-" + j.ID.String(), Kind: protocol.RunScript, Title: j.Title, TimeoutSeconds: 1800,
			Script: cleanupScript(p.Items)}
		res, err := s.Run.Do(ctx, st, p.NodeID, spec)
		if err != nil {
			return err
		}
		if res.State != protocol.RunOK {
			return errors.New(noderun.Describe(res))
		}
		return nil
	})
	if err != nil {
		return err
	}
	after, err := s.measure(ctx, j, p, "na")
	if err != nil {
		return err
	}
	freed := used(before, p.Mount) - used(after, p.Mount)
	if freed < 0 {
		freed = 0
	}
	now := s.Now().UTC()
	after.CleanedAt, after.Freed, after.Cleaned = &now, &freed, p.Items
	if err := s.save(ctx, p.NodeID, after); err != nil {
		return err
	}
	_ = j.Step(ctx, "resultaat", func(ctx context.Context, st *jobs.Step) error {
		st.Logf("Vrijgemaakt: %s.", Human(freed))
		return nil
	})
	if s.ev != nil {
		_ = s.ev.Write(context.WithoutCancel(ctx), nil, events.Event{Actor: actorOf(j), SubjectType: "node", SubjectID: p.NodeID.String(),
			Action: "disk.cleaned", Payload: map[string]any{"hostname": p.Hostname, "items": p.Items, "freed": freed, "job_id": j.ID.String()}})
	}
	return nil
}

func actorOf(j *jobs.Job) events.Actor {
	if j.RequestedBy != nil {
		return events.User(*j.RequestedBy)
	}
	return events.System()
}

// used is wat er op mount in gebruik is; zonder die mount de som.
func used(sc Scan, mount string) int64 {
	var total int64
	for _, m := range sc.Mounts {
		if m.Mount == mount {
			return m.Used
		}
		total += m.Used
	}
	return total
}

func (s *Service) save(ctx context.Context, id uuid.UUID, sc Scan) error {
	b, err := json.Marshal(sc)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO disk_scans (node_id, job_id, scanned_at, result) VALUES ($1, $2, $3, $4)
		ON CONFLICT (node_id) DO UPDATE SET job_id = $2, scanned_at = $3, result = $4`, id, sc.JobID, sc.ScannedAt, b)
	return err
}

func (s *Service) release(ctx context.Context, id, job uuid.UUID) {
	_, _ = s.pool.Exec(ctx, `UPDATE disk_scans SET running_job = NULL WHERE node_id = $1 AND running_job = $2`, id, job)
}

// Human schrijft bytes leesbaar, zoals 1,4 GB.
func Human(b int64) string {
	const unit = 1000
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return strings.Replace(fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "kMGTPE"[exp]), ".", ",", 1)
}
