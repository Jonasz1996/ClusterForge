package opnsense

// Welke aanroepen van de OPNsense-API ClusterForge doet (fase 34). De agent
// voert elke wijziging uit die er goed uitziet; hier staat wat de GUI mag:
// pad, velden, welk onderdeel opnieuw geladen wordt en of de wijziging de
// toegang kan raken (dan tik je de naam van het cluster over).

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	segUUID  = `(?P<uuid>[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`
	aliasKey = `enabled|name|type|proto|interface|counters|updatefreq|content|path_expression|authtype|username|password|categories|description`
)

// Level zegt hoe voorzichtig een wijziging is.
const (
	// LevelNormal: herstelpunt, timer en proeven.
	LevelNormal = "normal"
	// LevelAccess: kan de toegang tot de firewall raken (regels, NAT,
	// interfaces, CARP); je tikt de naam van het cluster over.
	LevelAccess = "access"
)

type rule struct {
	path *regexp.Regexp
	// root is de enige sleutel van het lichaam ("alias"); leeg is een
	// aanroep zonder lichaam.
	root  string
	keys  *regexp.Regexp
	title func(m map[string]string, body map[string]any) string
	// apply is de aanroep die de wijziging actief maakt.
	apply string
	level string
}

func rl(path, root, keys, apply, level string, title func(m map[string]string, body map[string]any) string) *rule {
	x := &rule{path: regexp.MustCompile("^" + path + "$"), root: root, apply: apply, level: level, title: title}
	if keys != "" {
		x.keys = regexp.MustCompile("^(" + keys + ")$")
	}
	return x
}

// field geeft een veld uit het lichaam als tekst.
func field(body map[string]any, root, key string) string {
	m, _ := body[root].(map[string]any)
	if m == nil {
		return ""
	}
	return fmt.Sprint(m[key])
}

// Velden van firewall- en NAT-regels verschillen per versie; de wortel
// ("rule") en het pad houden ze binnen dat onderdeel.
const ruleKey = `[a-z][a-z0-9_-]{0,40}`

func ruleTitle(what, verb string) func(map[string]string, map[string]any) string {
	return func(_ map[string]string, b map[string]any) string {
		d := field(b, "rule", "description")
		if d == "" || d == "<nil>" {
			d = field(b, "rule", "descr")
		}
		if d != "" && d != "<nil>" {
			return what + " \u201c" + d + "\u201d " + verb
		}
		return what + " " + verb
	}
}

func toggleTitle(what string) func(map[string]string, map[string]any) string {
	return func(m map[string]string, _ map[string]any) string {
		switch m["on"] {
		case "1":
			return what + " aanzetten"
		case "0":
			return what + " uitzetten"
		}
		return what + " aan/uit"
	}
}

// natRules zijn de regels voor één soort regels (filter, d_nat ...): elk
// raakt mogelijk de toegang.
func natRules(ctrl, what string) []*rule {
	apply := "firewall/" + ctrl + "/apply"
	return []*rule{
		rl(`firewall/`+ctrl+`/addRule`, "rule", ruleKey, apply, LevelAccess, ruleTitle(what, "toevoegen")),
		rl(`firewall/`+ctrl+`/setRule/`+segUUID, "rule", ruleKey, apply, LevelAccess, ruleTitle(what, "wijzigen")),
		rl(`firewall/`+ctrl+`/delRule/`+segUUID, "", "", apply, LevelAccess,
			func(map[string]string, map[string]any) string { return what + " verwijderen" }),
		rl(`firewall/`+ctrl+`/toggleRule/`+segUUID+`(/(?P<on>[01]))?`, "", "", apply, LevelAccess, toggleTitle(what)),
	}
}

var rules = append(append(append(append(append(append([]*rule{
	rl(`firewall/filter/moveRuleBefore/`+segUUID+`/(?P<before>[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`, "", "",
		"firewall/filter/apply", LevelAccess, func(map[string]string, map[string]any) string { return "firewallregel verplaatsen" }),
},
	natRules("filter", "firewallregel")...),
	natRules("d_nat", "port forward")...),
	natRules("source_nat", "uitgaande NAT-regel")...),
	natRules("one_to_one", "1:1-regel")...),
	natRules("npt", "NPT-regel")...),
	baseRules...)

var baseRules = []*rule{
	// --- Aliassen -------------------------------------------------------
	rl(`firewall/alias/addItem`, "alias", aliasKey, "firewall/alias/reconfigure", LevelNormal,
		func(_ map[string]string, b map[string]any) string {
			return "alias " + field(b, "alias", "name") + " aanmaken"
		}),
	rl(`firewall/alias/setItem/`+segUUID, "alias", aliasKey, "firewall/alias/reconfigure", LevelNormal,
		func(_ map[string]string, b map[string]any) string {
			if n := field(b, "alias", "name"); n != "" {
				return "alias " + n + " wijzigen"
			}
			return "alias wijzigen"
		}),
	rl(`firewall/alias/delItem/`+segUUID, "", "", "firewall/alias/reconfigure", LevelNormal,
		func(map[string]string, map[string]any) string { return "alias verwijderen" }),
	rl(`firewall/alias/toggleItem/`+segUUID+`(/(?P<on>[01]))?`, "", "", "firewall/alias/reconfigure", LevelNormal, toggleTitle("alias")),
}

// Plan is wat de server van een reeks aanroepen maakt.
type Plan struct {
	Titles []string           `json:"titles"`
	Apply  []protocol.OPNCall `json:"apply"`
	Level  string             `json:"level"`
}

// Title is één zin voor de taak en het logboek.
func (p Plan) Title() string {
	if len(p.Titles) == 1 {
		return capitalize(p.Titles[0])
	}
	return capitalize(p.Titles[0]) + fmt.Sprintf(" (+%d)", len(p.Titles)-1)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// check zoekt voor elke aanroep de regel en bouwt het plan.
func check(calls []protocol.OPNCall) (Plan, error) {
	if len(calls) == 0 {
		return Plan{}, fmt.Errorf("geen aanroepen")
	}
	if len(calls) > 50 {
		return Plan{}, fmt.Errorf("te veel aanroepen in één wijziging (hoogstens 50)")
	}
	p := Plan{Level: LevelNormal}
	var apply []string
	for _, c := range calls {
		x, m, err := match(c)
		if err != nil {
			return Plan{}, err
		}
		p.Titles = append(p.Titles, x.title(m, c.Body))
		if x.apply != "" && !slices.Contains(apply, x.apply) {
			apply = append(apply, x.apply)
		}
		if x.level == LevelAccess {
			p.Level = LevelAccess
		}
	}
	sort.Strings(apply)
	for _, a := range apply {
		p.Apply = append(p.Apply, protocol.OPNCall{Path: a})
	}
	return p, nil
}

func match(c protocol.OPNCall) (*rule, map[string]string, error) {
	if !protocol.ValidOPNsenseAPIPath(c.Path) {
		return nil, nil, fmt.Errorf("ongeldig API-pad %q", c.Path)
	}
	for _, x := range rules {
		sm := x.path.FindStringSubmatch(c.Path)
		if sm == nil {
			continue
		}
		m := map[string]string{}
		for i, n := range x.path.SubexpNames() {
			if n != "" {
				m[n] = sm[i]
			}
		}
		if err := x.checkBody(c); err != nil {
			return nil, nil, err
		}
		return x, m, nil
	}
	return nil, nil, fmt.Errorf("ClusterForge doet %s niet", c.Path)
}

func (x *rule) checkBody(c protocol.OPNCall) error {
	if x.root == "" {
		if len(c.Body) > 0 {
			return fmt.Errorf("%s heeft geen velden", c.Path)
		}
		return nil
	}
	if len(c.Body) != 1 {
		return fmt.Errorf("%s verwacht precies één onderdeel %q", c.Path, x.root)
	}
	inner, ok := c.Body[x.root].(map[string]any)
	if !ok {
		return fmt.Errorf("%s verwacht velden onder %q", c.Path, x.root)
	}
	for k, v := range inner {
		if x.keys == nil || !x.keys.MatchString(k) {
			return fmt.Errorf("veld %s mag niet bij %s", k, c.Path)
		}
		if err := scalar(k, v, true); err != nil {
			return err
		}
	}
	return nil
}

// scalar keurt een veldwaarde: tekst, getal of waar/onwaar, en bij nested
// één niveau dieper (zoals source.network bij port forwards).
func scalar(k string, v any, nested bool) error {
	switch t := v.(type) {
	case string:
		if len(t) > 65536 {
			return fmt.Errorf("veld %s is te lang", k)
		}
	case bool, float64, nil:
	case map[string]any:
		if !nested {
			return fmt.Errorf("veld %s is te diep genest", k)
		}
		for kk, vv := range t {
			if !subKey.MatchString(kk) {
				return fmt.Errorf("veld %s.%s mag niet", k, kk)
			}
			if err := scalar(k+"."+kk, vv, false); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("veld %s moet tekst zijn", k)
	}
	return nil
}

var subKey = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,40}$`)

// readDenied zijn API-onderdelen die een kijker niet leest: gebruikers,
// sleutels, certificaten, back-ups en VPN-sleutels.
var readDenied = []string{
	"core/backup", "system/user", "auth/", "trust/", "wireguard/client", "wireguard/server", "ipsec/pre_shared_keys",
	"openvpn/instances", "openvpn/client_overwrites", "acmeclient/accounts",
}

// readAllowed: een beheerder leest alles behalve de API-sleutels zelf; een
// kijker niets uit readDenied.
func readAllowed(path string, admin bool) bool {
	if strings.HasPrefix(path, "auth/") || strings.Contains(strings.ToLower(path), "apikey") {
		return false
	}
	if admin {
		return true
	}
	for _, p := range readDenied {
		if strings.HasPrefix(path, p) {
			return false
		}
	}
	return true
}

// IsSearch is true voor een zoekopdracht (search* of get* met POST), zoals
// firewall/alias/searchItem.
func IsSearch(path string) bool {
	parts := strings.Split(path, "/")
	return len(parts) >= 3 && strings.HasPrefix(parts[2], "search")
}
