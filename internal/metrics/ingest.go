// Package metrics schrijft de metrics van de agents naar VictoriaMetrics en
// leest ze terug voor de grafieken in de webinterface.
package metrics

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

var (
	metricName = regexp.MustCompile(`^node_[a-zA-Z0-9_]{1,100}$`)
	labelName  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,63}$`)
)

// serverLabels zet de server zelf; een agent kan ze niet overschrijven.
var serverLabels = []string{"job", "instance", "node", "node_id", "cluster", "cluster_id", "env"}

const (
	flushInterval = 5 * time.Second
	flushBytes    = 1 << 20
	// maxPending is hoeveel er mag wachten als VictoriaMetrics onbereikbaar
	// is; daarboven vervalt wat wacht.
	maxPending = 16 << 20
	labelTTL   = time.Minute
)

// Ingester neemt batches van agents aan en schrijft ze naar VictoriaMetrics.
type Ingester struct {
	url    string
	client *http.Client
	q      *store.Queries
	log    *slog.Logger

	mu      sync.Mutex
	pending []byte
	labels  map[uuid.UUID]cachedLabels
	flush   chan struct{}
}

type cachedLabels struct {
	prefix  string
	fetched time.Time
}

// NewIngester schrijft naar de VictoriaMetrics op vmURL; leeg schrijft
// nergens heen, maar de statusgegevens (schijfbezetting) worden wel bijgewerkt.
func NewIngester(vmURL string, q *store.Queries, log *slog.Logger) *Ingester {
	return &Ingester{
		url: strings.TrimRight(vmURL, "/"), client: &http.Client{Timeout: 30 * time.Second},
		q: q, log: log, labels: map[uuid.UUID]cachedLabels{}, flush: make(chan struct{}, 1),
	}
}

func (i *Ingester) Enabled() bool { return i.url != "" }

// Ingest verwerkt een batch van een node, met now als tijdstip.
func (i *Ingester) Ingest(ctx context.Context, nodeID uuid.UUID, m protocol.Metrics, now time.Time) error {
	if len(m.Samples) > protocol.MaxSamples {
		return fmt.Errorf("te veel metingen: %d", len(m.Samples))
	}
	ratio, mount := diskUsage(m.Samples)
	err := i.q.SetNodeDiskUsage(ctx, store.SetNodeDiskUsageParams{NodeID: nodeID, DiskUsedRatio: ratio, DiskUsedMount: mount})
	if err != nil {
		return err
	}
	if !i.Enabled() {
		return nil
	}
	prefix, err := i.nodeLabels(ctx, nodeID)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	writeLines(&buf, prefix, m.Samples, now)
	i.mu.Lock()
	if len(i.pending)+buf.Len() > maxPending {
		// VictoriaMetrics is al een tijd onbereikbaar; de oude data vervalt.
		i.pending = nil
	}
	i.pending = append(i.pending, buf.Bytes()...)
	full := len(i.pending) >= flushBytes
	i.mu.Unlock()
	if full {
		select {
		case i.flush <- struct{}{}:
		default:
		}
	}
	return nil
}

// nodeLabels geeft de opgemaakte serverlabels van een node, uit een cache.
func (i *Ingester) nodeLabels(ctx context.Context, nodeID uuid.UUID) (string, error) {
	i.mu.Lock()
	l, ok := i.labels[nodeID]
	i.mu.Unlock()
	if ok && time.Since(l.fetched) < labelTTL {
		return l.prefix, nil
	}
	row, err := i.q.GetMetricLabels(ctx, nodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("onbekende node %s", nodeID)
	} else if err != nil {
		return "", err
	}
	clusterID := ""
	if row.ClusterID != nil {
		clusterID = row.ClusterID.String()
	}
	l = cachedLabels{prefix: labelPrefix(row.Hostname, nodeID.String(), row.Cluster, clusterID, row.Environment), fetched: time.Now()}
	i.mu.Lock()
	i.labels[nodeID] = l
	i.mu.Unlock()
	return l.prefix, nil
}

// labelPrefix maakt de labels die de server aan elke meting van een node
// hangt. Een node zonder cluster krijgt lege clusterlabels; VictoriaMetrics
// laat die weg.
func labelPrefix(hostname, nodeID, cluster, clusterID, env string) string {
	var b bytes.Buffer
	for i, kv := range [][2]string{
		{"job", "clusterforge"}, {"instance", hostname}, {"node", hostname}, {"node_id", nodeID},
		{"cluster", cluster}, {"cluster_id", clusterID}, {"env", env},
	} {
		if i > 0 {
			b.WriteByte(',')
		}
		writeLabel(&b, kv[0], kv[1])
	}
	return b.String()
}

// writeLabel schrijft naam="waarde" in Prometheus-tekstformaat.
func writeLabel(b *bytes.Buffer, name, value string) {
	if len(value) > 256 {
		value = value[:256]
	}
	b.WriteString(name)
	b.WriteString(`="`)
	for _, r := range value {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// writeLines schrijft de metingen in Prometheus-tekstformaat, met prefix als
// eerste labels. Ongeldige namen en waarden vallen weg, net als labels die de
// server zelf zet.
func writeLines(buf *bytes.Buffer, prefix string, samples []protocol.Sample, now time.Time) {
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	for _, s := range samples {
		if !metricName.MatchString(s.Name) || math.IsNaN(s.Value) || math.IsInf(s.Value, 0) {
			continue
		}
		buf.WriteString(s.Name)
		buf.WriteByte('{')
		buf.WriteString(prefix)
		keys := make([]string, 0, len(s.Labels))
		for k := range s.Labels {
			if labelName.MatchString(k) && !slices.Contains(serverLabels, k) && !strings.HasPrefix(k, "__") {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		for _, k := range keys {
			buf.WriteByte(',')
			writeLabel(buf, k, s.Labels[k])
		}
		buf.WriteString("} ")
		buf.WriteString(strconv.FormatFloat(s.Value, 'g', -1, 64))
		buf.WriteByte(' ')
		buf.WriteString(ts)
		buf.WriteByte('\n')
	}
}

// diskUsage geeft het volste bestandssysteem, voor de statusregels. De
// bezetting rekent zoals df: gebruikt / (gebruikt + beschikbaar).
func diskUsage(samples []protocol.Sample) (ratio float64, mount string) {
	type fs struct{ size, free, avail float64 }
	byMount := map[string]*fs{}
	get := func(mount string) *fs {
		if byMount[mount] == nil {
			byMount[mount] = &fs{}
		}
		return byMount[mount]
	}
	for _, s := range samples {
		mount := s.Labels["mountpoint"]
		if mount == "" {
			continue
		}
		switch s.Name {
		case "node_filesystem_size_bytes":
			get(mount).size = s.Value
		case "node_filesystem_free_bytes":
			get(mount).free = s.Value
		case "node_filesystem_avail_bytes":
			get(mount).avail = s.Value
		}
	}
	for m, f := range byMount {
		used := f.size - f.free
		if f.size <= 0 || used < 0 || used+f.avail <= 0 {
			continue
		}
		r := used / (used + f.avail)
		if r > ratio || (r == ratio && m < mount) {
			ratio, mount = r, m
		}
	}
	return ratio, mount
}

// Run stuurt de verzamelde metingen regelmatig door tot ctx afloopt.
func (i *Ingester) Run(ctx context.Context) {
	if !i.Enabled() {
		return
	}
	t := time.NewTicker(flushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// Wat nog wacht, nog één keer proberen.
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			i.Flush(fctx)
			cancel()
			return
		case <-t.C:
		case <-i.flush:
		}
		i.Flush(ctx)
	}
}

// Flush stuurt alles wat wacht naar VictoriaMetrics. Lukt dat niet, dan blijft
// het wachten voor de volgende poging.
func (i *Ingester) Flush(ctx context.Context) {
	i.mu.Lock()
	data := i.pending
	i.pending = nil
	i.mu.Unlock()
	if len(data) == 0 {
		return
	}
	if err := i.send(ctx, data); err != nil {
		i.log.Warn("metrics naar VictoriaMetrics schrijven mislukt", "err", err, "bytes", len(data))
		i.mu.Lock()
		if len(data)+len(i.pending) <= maxPending {
			i.pending = append(data, i.pending...)
		}
		i.mu.Unlock()
	}
}

func (i *Ingester) send(ctx context.Context, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.url+"/api/v1/import/prometheus", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain")
	resp, err := i.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
