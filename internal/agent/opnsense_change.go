package agent

// Fase 34: lezen en veilig veranderen op OPNsense.
//
// Een wijziging (run-actie opn_api) gaat zo: de agent bewaart config.xml als
// herstelpunt, zet een terugdraaitimer buiten de agent (daemon op FreeBSD),
// voert de API-aanroepen uit en past ze toe. De server bevestigt daarna met
// opn_confirm als zijn proeven slagen. Komt die bevestiging niet op tijd,
// dan zet de timer (cf-agent opn-rollback) het herstelpunt terug en past de
// oude toestand toe, ook als het netwerk naar de server net stuk is.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// opnRollbackUnit is de naam van de terugdraaitimer van een wijziging.
func opnRollbackUnit(id string) string { return "cf-opn-rollback-" + id }

// opnChangeState staat naast het herstelpunt zolang een wijziging loopt.
type opnChangeState struct {
	ID          string             `json:"id"`
	Status      string             `json:"status"` // pending, confirmed, rolled_back
	Snapshot    string             `json:"snapshot"`
	Apply       []protocol.OPNCall `json:"apply,omitempty"`
	Description string             `json:"description,omitempty"`
	Started     time.Time          `json:"started"`
	Deadline    time.Time          `json:"deadline"`
	Reason      string             `json:"reason,omitempty"`
	Calls       []string           `json:"calls,omitempty"`
	Secret      []string           `json:"secret,omitempty"`
}

func opnChangePath(root, id string) string {
	return filepath.Join(root, opnStateDir, "change-"+id+".json")
}

func loadOPNChange(path string) (*opnChangeState, error) {
	var st opnChangeState
	ok, err := loadJSON(path, &st)
	if !ok || err != nil {
		return nil, err
	}
	return &st, nil
}

// opnPending geeft de wijziging die nog op bevestiging wacht, of nil.
func opnPending(root string) *opnChangeState {
	files, _ := filepath.Glob(filepath.Join(root, opnStateDir, "change-*.json"))
	for _, f := range files {
		if st, err := loadOPNChange(f); err == nil && st != nil && st.Status == "pending" {
			return st
		}
	}
	return nil
}

// opnCollector is een collector met de wortel en commando's van de run.
func (r *RunExecutor) opnCollector() *Collector {
	c := &Collector{Root: r.Root}
	if r.Cmd != nil {
		c.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			var buf bytes.Buffer
			code, err := r.Cmd(ctx, "", append([]string{name}, args...), nil, &buf)
			if err == nil && code != 0 {
				err = &errStep{name, code}
			}
			return buf.Bytes(), err
		}
	}
	if r.OPN != nil {
		c.opnOnce.Do(func() { c.opn = r.OPN })
	} else {
		r.OPN = c.opnAPI()
	}
	return c
}

// opn voert een OPNsense-actie van een run pve uit.
func (r *RunExecutor) opn(ctx context.Context, p *protocol.PVERun, out io.Writer, st *protocol.RunStatus) (int, error) {
	c := r.opnCollector()
	if !c.isOPNsense() {
		return -1, errors.New("dit is geen OPNsense-firewall")
	}
	switch p.Action {
	case protocol.OPNAPI:
		return r.opnApply(ctx, c, p.OPN, out, st)
	case protocol.OPNConfirm:
		return r.opnConfirm(ctx, c, p.Change, out)
	case protocol.OPNRollback:
		st, err := loadOPNChange(opnChangePath(r.Root, p.Change))
		if err != nil || st == nil {
			return -1, fmt.Errorf("wijziging %s is hier niet bekend", p.Change)
		}
		r.disarm(ctx, opnRollbackUnit(p.Change))
		if err := opnRollback(ctx, c, st, "teruggedraaid op vraag van ClusterForge", out); err != nil {
			return 1, err
		}
		return 0, nil
	case protocol.OPNHASync:
		return r.opnHASync(ctx, c, out)
	case protocol.OPNKey:
		return r.opnPasteKey(ctx, c, p.OPNKey, out)
	}
	return -1, fmt.Errorf("onbekende OPNsense-actie %q", p.Action)
}

func (r *RunExecutor) execFn() execFunc {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		var buf bytes.Buffer
		code, err := r.cmd(ctx, "", append([]string{name}, args...), nil, &buf)
		if err == nil && code != 0 {
			err = &errStep{name, code}
		}
		return buf.Bytes(), err
	}
}

func (r *RunExecutor) disarm(ctx context.Context, unit string) { disarmOneShot(ctx, r.execFn(), unit) }

// opnResult is het gebruikelijke antwoord van een API-aanroep die iets
// opslaat of toepast.
type opnResult struct {
	Result      string         `json:"result"`
	Status      string         `json:"status"`
	Validations map[string]any `json:"validations"`
	Message     string         `json:"message"`
}

// opnCheckResult zegt of een antwoord een mislukking meldt.
func opnCheckResult(raw json.RawMessage) error {
	var res opnResult
	if json.Unmarshal(raw, &res) != nil {
		return nil
	}
	if len(res.Validations) > 0 {
		keys := make([]string, 0, len(res.Validations))
		for k := range res.Validations {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s: %v", k, res.Validations[k]))
		}
		return fmt.Errorf("OPNsense keurt dit af: %s", strings.Join(parts, "; "))
	}
	for _, v := range []string{res.Result, res.Status} {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "failed", "error":
			msg := res.Message
			if msg == "" {
				msg = "zonder uitleg"
			}
			return fmt.Errorf("OPNsense meldt een fout: %s", msg)
		}
	}
	return nil
}

// redactBody maakt een kopie van body met de geheime velden vervangen.
func redactBody(body map[string]any, secret []string) map[string]any {
	if len(secret) == 0 || body == nil {
		return body
	}
	var cp map[string]any
	b, _ := json.Marshal(body)
	_ = json.Unmarshal(b, &cp)
	for _, path := range secret {
		parts := strings.Split(path, ".")
		m := cp
		for i, p := range parts {
			if i == len(parts)-1 {
				if _, ok := m[p]; ok {
					m[p] = "[geheim]"
				}
				break
			}
			next, ok := m[p].(map[string]any)
			if !ok {
				break
			}
			m = next
		}
	}
	return cp
}

func (r *RunExecutor) opnApply(ctx context.Context, c *Collector, ch *protocol.OPNChange, out io.Writer, rs *protocol.RunStatus) (int, error) {
	if err := ch.Validate(); err != nil {
		return -1, err
	}
	if pend := opnPending(r.Root); pend != nil && pend.ID != ch.ID {
		return -1, fmt.Errorf("wijziging %s wacht nog op bevestiging; eerst die afronden", pend.ID)
	}
	if ch.RequireMaster {
		f := c.opnsense(ctx)
		if role := f.Role(); role != "master" {
			return -1, fmt.Errorf("deze firewall is niet de master voor alle CARP-VIP's (rol: %s); wijzigingen alleen op de master", orDash(role))
		}
	}
	o := c.opnAPI()
	if o.loadKey() == nil {
		return -1, errors.New("de agent heeft nog geen werkende API-sleutel van OPNsense")
	}
	// Herstelpunt.
	if err := os.MkdirAll(c.path(opnStateDir), 0o700); err != nil {
		return -1, err
	}
	snap := filepath.Join(opnStateDir, "change-"+ch.ID+".xml")
	cfg, err := os.ReadFile(c.path(opnConfigXML))
	if err != nil {
		return -1, fmt.Errorf("config.xml lezen mislukte: %w", err)
	}
	if err := os.WriteFile(c.path(snap), cfg, 0o600); err != nil {
		return -1, fmt.Errorf("het herstelpunt bewaren mislukte: %w", err)
	}
	now := time.Now()
	st := &opnChangeState{
		ID: ch.ID, Status: "pending", Snapshot: snap, Apply: ch.Apply, Description: ch.Description,
		Started: now.UTC(), Deadline: now.Add(time.Duration(ch.ConfirmWithin) * time.Second).UTC(), Secret: ch.Secret,
	}
	for _, call := range ch.Calls {
		st.Calls = append(st.Calls, call.Path)
	}
	path := opnChangePath(r.Root, ch.ID)
	if err := saveJSON(path, st); err != nil {
		return -1, err
	}
	_, _ = fmt.Fprintf(out, "herstelpunt bewaard (%d kB)\n", len(cfg)>>10)
	bin, _ := os.Executable()
	if err := armOneShot(ctx, r.execFn(), opnRollbackUnit(ch.ID), "ClusterForge: OPNsense-wijziging terugdraaien zonder bevestiging",
		time.Duration(ch.ConfirmWithin)*time.Second, bin, "opn-rollback", "-id", ch.ID); err != nil {
		_ = os.Remove(path)
		_ = os.Remove(c.path(snap))
		return -1, fmt.Errorf("de terugdraaitimer zetten mislukte, er is niets veranderd: %w", err)
	}
	_, _ = fmt.Fprintf(out, "terugdraaitimer staat: zonder bevestiging binnen %ds gaat de oude configuratie terug\n", ch.ConfirmWithin)

	fail := func(err error) (int, error) {
		_, _ = fmt.Fprintf(out, "FOUT: %v\n", err)
		r.disarm(ctx, opnRollbackUnit(ch.ID))
		if rerr := opnRollback(ctx, c, st, "mislukt: "+err.Error(), out); rerr != nil {
			return 1, fmt.Errorf("%v; terugzetten mislukte ook: %v", err, rerr)
		}
		return 1, err
	}
	for _, call := range append(append([]protocol.OPNCall{}, ch.Calls...), ch.Apply...) {
		shown, _ := json.Marshal(redactBody(call.Body, ch.Secret))
		if call.Body == nil {
			shown = []byte("{}")
		}
		_, _ = fmt.Fprintf(out, "\n$ POST /api/%s %s\n", call.Path, shown)
		body := call.Body
		if body == nil {
			body = map[string]any{}
		}
		raw, err := o.do(ctx, call.Path, body)
		if err != nil {
			return fail(err)
		}
		_, _ = fmt.Fprintln(out, lastLines(string(raw), 5))
		if err := opnCheckResult(raw); err != nil {
			return fail(err)
		}
		if rs.PVE == nil {
			rs.PVE = &protocol.PVERunResult{}
		}
		rs.PVE.Data = raw
	}
	_, _ = fmt.Fprintf(out, "\ntoegepast; wacht op bevestiging van ClusterForge tot %s\n", st.Deadline.Local().Format("15:04:05"))
	return 0, nil
}

func orDash(s string) string {
	if s == "" {
		return "geen CARP"
	}
	return s
}

func (r *RunExecutor) opnConfirm(ctx context.Context, c *Collector, id string, out io.Writer) (int, error) {
	path := opnChangePath(r.Root, id)
	st, err := loadOPNChange(path)
	if err != nil || st == nil {
		return -1, fmt.Errorf("wijziging %s is hier niet bekend", id)
	}
	switch st.Status {
	case "confirmed":
		_, _ = fmt.Fprintln(out, "al bevestigd")
		return 0, nil
	case "rolled_back":
		return 1, fmt.Errorf("te laat: de wijziging werd al teruggedraaid (%s)", st.Reason)
	}
	r.disarm(ctx, opnRollbackUnit(id))
	st.Status = "confirmed"
	if err := saveJSON(path, st); err != nil {
		return -1, err
	}
	_ = os.Remove(c.path(st.Snapshot))
	_, _ = fmt.Fprintln(out, "bevestigd: de terugdraaitimer is gestopt")
	return 0, nil
}

// opnRestorePHP zet een herstelpunt terug met OPNsense' eigen
// Config::restoreBackup en schrijft er een revisie van.
const opnRestorePHP = `<?php
require_once('script/load_phalcon.php');
use OPNsense\Core\Config;
$file = $argv[1];
$why = $argv[2] ?? 'ClusterForge';
$cnf = Config::getInstance();
if (!$cnf->restoreBackup($file)) {
    echo json_encode(['error' => 'herstelpunt is geen geldige configuratie']) . "\n";
    exit(1);
}
$cnf->save(['description' => $why, 'username' => 'cf-agent']);
echo json_encode(['status' => 'ok']) . "\n";
`

// opnRollback zet het herstelpunt van een wijziging terug en past de oude
// toestand toe.
func opnRollback(ctx context.Context, c *Collector, st *opnChangeState, reason string, out io.Writer) error {
	if st.Status != "pending" {
		_, _ = fmt.Fprintf(out, "wijziging %s is al %s\n", st.ID, st.Status)
		return nil
	}
	_, _ = fmt.Fprintf(out, "\nterugdraaien: %s\n", reason)
	script := c.path(filepath.Join(opnStateDir, "restore.php"))
	if err := os.WriteFile(script, []byte(opnRestorePHP), 0o600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(script) }()
	desc := "ClusterForge: wijziging " + st.ID + " teruggedraaid"
	res, err := c.run(ctx, opnPHP, script, c.path(st.Snapshot), desc)
	if err != nil {
		return fmt.Errorf("het herstelpunt terugzetten mislukte: %v: %s", err, lastLines(strings.TrimSpace(string(res)), 2))
	}
	_, _ = fmt.Fprintln(out, "oude config.xml teruggezet")
	// De oude toestand actief maken. Lukt de API niet, dan herladen we
	// minstens het filter, het belangrijkste om binnen te blijven.
	o := c.opnAPI()
	applied := false
	for _, call := range st.Apply {
		raw, err := o.do(ctx, call.Path, map[string]any{})
		if err == nil {
			err = opnCheckResult(raw)
		}
		if err != nil {
			_, _ = fmt.Fprintf(out, "toepassen via %s mislukte: %v\n", call.Path, err)
			continue
		}
		applied = true
	}
	if !applied {
		if res, err := c.run(ctx, opnConfigctl, "filter", "reload"); err != nil {
			_, _ = fmt.Fprintf(out, "configctl filter reload mislukte: %v %s\n", err, strings.TrimSpace(string(res)))
		} else {
			_, _ = fmt.Fprintln(out, "filter herladen")
		}
	}
	st.Status, st.Reason = "rolled_back", reason
	_ = os.Remove(c.path(st.Snapshot))
	return saveJSON(filepath.Join(c.Root, opnStateDir, "change-"+st.ID+".json"), st)
}

// RollbackOPNChange is cf-agent opn-rollback: de terugdraaitimer start het.
func RollbackOPNChange(ctx context.Context, id string, log *slog.Logger) error {
	if !protocol.ValidOPNChangeID(id) {
		return errors.New("ongeldig nummer van de wijziging")
	}
	c := &Collector{}
	st, err := loadOPNChange(opnChangePath("", id))
	if err != nil || st == nil {
		return fmt.Errorf("wijziging %s is hier niet bekend", id)
	}
	if st.Status != "pending" {
		log.Info("niets terug te draaien", "change", id, "status", st.Status)
		return nil
	}
	var buf bytes.Buffer
	err = opnRollback(ctx, c, st, "niet op tijd bevestigd door ClusterForge", &buf)
	log.Warn("OPNsense-wijziging teruggedraaid", "change", id, "output", strings.TrimSpace(buf.String()), "err", err)
	return err
}

// opnResume zet bij de start de timers van lopende wijzigingen terug die
// verdwenen (bijvoorbeeld na een reboot), met de tijd die er nog over was.
func (a *Agent) opnResume(ctx context.Context) {
	st := opnPending(a.Root)
	if st == nil {
		return
	}
	unit := opnRollbackUnit(st.ID)
	if oneShotActive(ctx, a.exec, unit) {
		return
	}
	left := max(time.Until(st.Deadline), 5*time.Second)
	bin, _ := a.binary()
	if err := armOneShot(ctx, a.exec, unit, "ClusterForge: OPNsense-wijziging terugdraaien zonder bevestiging", left, bin, "opn-rollback", "-id", st.ID); err != nil {
		a.Log.Error("terugdraaitimer van een OPNsense-wijziging opnieuw zetten mislukt", "change", st.ID, "err", err)
	}
}

func (r *RunExecutor) opnHASync(ctx context.Context, c *Collector, out io.Writer) (int, error) {
	f := c.opnsense(ctx)
	if f.HA.SyncTo == "" {
		return -1, errors.New("op deze firewall staat geen backup voor de configsync (System → High Availability)")
	}
	if role := f.Role(); role != "master" && role != "" {
		return -1, fmt.Errorf("synchroniseren alleen vanaf de master (rol: %s)", role)
	}
	_, _ = fmt.Fprintf(out, "$ configctl system ha exec exec_sync (naar %s)\n", f.HA.SyncTo)
	res, err := c.run(ctx, opnConfigctl, "system", "ha", "exec", "exec_sync")
	_, _ = fmt.Fprintln(out, strings.TrimSpace(string(res)))
	if err != nil {
		return 1, fmt.Errorf("synchroniseren mislukte: %w", err)
	}
	_, _ = fmt.Fprintln(out, "$ configctl system ha exec reload_templates")
	res, err = c.run(ctx, opnConfigctl, "system", "ha", "exec", "reload_templates")
	_, _ = fmt.Fprintln(out, strings.TrimSpace(string(res)))
	if err != nil {
		return 1, fmt.Errorf("templates herladen op de backup mislukte: %w", err)
	}
	var e struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if json.Unmarshal(lastOutputLine(res), &e) == nil && e.Status == "error" {
		return 1, fmt.Errorf("de backup antwoordt niet: %s", e.Message)
	}
	return 0, nil
}

func (r *RunExecutor) opnPasteKey(ctx context.Context, c *Collector, k *protocol.OPNKeyInput, out io.Writer) (int, error) {
	o := c.opnAPI()
	key := opnKey{Key: k.Key, Secret: k.Secret, User: "(geplakt)", Pasted: true}
	if _, err := o.doWith(ctx, &key, opnPing, nil); err != nil {
		return 1, fmt.Errorf("deze sleutel werkt niet: %w", err)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.saveKey(key); err != nil {
		return 1, err
	}
	_, _ = fmt.Fprintln(out, "de sleutel werkt en is bewaard op de firewall")
	return 0, nil
}

// --- Lezen -----------------------------------------------------------------

// handleOPN beantwoordt de leesverzoeken.
func (a *Agent) handleOPN(ctx context.Context, req protocol.PVERequest) protocol.PVEResponse {
	c := a.Collector
	if c == nil || !c.isOPNsense() {
		return protocol.PVEResponse{Error: "dit is geen OPNsense-firewall"}
	}
	var data json.RawMessage
	var err error
	switch req.Op {
	case protocol.OPNOpGet:
		data, err = c.opnAPI().do(ctx, req.Path, nil)
	case protocol.OPNOpPost:
		if !opnSearchPath.MatchString(req.Path) {
			return protocol.PVEResponse{Error: "alleen zoekopdrachten mogen als leesverzoek"}
		}
		body := map[string]any{}
		for k, v := range req.Params {
			body[k] = v
		}
		data, err = c.opnAPI().do(ctx, req.Path, body)
	case protocol.OPNOpConfigctl:
		data, err = opnConfigctlRead(ctx, c, req.Path)
	case protocol.OPNOpRevisions:
		data, err = json.Marshal(c.opnRevisions())
	case protocol.OPNOpRevision:
		data, err = c.opnRevision(req.Path)
	case protocol.OPNOpFacts:
		data, err = json.Marshal(c.opnsense(ctx))
	default:
		return protocol.PVEResponse{Error: "onbekend OPNsense-verzoek " + req.Op}
	}
	if err != nil {
		return protocol.PVEResponse{Error: err.Error()}
	}
	return protocol.PVEResponse{Data: data}
}

// Zoekopdrachten veranderen niets: search* en get* via POST.
var opnSearchPath = regexp.MustCompile(`^[a-z][a-z0-9_]*/[a-z][a-z0-9_]*/(search|get)[A-Za-z0-9_]*(/[A-Za-z0-9_.:-]+){0,2}$`)

func opnConfigctlRead(ctx context.Context, c *Collector, action string) (json.RawMessage, error) {
	if !slices.Contains(protocol.OPNReadActions, action) {
		return nil, fmt.Errorf("configctl %q mag niet als leesverzoek", action)
	}
	out, err := c.run(ctx, opnConfigctl, strings.Fields(action)...)
	if err != nil {
		return nil, fmt.Errorf("configctl %s: %w", action, err)
	}
	out = bytes.TrimSpace(out)
	if json.Valid(out) {
		return out, nil
	}
	return json.Marshal(string(out))
}

// opnRevision is één revisie van config.xml.
type opnRevision struct {
	File        string  `json:"file"`
	Time        float64 `json:"time"`
	Description string  `json:"description"`
	User        string  `json:"user"`
	Size        int64   `json:"size"`
}

// opnRevisions geeft de revisies in /conf/backup, nieuwste eerst, plus
// de huidige config als "current".
func (c *Collector) opnRevisions() []opnRevision {
	files, _ := filepath.Glob(c.path(opnBackupDir + "/config-*.xml"))
	var res []opnRevision
	add := func(path, name string) {
		b, err := os.ReadFile(path)
		if err != nil {
			return
		}
		rev := opnRevision{File: name, Size: int64(len(b))}
		if tree, err := parseXMLTree(b); err == nil {
			rev.Time, _ = strconv.ParseFloat(tree.text("revision/time"), 64)
			rev.Description = tree.text("revision/description")
			rev.User = tree.text("revision/username")
		}
		res = append(res, rev)
	}
	add(c.path(opnConfigXML), "current")
	for _, f := range files {
		add(f, filepath.Base(f))
	}
	sort.SliceStable(res[min(1, len(res)):], func(i, j int) bool {
		rest := res[min(1, len(res)):]
		return rest[i].Time > rest[j].Time
	})
	return res
}

var opnRevisionFile = regexp.MustCompile(`^config-[0-9]{9,11}(\.[0-9]{1,6})?\.xml$`)

// opnRevision geeft een revisie als tekst, met geheimen vervangen.
func (c *Collector) opnRevision(name string) (json.RawMessage, error) {
	path := c.path(opnConfigXML)
	if name != "current" {
		if !opnRevisionFile.MatchString(name) {
			return nil, fmt.Errorf("ongeldige revisie %q", name)
		}
		path = c.path(filepath.Join(opnBackupDir, name))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("revisie %s lezen mislukte: %w", name, err)
	}
	if len(b) > pveGetMax {
		return nil, fmt.Errorf("revisie %s is te groot", name)
	}
	return json.Marshal(redactConfigXML(string(b)))
}

// opnSecretTags zijn elementen in config.xml met een geheim als inhoud.
var opnSecretTags = regexp.MustCompile(`(?s)<((?:password|passwd|secret|prv|privatekey|private_key|pre-shared-key|psk|tls|shared_key|otp_seed|apikeys|authorizedkeys|bcrypt-hash|md5-hash|sha512-hash|nt-hash|key|pskey|sharedsecret|radius_secret|ldap_bindpw|auth_pass|community))(\s[^>]*)?>([^<]+)</([^>]+)>`)

// redactConfigXML vervangt de inhoud van geheime elementen door [geheim].
func redactConfigXML(s string) string {
	return opnSecretTags.ReplaceAllStringFunc(s, func(m string) string {
		sub := opnSecretTags.FindStringSubmatch(m)
		if len(sub) < 5 || sub[1] != sub[4] {
			return m
		}
		return "<" + sub[1] + sub[2] + ">[geheim]</" + sub[4] + ">"
	})
}
