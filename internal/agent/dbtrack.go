package agent

// De database in keepalived: cf-agent check db zegt of de MariaDB op deze
// node mag schrijven, en DBTrack zet die controle in de vrrp_instance met
// het VIP van de app. Zo staat het VIP altijd op een node met een gezonde
// database.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"regexp"
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/internal/textdiff"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	keepalivedConfPath = "/etc/keepalived/keepalived.conf"
	keepalivedInclude  = "include /etc/keepalived/conf.d/*.conf"
)

// CheckDB is cf-agent check db, dat keepalived elke twee seconden
// aanroept: nil als de database van engine op deze node mag schrijven.
// MariaDB (engine leeg of mariadb) moet Synced zijn in een Primary
// component van Galera met wsrep_ready ON; PostgreSQL moet de primary van
// Patroni zijn. Anders zegt de fout waarom niet.
func (a *Agent) CheckDB(ctx context.Context, engine string) error {
	switch engine {
	case "", protocol.DBMariaDB:
		return a.checkGalera(ctx)
	case protocol.DBPostgreSQL:
		return a.checkPatroni(ctx)
	}
	return fmt.Errorf("onbekende database %q; kies mariadb of postgresql", engine)
}

func (a *Agent) checkGalera(ctx context.Context) error {
	rows, err := a.maria(ctx, false, dbsql.MariaStatus)
	if err != nil {
		return fmt.Errorf("MariaDB antwoordt niet: %s", dbError(err))
	}
	st := keyValues(rows)
	switch {
	case st["wsrep_local_state"] != "4":
		return fmt.Errorf("deze Galera-node is %s (wsrep_local_state %s), niet Synced", orUnknown(st["wsrep_local_state_comment"]), orUnknown(st["wsrep_local_state"]))
	case st["wsrep_cluster_status"] != "Primary":
		return fmt.Errorf("het cluster is %s, geen Primary component", orUnknown(st["wsrep_cluster_status"]))
	case !strings.EqualFold(st["wsrep_ready"], "ON"):
		return errors.New("wsrep_ready staat uit")
	}
	return nil
}

func orUnknown(s string) string {
	if s == "" {
		return "onbekend"
	}
	return s
}

var authPassRe = regexp.MustCompile(`(?m)^(\s*auth_pass\s+)\S+`)

// maskAuthPass verbergt het wachtwoord van VRRP in een diff.
func maskAuthPass(s string) string { return authPassRe.ReplaceAllString(s, "${1}[geheim]") }

// dbTrack voert DBTrack uit: de diff, en met Apply de nieuwe
// keepalived.conf als keepalived -t hem goedkeurt.
func (a *Agent) dbTrack(ctx context.Context, req *protocol.DBTrackRequest) protocol.DBResponse {
	res := protocol.DBResponse{Servers: []protocol.DBServer{}}
	if req == nil || net.ParseIP(req.VIP) == nil {
		res.Error = "geen geldig VIP in het verzoek"
		return res
	}
	p, err := a.path(keepalivedConfPath)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if req.Include {
		return a.dbInclude(ctx, p, req)
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		res.Error = keepalivedConfPath + " ontbreekt; draait keepalived op deze node?"
		return res
	} else if err != nil {
		res.Error = err.Error()
		return res
	}
	conf := string(b)
	next, inst, err := addDBTrack(conf, req.VIP)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	diff, _ := textdiff.Diff(maskAuthPass(conf), maskAuthPass(next), keepalivedConfPath, keepalivedConfPath)
	res.Track = &protocol.DBTrackResult{Instance: inst, Changed: next != conf, Diff: diff}
	if !req.Apply || next == conf {
		return res
	}
	if f, err := a.path(protocol.DBTrackFile); err != nil {
		res.Error = err.Error()
		return res
	} else if _, err := os.Stat(f); err != nil {
		res.Error = protocol.DBTrackFile + " met vrrp_script " + protocol.DBTrackScript + " ontbreekt, dus keepalived.conf blijft zoals hij is"
		res.Track.Changed = false
		return res
	}
	if err := a.writeKeepalived(ctx, p, next); err != nil {
		res.Error = err.Error()
		res.Track.Changed = false
	}
	return res
}

// dbInclude voert DBTrack met Include uit: alleen de include van conf.d
// bovenaan keepalived.conf, voor een eigen VIP met zijn vrrp_instance in
// conf.d. Zonder keepalived.conf komt er een met alleen die include.
func (a *Agent) dbInclude(ctx context.Context, p string, req *protocol.DBTrackRequest) protocol.DBResponse {
	res := protocol.DBResponse{Servers: []protocol.DBServer{}}
	b, err := os.ReadFile(p)
	missing := errors.Is(err, fs.ErrNotExist)
	if err != nil && !missing {
		res.Error = err.Error()
		return res
	}
	conf := string(b)
	next, err := addInclude(conf, req.VIP)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	diff, _ := textdiff.Diff(maskAuthPass(conf), maskAuthPass(next), keepalivedConfPath, keepalivedConfPath)
	res.Track = &protocol.DBTrackResult{Changed: next != conf || missing, Diff: diff}
	if !req.Apply || !res.Track.Changed {
		return res
	}
	if missing {
		// Een nieuw bestand zoals het Debian-pakket het verwacht: van root,
		// alleen door root te schrijven.
		if err := os.WriteFile(p+".clusterforge", nil, 0o644); err != nil {
			res.Error = err.Error()
			res.Track.Changed = false
			return res
		}
		if err := os.Rename(p+".clusterforge", p); err != nil {
			res.Error = err.Error()
			res.Track.Changed = false
			return res
		}
	}
	if err := a.writeKeepalived(ctx, p, next); err != nil {
		if missing {
			_ = os.Remove(p)
		}
		res.Error = err.Error()
		res.Track.Changed = false
	}
	return res
}

// addInclude zet de include van conf.d bovenaan keepalived.conf, als die er
// nog niet staat. Het eigen VIP mag in geen vrrp_instance van conf zelf
// staan: dan zou keepalived het twee keer beheren.
func addInclude(conf, vip string) (string, error) {
	if strings.TrimSpace(conf) == "" {
		return "# Beheerd door ClusterForge: de VIP's van de database staan in /etc/keepalived/conf.d.\n" + keepalivedInclude + "\n", nil
	}
	lines := strings.Split(conf, "\n")
	root, err := parseBlocks(tokenize(lines))
	if err != nil {
		return "", fmt.Errorf("keepalived.conf is niet te lezen: %w", err)
	}
	for _, b := range root.kids {
		if len(b.name) > 1 && b.name[0] == "vrrp_instance" && hasVIP(b, vip) {
			return "", fmt.Errorf("het VIP %s staat al in vrrp_instance %s van %s; een eigen VIP van de database moet een vrij adres zijn",
				vip, strings.Join(b.name[1:], " "), keepalivedConfPath)
		}
	}
	if included(root) {
		return conf, nil
	}
	return strings.Join(insertLines(lines, 0,
		"# ClusterForge: de VIP's en controles van de database staan in /etc/keepalived/conf.d.",
		keepalivedInclude,
		""), "\n"), nil
}

// included zegt of keepalived.conf conf.d al opneemt.
func included(root *confBlock) bool {
	for _, s := range root.stmts {
		if len(s) == 2 && s[0].text == "include" && s[1].text == "/etc/keepalived/conf.d/*.conf" {
			return true
		}
	}
	return false
}

// writeKeepalived schrijft keepalived.conf via een tijdelijk bestand met
// dezelfde modus en eigenaar, en alleen als keepalived -t het goedkeurt.
func (a *Agent) writeKeepalived(ctx context.Context, p, conf string) error {
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	tmp := p + ".clusterforge"
	if err := os.WriteFile(tmp, []byte(conf), fi.Mode().Perm()); err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := os.Chmod(tmp, fi.Mode().Perm()); err != nil {
		return err
	}
	if uid, gid, ok := fileOwner(fi); ok {
		if err := os.Chown(tmp, uid, gid); err != nil {
			return err
		}
	}
	if out, err := a.exec(ctx, "keepalived", "-t", "-f", tmp); err != nil {
		return fmt.Errorf("keepalived -t keurt de nieuwe configuratie af, dus ClusterForge veranderde niets: %s", firstNonEmptyLine(string(out), err.Error()))
	}
	return os.Rename(tmp, p)
}

func firstNonEmptyLine(s ...string) string {
	for _, x := range s {
		for line := range strings.SplitSeq(x, "\n") {
			if l := strings.TrimSpace(line); l != "" {
				return l
			}
		}
	}
	return ""
}

// confTok is een woord of accolade in keepalived.conf, met zijn regel en
// kolom.
type confTok struct {
	text      string
	line, col int
}

// confBlock is een blok: de woorden ervoor, waar { en } staan, de regels
// erin en de blokken erin.
type confBlock struct {
	name        []string
	open, close confTok
	stmts       [][]confTok
	kids        []*confBlock
}

func (b *confBlock) kid(name string) *confBlock {
	for _, k := range b.kids {
		if len(k.name) > 0 && k.name[0] == name {
			return k
		}
	}
	return nil
}

// tokenize splitst keepalived.conf in woorden en accolades, zonder
// commentaar (# of ! tot het einde van de regel).
func tokenize(lines []string) []confTok {
	var out []confTok
	for i, line := range lines {
		if j := strings.IndexAny(line, "#!"); j >= 0 {
			line = line[:j]
		}
		start := -1
		flush := func(end int) {
			if start >= 0 {
				out = append(out, confTok{text: line[start:end], line: i, col: start})
				start = -1
			}
		}
		for j, r := range line {
			switch {
			case r == '{' || r == '}':
				flush(j)
				out = append(out, confTok{text: string(r), line: i, col: j})
			case r == ' ' || r == '\t' || r == '\r':
				flush(j)
			case start < 0:
				start = j
			}
		}
		flush(len(line))
	}
	return out
}

// parseBlocks maakt van de woorden een boom. Een { op een eigen regel hoort
// bij de regel ervoor, zoals keepalived dat leest.
func parseBlocks(toks []confTok) (*confBlock, error) {
	root := &confBlock{}
	stack := []*confBlock{root}
	var stmt []confTok
	line := -1
	end := func() {
		if len(stmt) > 0 {
			top := stack[len(stack)-1]
			top.stmts = append(top.stmts, stmt)
			stmt = nil
		}
	}
	for _, t := range toks {
		if t.line != line {
			end()
			line = t.line
		}
		top := stack[len(stack)-1]
		switch t.text {
		case "{":
			name := stmt
			stmt = nil
			if len(name) == 0 && len(top.stmts) > 0 {
				// De woorden stonden op de regel ervoor.
				name = top.stmts[len(top.stmts)-1]
				top.stmts = top.stmts[:len(top.stmts)-1]
			}
			b := &confBlock{open: t}
			for _, w := range name {
				b.name = append(b.name, w.text)
			}
			top.kids = append(top.kids, b)
			stack = append(stack, b)
		case "}":
			end()
			if len(stack) == 1 {
				return nil, fmt.Errorf("regel %d: een } te veel", t.line+1)
			}
			top.close = t
			stack = stack[:len(stack)-1]
		default:
			stmt = append(stmt, t)
		}
	}
	end()
	if len(stack) > 1 {
		return nil, fmt.Errorf("het blok %s op regel %d wordt niet gesloten", strings.Join(stack[len(stack)-1].name, " "), stack[len(stack)-1].open.line+1)
	}
	return root, nil
}

func indentOf(s string) string { return s[:len(s)-len(strings.TrimLeft(s, " \t"))] }

// innerIndent is het inspringen van de eerste regel in een blok, of dat van
// de } met vier spaties erbij.
func innerIndent(lines []string, b *confBlock) string {
	for i := b.open.line + 1; i < b.close.line; i++ {
		if t := strings.TrimSpace(lines[i]); t != "" && t[0] != '#' && t[0] != '!' {
			return indentOf(lines[i])
		}
	}
	return indentOf(lines[b.close.line]) + "    "
}

// addDBTrack zet track_script cf_db in de vrrp_instance met het VIP, en
// bovenaan een include van /etc/keepalived/conf.d, waar ClusterForge de
// vrrp_script cf_db zet. keepalived zoekt een track_script op terwijl hij
// leest, dus de include moet vóór de instance staan. Staat alles er al,
// dan komt conf ongewijzigd terug.
func addDBTrack(conf, vip string) (string, string, error) {
	lines := strings.Split(conf, "\n")
	root, err := parseBlocks(tokenize(lines))
	if err != nil {
		return "", "", fmt.Errorf("keepalived.conf is niet te lezen: %w", err)
	}
	var inst *confBlock
	for _, b := range root.kids {
		if len(b.name) == 0 || b.name[0] != "vrrp_instance" {
			continue
		}
		if hasVIP(b, vip) {
			if inst != nil {
				return "", "", fmt.Errorf("%s staat in meer dan één vrrp_instance", vip)
			}
			inst = b
		}
	}
	if inst == nil {
		return "", "", fmt.Errorf("geen vrrp_instance in %s heeft het VIP %s", keepalivedConfPath, vip)
	}
	name := strings.Join(inst.name[1:], " ")
	if inst.open.line == inst.close.line {
		return "", "", fmt.Errorf("vrrp_instance %s staat op één regel; ClusterForge past hem niet aan", name)
	}

	ts := inst.kid("track_script")
	tracked := false
	if ts != nil {
		for _, s := range ts.stmts {
			for _, w := range s {
				tracked = tracked || w.text == protocol.DBTrackScript
			}
		}
	}
	if !tracked {
		switch {
		case ts != nil && strings.TrimSpace(lines[ts.close.line][:ts.close.col]) != "":
			// track_script { chk_nginx } op één regel, of chk_nginx } op
			// de laatste regel: cf_db vóór de }.
			l := lines[ts.close.line]
			lines[ts.close.line] = l[:ts.close.col] + protocol.DBTrackScript + " " + l[ts.close.col:]
		case ts != nil:
			indent := indentOf(lines[ts.close.line]) + "    "
			if n := len(ts.stmts); n > 0 {
				indent = indentOf(lines[ts.stmts[n-1][0].line])
			}
			lines = insertLines(lines, ts.close.line, indent+protocol.DBTrackScript)
		default:
			if strings.TrimSpace(lines[inst.close.line][:inst.close.col]) != "" {
				return "", "", fmt.Errorf("de } van vrrp_instance %s staat niet op een eigen regel; ClusterForge past hem niet aan", name)
			}
			indent := innerIndent(lines, inst)
			step := "    "
			if strings.HasPrefix(indent, "\t") {
				step = "\t"
			}
			lines = insertLines(lines, inst.close.line,
				indent+"# ClusterForge: het VIP alleen op een node met een gezonde database.",
				indent+"track_script {",
				indent+step+protocol.DBTrackScript,
				indent+"}")
		}
	}

	if !included(root) {
		lines = insertLines(lines, 0,
			"# ClusterForge: de controle van de database (vrrp_script "+protocol.DBTrackScript+") staat in "+protocol.DBTrackFile+".",
			keepalivedInclude,
			"")
	}
	return strings.Join(lines, "\n"), name, nil
}

// hasVIP is true als het VIP in de virtual_ipaddress van de instance staat.
func hasVIP(inst *confBlock, vip string) bool {
	for _, k := range inst.kids {
		if len(k.name) == 0 || (k.name[0] != "virtual_ipaddress" && k.name[0] != "virtual_ipaddress_excluded") {
			continue
		}
		for _, s := range k.stmts {
			if addr, _, _ := strings.Cut(s[0].text, "/"); addr == vip {
				return true
			}
		}
	}
	return false
}

func insertLines(lines []string, at int, add ...string) []string {
	out := make([]string, 0, len(lines)+len(add))
	out = append(out, lines[:at]...)
	out = append(out, add...)
	return append(out, lines[at:]...)
}
