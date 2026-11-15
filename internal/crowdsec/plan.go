package crowdsec

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"
)

// DefaultWhitelist zijn de netwerken die CrowdSec nooit bant: de eigen
// netwerken, zodat een proxy, een clusternode of Jonas' pc nooit geband
// wordt.
var DefaultWhitelist = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "::1/128", "fc00::/7"}

// NodeConfig is wat Jonas per node instelt.
type NodeConfig struct {
	Bouncer bool
	// Extra zijn profielen die Jonas zelf aanzet; Disabled die hij uitzet.
	Extra    []string
	Disabled []string
}

// PlannedProfile is een profiel in het plan van een node.
type PlannedProfile struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	// Origin is auto (herkend), extra (zelf aangezet) of off (herkend maar
	// uitgezet).
	Origin  string   `json:"origin"`
	Reason  string   `json:"reason,omitempty"`
	Note    string   `json:"note,omitempty"`
	Sources []string `json:"sources,omitempty"`
}

// Plan is de gewenste CrowdSec-configuratie van een node.
type Plan struct {
	Profiles    []PlannedProfile `json:"profiles"`
	Collections []string         `json:"collections"`
	// Files zijn de bestanden die het script schrijft (pad → inhoud).
	Files     map[string]string `json:"-"`
	Bouncer   bool              `json:"bouncer"`
	Whitelist []string          `json:"whitelist"`
	Hash      string            `json:"hash"`
}

const (
	acquisDir     = "/etc/crowdsec/acquis.d"
	whitelistFile = "/etc/crowdsec/parsers/s02-enrich/cf-whitelist.yaml"
	bouncerLocal  = "/etc/crowdsec/bouncers/crowdsec-firewall-bouncer.yaml.local"
)

// ValidWhitelist controleert de whitelist uit de instellingen.
func ValidWhitelist(list []string) error {
	for _, w := range list {
		if _, err := netip.ParsePrefix(w); err == nil {
			continue
		}
		if _, err := netip.ParseAddr(w); err == nil {
			continue
		}
		return fmt.Errorf("ongeldig adres of netwerk %q", w)
	}
	return nil
}

// Build rekent het plan van een node uit.
func Build(s Signals, cfg NodeConfig, whitelist []string) Plan {
	p := Plan{Files: map[string]string{}, Bouncer: cfg.Bouncer, Whitelist: slices.Clone(whitelist)}
	cols := map[string]bool{}
	for _, prof := range Catalog {
		m := prof.Detect(s)
		pp := PlannedProfile{Key: prof.Key, Name: prof.Name}
		switch {
		case slices.Contains(cfg.Disabled, prof.Key) && prof.Key != "linux":
			if m == nil {
				continue
			}
			pp.Origin, pp.Reason = "off", m.Reason
			p.Profiles = append(p.Profiles, pp)
			continue
		case m != nil:
			pp.Origin, pp.Reason, pp.Note = "auto", m.Reason, m.Note
		case slices.Contains(cfg.Extra, prof.Key):
			pp.Origin = "extra"
			m = &Match{Note: "Niet herkend op deze node: alleen de collection staat erop, zonder logbron."}
			pp.Note = m.Note
		default:
			continue
		}
		for _, c := range prof.Collections {
			cols[c] = true
		}
		if len(m.Sources) > 0 {
			p.Files[acquisDir+"/cf-"+prof.Key+".yaml"] = acquisYAML(prof, m.Sources)
		}
		for _, src := range m.Sources {
			pp.Sources = append(pp.Sources, describeSource(src))
		}
		p.Profiles = append(p.Profiles, pp)
	}
	for c := range cols {
		p.Collections = append(p.Collections, c)
	}
	sort.Strings(p.Collections)
	p.Files[whitelistFile] = whitelistYAML(whitelist)
	if cfg.Bouncer {
		hooks := "  - input\n"
		if s.Docker || len(s.Containers) > 0 {
			hooks += "  - forward\n"
		}
		p.Files[bouncerLocal] = "# Beheerd door ClusterForge.\n# Met Docker ook forward, zodat een ban de gepubliceerde poorten van containers raakt.\nnftables_hooks:\n" + hooks
	}
	p.Hash = p.hash()
	return p
}

func (p Plan) hash() string {
	h := sha256.New()
	fmt.Fprintf(h, "v1\nbouncer=%v\n", p.Bouncer)
	for _, c := range p.Collections {
		fmt.Fprintf(h, "col %s\n", c)
	}
	keys := make([]string, 0, len(p.Files))
	for k := range p.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "file %s %d\n%s\n", k, len(p.Files[k]), p.Files[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func describeSource(s Source) string {
	switch s.Kind {
	case "file":
		return strings.Join(s.Files, ", ") + " (" + s.Type + ")"
	case "journalctl":
		u := make([]string, len(s.Journal))
		for i, j := range s.Journal {
			u[i] = strings.TrimSuffix(strings.TrimPrefix(j, "_SYSTEMD_UNIT="), ".service")
		}
		return "journald " + strings.Join(u, ", ") + " (" + s.Type + ")"
	case "docker":
		return "container " + strings.Join(s.Containers, ", ") + " (" + s.Type + ")"
	}
	return s.Kind
}

// yamlQuote zet een waarde tussen dubbele aanhalingstekens.
func yamlQuote(v string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
}

func acquisYAML(prof Profile, sources []Source) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Beheerd door ClusterForge: profiel %s. Wijzigingen worden overschreven.\n", prof.Name)
	for i, s := range sources {
		if i > 0 {
			b.WriteString("---\n")
		}
		fmt.Fprintf(&b, "source: %s\n", s.Kind)
		list := func(key string, vals []string) {
			fmt.Fprintf(&b, "%s:\n", key)
			for _, v := range vals {
				fmt.Fprintf(&b, "  - %s\n", yamlQuote(v))
			}
		}
		switch s.Kind {
		case "file":
			list("filenames", s.Files)
		case "journalctl":
			list("journalctl_filter", s.Journal)
		case "docker":
			list("container_name", s.Containers)
		}
		fmt.Fprintf(&b, "labels:\n  type: %s\n", yamlQuote(s.Type))
	}
	return b.String()
}

func whitelistYAML(list []string) string {
	var ips, cidrs []string
	for _, w := range list {
		if _, err := netip.ParsePrefix(w); err == nil {
			cidrs = append(cidrs, w)
		} else if _, err := netip.ParseAddr(w); err == nil {
			ips = append(ips, w)
		}
	}
	var b strings.Builder
	b.WriteString("# Beheerd door ClusterForge: deze adressen worden nooit geband.\n")
	b.WriteString("name: clusterforge/whitelist\ndescription: \"ClusterForge: eigen netwerken\"\nwhitelist:\n  reason: \"ClusterForge whitelist\"\n")
	b.WriteString("  ip:\n")
	if len(ips) == 0 {
		b.WriteString("    []\n")
	}
	for _, ip := range ips {
		fmt.Fprintf(&b, "    - %s\n", yamlQuote(ip))
	}
	b.WriteString("  cidr:\n")
	if len(cidrs) == 0 {
		b.WriteString("    []\n")
	}
	for _, c := range cidrs {
		fmt.Fprintf(&b, "    - %s\n", yamlQuote(c))
	}
	return b.String()
}
