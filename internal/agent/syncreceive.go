package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// SyncReceiver is `cf-agent sync-receive --from <node>`: het enige wat de
// sleutel van een bron op dit doel mag. Het laat alleen `rsync --server`
// toe, naar de doelmap van een sync van die bron, met --delete alleen als
// die sync het mag, en weigert als deze node het VIP van zijn cluster
// heeft.
type SyncReceiver struct {
	// Root is de bestandssysteemwortel van receive.json; leeg is "/".
	Root string
	// Addresses geeft de adressen van de node; nil leest de interfaces.
	Addresses func() []string
	Stderr    io.Writer
	// Exec start rsync; nil vervangt het proces (syscall.Exec).
	Exec func(argv []string) error
}

// syncShortOpts zijn de korte opties die rsync --server krijgt, zoals
// -logDtpre.iLsfxCIvu.
var syncShortOpts = regexp.MustCompile(`^-[A-Za-z.]+$`)

// syncLongOpts zijn de lange opties die de bron mag sturen; --delete en
// verwanten alleen met toestemming.
var syncLongOpts = []string{"--partial", "--numeric-ids", "--stats", "--log-format=%i", "--inplace"}

var syncLongArg = regexp.MustCompile(`^--(bwlimit|timeout)=\d+$`)

// Main controleert het commando (SSH_ORIGINAL_COMMAND) en start rsync. Het
// geeft de exitcode bij een weigering; bij succes keert het niet terug.
func (r *SyncReceiver) Main(from, command string) int {
	refuse := func(format string, args ...any) int {
		w := r.Stderr
		if w == nil {
			w = os.Stderr
		}
		_, _ = fmt.Fprintf(w, protocol.SyncRefused+": "+format+"\n", args...)
		return 3
	}
	data, err := os.ReadFile(filepath.Join(r.Root, protocol.SyncDir, "receive.json"))
	if err != nil {
		return refuse("deze node ontvangt geen syncs")
	}
	var receives []protocol.SyncReceive
	if err := json.Unmarshal(data, &receives); err != nil {
		return refuse("receive.json is onleesbaar")
	}
	var mine []protocol.SyncReceive
	for _, rc := range receives {
		if rc.From == from {
			mine = append(mine, rc)
		}
	}
	if len(mine) == 0 {
		return refuse("geen sync van deze bron")
	}
	argv, ok := splitRemote(command)
	if !ok || len(argv) < 4 || argv[0] != "rsync" || argv[1] != "--server" {
		return refuse("alleen rsync naar de doelmap")
	}
	target, dot := argv[len(argv)-1], argv[len(argv)-2]
	if dot != "." {
		return refuse("alleen rsync naar de doelmap")
	}
	if !strings.HasSuffix(target, "/") {
		target += "/"
	}
	i := slices.IndexFunc(mine, func(rc protocol.SyncReceive) bool { return rc.Path == target && path.Clean(target)+"/" == target })
	if i < 0 {
		return refuse("%s is geen doelmap van een sync van deze bron", target)
	}
	rc := mine[i]
	for _, o := range argv[2 : len(argv)-2] {
		switch {
		case o == "--sender":
			return refuse("deze sleutel mag niet lezen, alleen schrijven")
		case strings.HasPrefix(o, "--delete"):
			if !rc.Delete {
				return refuse("deze sync mag op het doel niets weghalen")
			}
		case syncShortOpts.MatchString(o), slices.Contains(syncLongOpts, o), syncLongArg.MatchString(o):
		default:
			return refuse("optie %s is niet toegestaan", o)
		}
	}
	addrs := hostAddresses()
	if r.Addresses != nil {
		addrs = r.Addresses()
	}
	if slices.ContainsFunc(rc.VIPs, func(v string) bool { return slices.Contains(addrs, v) }) {
		return refuse("deze node heeft het VIP van zijn cluster; een kopie overschrijft de actieve node niet")
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return refuse("de doelmap maken: %v", err)
	}
	if r.Exec != nil {
		if err := r.Exec(argv); err != nil {
			return refuse("rsync: %v", err)
		}
		return 0
	}
	bin, err := exec.LookPath("rsync")
	if err != nil {
		return refuse("rsync staat niet op deze node")
	}
	err = syscall.Exec(bin, argv, os.Environ())
	return refuse("rsync starten: %v", err)
}

// splitRemote splitst het commando van ssh op spaties; een backslash maakt
// het volgende teken letterlijk. Aanhalingstekens laat rsync niet toe.
func splitRemote(s string) ([]string, bool) {
	var out []string
	var cur strings.Builder
	have := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			have = true
		case c == ' ' || c == '\t':
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		case c == '\'' || c == '"' || c == '\n' || c == ';' || c == '|' || c == '&' || c == '$' || c == '`':
			return nil, false
		default:
			cur.WriteByte(c)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out, true
}
