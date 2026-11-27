package crowdsec

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// PublicSource is een publieke blocklist die ClusterForge kan ophalen.
// MinMinutes volgt de voorwaarden van de bron: nooit vaker ophalen.
type PublicSource struct {
	Key            string `json:"key"`
	Name           string `json:"name"`
	URL            string `json:"url"`
	Info           string `json:"info"`
	DefaultMinutes int    `json:"default_minutes"`
	MinMinutes     int    `json:"min_minutes"`
	// Format: lines (IP of CIDR per regel, # en ; zijn commentaar) of
	// ndjson (Spamhaus: één JSON-object met cidr per regel).
	Format string `json:"format"`
}

// PublicCatalog zijn de bekende gratis lijsten.
var PublicCatalog = []PublicSource{
	{Key: "firehol-level1", Name: "FireHOL level 1", URL: "https://raw.githubusercontent.com/firehol/blocklist-ipsets/master/firehol_level1.netset",
		Info:           "Samengesteld door FireHOL uit o.a. Spamhaus DROP, dshield en Feodo; weinig vals-positief. Bevat ook privé-netwerken: die laat ClusterForge altijd weg.",
		DefaultMinutes: 1440, MinMinutes: 60, Format: "lines"},
	{Key: "spamhaus-drop", Name: "Spamhaus DROP (IPv4)", URL: "https://www.spamhaus.org/drop/drop_v4.json",
		Info:           "Gekaapte en criminele netwerken. Gratis voor eigen gebruik; Spamhaus vraagt hoogstens één keer per uur op te halen.",
		DefaultMinutes: 1440, MinMinutes: 60, Format: "ndjson"},
	{Key: "spamhaus-drop6", Name: "Spamhaus DROP (IPv6)", URL: "https://www.spamhaus.org/drop/drop_v6.json",
		Info: "De IPv6-variant van Spamhaus DROP; zelfde voorwaarden.", DefaultMinutes: 1440, MinMinutes: 60, Format: "ndjson"},
	{Key: "feodo", Name: "abuse.ch Feodo Tracker", URL: "https://feodotracker.abuse.ch/downloads/ipblocklist_recommended.txt",
		Info:           "Command-and-control-servers van botnets (CC0). abuse.ch werkt elke 5 minuten bij; ClusterForge haalt hoogstens om het uur.",
		DefaultMinutes: 360, MinMinutes: 60, Format: "lines"},
	{Key: "blocklist-de", Name: "blocklist.de (alles)", URL: "https://lists.blocklist.de/lists/all.txt",
		Info:           "IP's die de afgelopen 48 uur aanvielen (SSH, mail, web, …) bij de deelnemers van blocklist.de. Groot (tienduizenden IP's).",
		DefaultMinutes: 720, MinMinutes: 60, Format: "lines"},
	{Key: "cins", Name: "CINS Army", URL: "https://cinsscore.com/list/ci-badguys.txt",
		Info:           "IP's met een slechte score bij Sentinel IPS van CINS; gratis lijst, één keer per dag ophalen volstaat.",
		DefaultMinutes: 1440, MinMinutes: 240, Format: "lines"},
	{Key: "et-compromised", Name: "Emerging Threats compromised", URL: "https://rules.emergingthreats.net/blockrules/compromised-ips.txt",
		Info: "Gecompromitteerde hosts van Proofpoint Emerging Threats (open lijst).", DefaultMinutes: 1440, MinMinutes: 240, Format: "lines"},
}

// PublicByKey zoekt een bron in de catalogus.
func PublicByKey(k string) (PublicSource, bool) {
	for _, p := range PublicCatalog {
		if p.Key == k {
			return p, true
		}
	}
	return PublicSource{}, false
}

// checkSource geeft de bron voor een sleutel of een eigen https-URL.
func (s *Service) checkSource(key, raw string) (PublicSource, error) {
	if key != "" && key != "custom" {
		p, ok := PublicByKey(key)
		if !ok {
			return PublicSource{}, &FieldError{fmt.Sprintf("onbekende lijst %q", key)}
		}
		return p, nil
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return PublicSource{}, &FieldError{"geef een https-URL van een tekstlijst (één IP of range per regel)"}
	}
	return PublicSource{Key: "custom", Name: u.Host, URL: u.String(), DefaultMinutes: 1440, MinMinutes: 60, Format: "lines",
		Info: "Eigen URL; respecteer de voorwaarden van de bron."}, nil
}

// maxFetchBytes is de grootste lijst die ClusterForge ophaalt.
const maxFetchBytes = 16 << 20

// ParseList leest een lijst: geldige waarden genormaliseerd, en het aantal
// regels dat geen IP of range was.
func ParseList(r io.Reader, format string) (vals []string, bad int, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	seen := map[string]bool{}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' || strings.HasPrefix(line, "//") {
			continue
		}
		if format == "ndjson" || line[0] == '{' {
			var o struct {
				CIDR string `json:"cidr"`
				IP   string `json:"ip"`
			}
			if json.Unmarshal([]byte(line), &o) != nil {
				bad++
				continue
			}
			if line = o.CIDR; line == "" {
				line = o.IP
			}
			if line == "" {
				continue // Spamhaus sluit af met een regel zonder cidr
			}
		}
		if i := strings.IndexAny(line, " \t;#,"); i >= 0 {
			line = line[:i]
		}
		_, norm, err := protocol.ListValue(line)
		if err != nil {
			bad++
			continue
		}
		if !seen[norm] {
			seen[norm] = true
			vals = append(vals, norm)
		}
		if len(vals) > MaxListEntries {
			return nil, bad, fmt.Errorf("de lijst heeft meer dan %d waarden", MaxListEntries)
		}
	}
	return vals, bad, sc.Err()
}

type fetchResult struct {
	vals         []string
	bad          int
	notModified  bool
	etag, lastMd string
}

func (s *Service) httpClient() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 90 * time.Second}
}

// fetch haalt een lijst op, voorwaardelijk met ETag en Last-Modified.
func (s *Service) fetch(ctx context.Context, src PublicSource, etag, mod string) (fetchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return fetchResult{}, err
	}
	req.Header.Set("User-Agent", "ClusterForge-blocklist/1.0")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if mod != "" {
		req.Header.Set("If-Modified-Since", mod)
	}
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return fetchResult{}, fmt.Errorf("ophalen: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotModified {
		return fetchResult{notModified: true, etag: etag, lastMd: mod}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return fetchResult{}, fmt.Errorf("ophalen: HTTP %d", resp.StatusCode)
	}
	vals, bad, err := ParseList(io.LimitReader(resp.Body, maxFetchBytes), src.Format)
	if err != nil {
		return fetchResult{}, err
	}
	if len(vals) == 0 {
		return fetchResult{}, errors.New("de lijst is leeg of heeft geen IP-adressen")
	}
	return fetchResult{vals: vals, bad: bad, etag: resp.Header.Get("ETag"), lastMd: resp.Header.Get("Last-Modified")}, nil
}

// PreviewSource haalt een lijst op zonder iets te bewaren: hoeveel
// waarden, en welke ClusterForge weglaat.
func (s *Service) PreviewSource(ctx context.Context, key, raw string) (Diff, error) {
	src, err := s.checkSource(key, raw)
	if err != nil {
		return Diff{}, err
	}
	res, err := s.fetch(ctx, src, "", "")
	if err != nil {
		return Diff{}, &FieldError{err.Error()}
	}
	g, err := s.guard(ctx)
	if err != nil {
		return Diff{}, err
	}
	var d Diff
	for _, v := range res.vals {
		if _, why := g.Check(v); why != "" {
			d.Refused = append(d.Refused, Dropped{Value: v, Why: why})
		} else {
			d.Added = append(d.Added, v)
		}
	}
	d.Count = len(d.Added)
	if d.Nodes, err = s.nodesFor(ctx, List{Scope: "all"}); err != nil {
		return Diff{}, err
	}
	d.finish()
	return d, nil
}

// Refresh haalt een publieke lijst nu op (ook een vastgezette, die dan
// weer vanzelf ververst).
func (s *Service) RefreshList(ctx context.Context, actor events.Actor, id uuid.UUID) (Diff, error) {
	l, err := s.list(ctx, nil, id)
	if err != nil {
		return Diff{}, err
	}
	if l.Kind != ListPublic {
		return Diff{}, &FieldError{"alleen een publieke lijst wordt opgehaald"}
	}
	if l.FetchedAt != nil && s.Now().Sub(*l.FetchedAt) < 5*time.Minute && !l.Pinned {
		return Diff{}, &FieldError{"net opgehaald; wacht enkele minuten (voorwaarden van de bron)"}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE crowdsec_lists SET pinned = false WHERE id = $1`, id); err != nil {
		return Diff{}, err
	}
	l.Pinned = false
	return s.fetchList(ctx, actor, l, true)
}

// fetchList haalt een publieke lijst op en zet de nieuwe inhoud.
func (s *Service) fetchList(ctx context.Context, actor events.Actor, l List, force bool) (Diff, error) {
	src, ok := PublicByKey(l.Source)
	if !ok {
		var err error
		if src, err = s.checkSource("custom", l.URL); err != nil {
			return Diff{}, err
		}
	}
	etag, mod := l.fetchETag, l.fetchMod
	if force {
		etag, mod = "", ""
	}
	res, err := s.fetch(ctx, src, etag, mod)
	if err != nil {
		_, _ = s.pool.Exec(context.WithoutCancel(ctx), `UPDATE crowdsec_lists SET fetch_error = $2, fetch_failed_at = now() WHERE id = $1`, l.ID, err.Error())
		s.notifyKey(ctx, "list-fetch:"+l.Slug, 6*time.Hour, "Blocklist ophalen mislukt", l.Name+": "+err.Error(), "warning")
		return Diff{}, &FieldError{err.Error()}
	}
	if res.notModified {
		_, err := s.pool.Exec(ctx, `UPDATE crowdsec_lists SET fetched_at = now(), fetch_error = '', fetch_failed_at = NULL WHERE id = $1`, l.ID)
		return Diff{Count: l.Count}, err
	}
	g, err := s.guard(ctx)
	if err != nil {
		return Diff{}, err
	}
	var d Diff
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := s.entriesTx(ctx, tx, l.ID)
		if err != nil {
			return err
		}
		want := make(map[string]storedEntry, len(res.vals))
		for _, v := range res.vals {
			want[v] = storedEntry{Value: v}
			if _, ok := cur[v]; !ok {
				d.Added = append(d.Added, v)
			}
		}
		for v := range cur {
			if _, ok := want[v]; !ok {
				d.Removed = append(d.Removed, v)
			}
		}
		d.Count = len(want)
		if _, err := tx.Exec(ctx, `UPDATE crowdsec_lists SET fetched_at = now(), fetch_etag = $2, fetch_modified = $3, fetch_error = '',
			fetch_failed_at = NULL WHERE id = $1`, l.ID, res.etag, res.lastMd); err != nil {
			return err
		}
		if len(d.Added)+len(d.Removed) == 0 {
			return s.recompute(ctx, tx, l, g)
		}
		note := fmt.Sprintf("opgehaald: %d erbij, %d weg", len(d.Added), len(d.Removed))
		if d.VersionID, err = s.replaceEntries(ctx, tx, l, want, len(d.Added), len(d.Removed), note, nil, g); err != nil {
			return err
		}
		d.Applied = true
		return nil
	})
	if err != nil {
		return Diff{}, err
	}
	if d.Applied {
		s.listEvent(ctx, actor, l, "crowdsec.list_fetched", map[string]any{
			"added_count": len(d.Added), "removed_count": len(d.Removed), "count": d.Count, "invalid_lines": res.bad,
		})
	}
	d.finish()
	return d, nil
}

// fetchDue haalt de publieke lijsten op waarvan de tijd om is.
func (s *Service) fetchDue(ctx context.Context) {
	ls, err := s.allLists(ctx)
	if err != nil {
		s.log.Warn("lijsten lezen mislukt", "err", err)
		return
	}
	now := s.Now()
	for _, l := range ls {
		if l.Kind != ListPublic || !l.Enabled || l.Pinned {
			continue
		}
		every := time.Duration(l.RefreshMinutes) * time.Minute
		if l.FetchedAt != nil && now.Sub(*l.FetchedAt) < every {
			continue
		}
		// Na een fout niet meteen opnieuw: wacht minstens een kwartier
		// (en nooit vaker dan de bron toelaat).
		if l.FetchFailedAt != nil && now.Sub(*l.FetchFailedAt) < max(15*time.Minute, time.Duration(minMinutes(l))*time.Minute) {
			continue
		}
		if _, err := s.fetchList(ctx, events.System(), l, false); err != nil {
			s.log.Warn("blocklist ophalen mislukt", "list", l.Slug, "err", err)
		}
	}
}

func minMinutes(l List) int {
	if p, ok := PublicByKey(l.Source); ok {
		return p.MinMinutes
	}
	return 60
}
