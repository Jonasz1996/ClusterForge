package crowdsec

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
)

// Meldingen (fase 47): ClusterForge stuurt zelf berichten naar ntfy,
// Discord, Slack, Telegram of een eigen webhook, in plaats van de CrowdSec
// Console. De adressen en tokens staan versleutelde met de masterkey.

// Channel is een kanaal zoals de browser het ziet: zonder geheimen.
type Channel struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Target is het adres zonder geheim deel, bv. ntfy.sh/…
	Target string `json:"target"`
}

// NotifySettings zegt waarvoor een melding komt.
type NotifySettings struct {
	Channels []Channel `json:"channels"`
	// AutoBlock: elk IP dat de automatische regels blokkeren.
	AutoBlock bool `json:"auto_block"`
	// NewAlerts: een samenvatting van nieuwe alerts (hoogstens om de 5
	// minuten).
	NewAlerts bool `json:"new_alerts"`
	// AlertBurst: zoveel alerts in 10 minuten (0 is uit), hoogstens één
	// melding per uur.
	AlertBurst int `json:"alert_burst"`
	// NodeProblems: CrowdSec of de bouncer staat stil op een node.
	NodeProblems bool `json:"node_problems"`
	// ListProblems: een lijst ophalen of zetten mislukt.
	ListProblems bool `json:"list_problems"`
	// ManualBans: elke ban of deblokkering met de hand.
	ManualBans bool `json:"manual_bans"`
}

// DefaultNotify: meldingen voor wat aandacht vraagt.
var DefaultNotify = NotifySettings{Channels: []Channel{}, AutoBlock: true, AlertBurst: 25, NodeProblems: true, ListProblems: true}

type channelSecret struct {
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
}

// ChannelInput is een kanaal uit het formulier. Een leeg URL of Token bij
// een bestaand ID houdt het oude.
type ChannelInput struct {
	ID    string
	Kind  string
	Name  string
	URL   string
	Token string
}

var notifyAAD = []byte("crowdsec:notify")

func (s *Service) channelSecrets(set Settings) (map[string]channelSecret, error) {
	out := map[string]channelSecret{}
	if len(set.notifyEnc) == 0 {
		return out, nil
	}
	if s.Box == nil {
		return nil, errors.New("de kanalen zijn niet te lezen zonder masterkey")
	}
	b, err := s.Box.Open(set.notifyEnc, notifyAAD, set.notifyKeyID)
	if err != nil {
		return nil, errors.New("de kanalen zijn niet te ontsleutelen (is CF_MASTER_KEY veranderd?); vul ze opnieuw in")
	}
	return out, json.Unmarshal(b, &out)
}

func target(kind, raw string) string {
	if kind == "telegram" {
		return "chat " + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "?"
	}
	p := u.Path
	switch kind {
	case "discord", "slack":
		p = ""
	case "ntfy":
		if len(p) > 4 {
			p = p[:4] + "…"
		}
	}
	return u.Host + p
}

// UpdateNotify bewaart de meldingen en kanalen.
func (s *Service) UpdateNotify(ctx context.Context, actor events.Actor, userID *uuid.UUID, in NotifySettings, chans []ChannelInput) (Settings, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return Settings{}, err
	}
	if in.AlertBurst < 0 || in.AlertBurst > 10000 {
		return Settings{}, &FieldError{"drempel voor alerts is 0 (uit) tot 10000"}
	}
	if len(chans) > 10 {
		return Settings{}, &FieldError{"hoogstens 10 kanalen"}
	}
	old, err := s.channelSecrets(set)
	if err != nil && len(chans) > 0 {
		old = map[string]channelSecret{}
	}
	secrets := map[string]channelSecret{}
	in.Channels = []Channel{}
	for _, c := range chans {
		c.Kind, c.Name, c.URL, c.Token = strings.TrimSpace(c.Kind), strings.TrimSpace(c.Name), strings.TrimSpace(c.URL), strings.TrimSpace(c.Token)
		if c.ID == "" {
			var b [6]byte
			_, _ = rand.Read(b[:])
			c.ID = hex.EncodeToString(b[:])
		}
		sec := old[c.ID]
		if c.URL != "" {
			sec.URL = c.URL
		}
		if c.Token != "" {
			sec.Token = c.Token
		}
		if err := checkChannel(c.Kind, sec); err != nil {
			return Settings{}, err
		}
		if c.Name == "" {
			c.Name = c.Kind
		}
		secrets[c.ID] = sec
		in.Channels = append(in.Channels, Channel{ID: c.ID, Kind: c.Kind, Name: c.Name, Target: target(c.Kind, sec.URL)})
	}
	var enc []byte
	keyID := ""
	if len(secrets) > 0 {
		if s.Box == nil {
			return Settings{}, &FieldError{"de server heeft geen masterkey (CF_MASTER_KEY), dus hij kan de kanalen niet veilig bewaren"}
		}
		b, err := json.Marshal(secrets)
		if err != nil {
			return Settings{}, err
		}
		enc, keyID = s.Box.Seal(b, notifyAAD), s.Box.KeyID
	}
	pub, err := json.Marshal(in)
	if err != nil {
		return Settings{}, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE crowdsec_settings SET notify = $1, notify_enc = $2, notify_key_id = $3, updated_at = now(),
		updated_by = $4`, pub, enc, keyID, userID); err != nil {
		return Settings{}, err
	}
	s.event(ctx, actor, nil, "crowdsec.notify_changed", map[string]any{"channels": len(in.Channels), "auto_block": in.AutoBlock,
		"new_alerts": in.NewAlerts, "alert_burst": in.AlertBurst, "node_problems": in.NodeProblems, "list_problems": in.ListProblems})
	return s.Settings(ctx)
}

func checkChannel(kind string, sec channelSecret) error {
	switch kind {
	case "ntfy", "discord", "slack", "webhook":
		u, err := url.Parse(sec.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return &FieldError{"geef een http(s)-adres voor het kanaal " + kind}
		}
		if kind == "ntfy" && strings.Trim(u.Path, "/") == "" {
			return &FieldError{"geef het adres met het topic, zoals https://ntfy.sh/mijn-topic"}
		}
	case "telegram":
		if sec.URL == "" || sec.Token == "" {
			return &FieldError{"Telegram heeft een chat-id en een bot-token nodig"}
		}
	default:
		return &FieldError{fmt.Sprintf("onbekend kanaal %q (ntfy, discord, slack, telegram of webhook)", kind)}
	}
	return nil
}

// TestNotify stuurt een testmelding naar alle kanalen en geeft per kanaal
// de fout.
func (s *Service) TestNotify(ctx context.Context) (map[string]string, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	if len(set.Notify.Channels) == 0 {
		return nil, &FieldError{"voeg eerst een kanaal toe"}
	}
	sec, err := s.channelSecrets(set)
	if err != nil {
		return nil, &FieldError{err.Error()}
	}
	out := map[string]string{}
	for _, c := range set.Notify.Channels {
		out[c.Name] = ""
		if err := s.send(ctx, c, sec[c.ID], "Testmelding van ClusterForge", "Meldingen van CrowdSec komen voortaan hier binnen.", "info"); err != nil {
			out[c.Name] = err.Error()
		}
	}
	return out, nil
}

// notify stuurt een melding naar alle kanalen; fouten komen in het log.
func (s *Service) notify(ctx context.Context, title, body, level string) {
	set, err := s.Settings(ctx)
	if err != nil || len(set.Notify.Channels) == 0 {
		return
	}
	s.notifyMu.Lock()
	// Hoogstens 30 meldingen per uur, wat er ook gebeurt.
	n := 0
	now := s.Now()
	for k, t := range s.notified {
		if strings.HasPrefix(k, "sent:") && now.Sub(t) < time.Hour {
			n++
		}
	}
	if n >= 30 {
		s.notifyMu.Unlock()
		s.log.Warn("melding niet gestuurd: meer dan 30 in een uur", "title", title)
		return
	}
	s.notified[fmt.Sprintf("sent:%d", now.UnixNano())] = now
	s.notifyMu.Unlock()
	sec, err := s.channelSecrets(set)
	if err != nil {
		s.log.Warn("melding niet gestuurd", "err", err)
		return
	}
	for _, c := range set.Notify.Channels {
		if err := s.send(ctx, c, sec[c.ID], title, body, level); err != nil {
			s.log.Warn("melding sturen mislukt", "channel", c.Name, "err", err)
		}
	}
}

// notifyKey stuurt een melding hoogstens één keer per every voor key; de
// soort (lijst of node) moet aan staan.
func (s *Service) notifyKey(ctx context.Context, key string, every time.Duration, title, body, level string) {
	set, err := s.Settings(ctx)
	if err != nil {
		return
	}
	switch {
	case strings.HasPrefix(key, "list-") && !set.Notify.ListProblems:
		return
	case strings.HasPrefix(key, "node:") && !set.Notify.NodeProblems:
		return
	}
	now := s.Now()
	s.notifyMu.Lock()
	if t, ok := s.notified[key]; ok && now.Sub(t) < every {
		s.notifyMu.Unlock()
		return
	}
	s.notified[key] = now
	for k, t := range s.notified {
		if now.Sub(t) > 24*time.Hour {
			delete(s.notified, k)
		}
	}
	s.notifyMu.Unlock()
	s.notify(ctx, title, body, level)
}

func (s *Service) send(ctx context.Context, c Channel, sec channelSecret, title, body, level string) error {
	if len(body) > 1800 {
		body = body[:1800] + "…"
	}
	var u string
	var payload any
	headers := map[string]string{}
	switch c.Kind {
	case "ntfy":
		pu, err := url.Parse(sec.URL)
		if err != nil {
			return err
		}
		topic := strings.Trim(pu.Path, "/")
		pu.Path = "/"
		u = pu.String()
		prio := 3
		if level == "warning" {
			prio = 4
		}
		payload = map[string]any{"topic": topic, "title": title, "message": body, "priority": prio, "tags": []string{"shield"}}
		if sec.Token != "" {
			headers["Authorization"] = "Bearer " + sec.Token
		}
	case "discord":
		u, payload = sec.URL, map[string]any{"content": "**" + title + "**\n" + body}
	case "slack":
		u, payload = sec.URL, map[string]any{"text": "*" + title + "*\n" + body}
	case "telegram":
		u = "https://api.telegram.org/bot" + sec.Token + "/sendMessage"
		payload = map[string]any{"chat_id": sec.URL, "text": title + "\n" + body}
	default:
		u, payload = sec.URL, map[string]any{"source": "clusterforge", "event": "crowdsec", "title": title, "message": body,
			"level": level, "at": s.Now().UTC()}
		if sec.Token != "" {
			headers["Authorization"] = "Bearer " + sec.Token
		}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return errors.New("ongeldig adres")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ClusterForge")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.httpClient().Do(req)
	if err != nil {
		msg := err.Error()
		if sec.Token != "" {
			msg = strings.ReplaceAll(msg, sec.Token, "…")
		}
		return errors.New(msg)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// notifyRound kijkt na elke lijstronde of er iets te melden is: veel
// alerts, nieuwe alerts, of CrowdSec of de bouncer die stilstaat.
func (s *Service) notifyRound(ctx context.Context) {
	set, err := s.Settings(ctx)
	if err != nil || len(set.Notify.Channels) == 0 {
		return
	}
	nf := set.Notify
	now := s.Now()
	if nf.AlertBurst > 0 {
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM crowdsec_alerts WHERE received_at > $1`, now.Add(-10*time.Minute)).Scan(&n); err == nil &&
			n >= nf.AlertBurst {
			s.notifyKey(ctx, "burst", time.Hour, "Veel CrowdSec-alerts", fmt.Sprintf("%d alerts in de laatste 10 minuten. Zie CrowdSec → Alerts in ClusterForge.", n), "warning")
		}
	}
	s.newAlerts(ctx, nf.NewAlerts)
	if nf.NodeProblems {
		nodes, err := s.Nodes(ctx)
		if err != nil {
			return
		}
		for _, n := range nodes {
			if !n.Enabled || !n.Managed || !n.Online || n.Status == nil || !n.Status.Installed || n.ApplyRunning {
				continue
			}
			switch {
			case n.Status.Active != "active":
				s.notifyKey(ctx, "node:"+n.NodeID.String(), 6*time.Hour, "CrowdSec staat stil", n.Hostname+": crowdsec is "+n.Status.Active, "warning")
			case n.Bouncer && n.Status.Bouncer != "" && n.Status.Bouncer != "active":
				s.notifyKey(ctx, "node:"+n.NodeID.String(), 6*time.Hour, "Bouncer staat stil",
					n.Hostname+": de firewall-bouncer is "+n.Status.Bouncer+"; bans worden daar niet uitgevoerd", "warning")
			}
		}
	}
}

// newAlerts meldt alerts sinds de vorige melding, per scenario geteld.
func (s *Service) newAlerts(ctx context.Context, on bool) {
	var maxID int64
	if err := s.pool.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM crowdsec_alerts`).Scan(&maxID); err != nil {
		return
	}
	s.notifyMu.Lock()
	cursor := s.alertCursor
	last := s.notified["alerts"]
	if cursor < 0 || !on {
		s.alertCursor = maxID
		s.notifyMu.Unlock()
		return
	}
	if maxID <= cursor || s.Now().Sub(last) < 5*time.Minute {
		s.notifyMu.Unlock()
		return
	}
	s.alertCursor = maxID
	s.notified["alerts"] = s.Now()
	s.notifyMu.Unlock()
	rows, err := s.pool.Query(ctx, `SELECT a.scenario, count(*), count(DISTINCT a.value), array_agg(DISTINCT n.hostname)
		FROM crowdsec_alerts a JOIN nodes n ON n.id = a.node_id WHERE a.id > $1 AND a.id <= $2 GROUP BY a.scenario ORDER BY 2 DESC`, cursor, maxID)
	if err != nil {
		return
	}
	defer rows.Close()
	var lines []string
	total := 0
	for rows.Next() {
		var sc string
		var n, ips int
		var hosts []string
		if rows.Scan(&sc, &n, &ips, &hosts) != nil {
			return
		}
		sort.Strings(hosts)
		total += n
		if len(lines) < 10 {
			lines = append(lines, fmt.Sprintf("%s: %d× (%d IP) op %s", sc, n, ips, strings.Join(hosts, ", ")))
		}
	}
	if total > 0 {
		s.notify(ctx, fmt.Sprintf("%d nieuwe CrowdSec-alerts", total), strings.Join(lines, "\n"), "info")
	}
}
