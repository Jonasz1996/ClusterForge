package agent

// Zelfherstel voor Patroni: de agent leest de config van Patroni met het
// bestand en de regel van elk adres, en probeert de etcd-adressen. Een
// Patroni die etcd niet bereikt, weet niet wie de primary is; een
// connect_address die de node niet meer heeft, laat replica's en apps
// naar een verkeerd adres zoeken. De agent wijzigt hier niets.

import (
	"cmp"
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Waar Patroni zijn config kan hebben, als ExecStart van patroni.service
// het niet zegt.
var patroniConfigs = []string{"/etc/patroni/config.yml", "/etc/patroni/patroni.yml", "/etc/patroni/clusterforge.yml", "/etc/patroni.yml"}

// healDialTimeout is hoe lang de agent op een etcd-adres wacht.
const healDialTimeout = 2 * time.Second

// De DCS'en die Patroni kent; alleen etcd controleert de agent.
var patroniDCS = []string{"etcd3", "etcd", "consul", "zookeeper", "exhibitor", "kubernetes", "raft"}

// healPatroni vult de config van Patroni, de datamap uit die config en of
// de etcd-adressen te bereiken zijn.
func (a *Agent) healPatroni(ctx context.Context, e *protocol.HealEngine) {
	p := &protocol.HealPatroni{}
	e.Patroni = p
	units := a.patroniUnits(ctx)
	p.Etcd = units["etcd"]["ActiveState"]
	if units["etcd"]["LoadState"] == "not-found" {
		p.Etcd = ""
	}
	p.File = a.patroniFile(units["patroni"])
	if p.File == "" {
		p.Error = "geen configbestand van Patroni gevonden"
		return
	}
	c, err := a.readPatroni(p.File)
	if err != nil {
		p.Error = err.Error()
		return
	}
	p.Scope, p.Name, p.DCS = c.scope, c.name, c.dcs
	if c.dataDir != "" {
		e.DataDir = c.dataDir
	}
	var addrs []string
	for _, s := range c.endpoints {
		p.Settings = append(p.Settings, s)
		if !slices.Contains(addrs, s.Value) {
			addrs = append(addrs, s.Value)
		}
	}
	for _, k := range []string{"restapi.connect_address", "postgresql.connect_address"} {
		if s, ok := c.settings[k]; ok {
			p.Settings = append(p.Settings, s)
		}
	}
	if c.srv != "" && len(addrs) == 0 {
		p.Error = "Patroni zoekt etcd via DNS (" + c.srv + "); de agent controleert die adressen niet"
	}
	p.Endpoints = a.reach(ctx, addrs)
}

// patroniUnits leest ExecStart en Environment van patroni.service en de
// toestand van etcd.service.
func (a *Agent) patroniUnits(ctx context.Context) map[string]map[string]string {
	out, _ := a.dbRun(ctx, "systemctl", "show", "--property=Id,LoadState,ActiveState,ExecStart,Environment", "patroni.service", "etcd.service")
	units := map[string]map[string]string{}
	for block := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n\n") {
		kv := map[string]string{}
		for line := range strings.SplitSeq(block, "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				kv[k] = v
			}
		}
		if id := strings.TrimSuffix(kv["Id"], ".service"); id != "" {
			units[id] = kv
		}
	}
	return units
}

// patroniFile is het configbestand uit ExecStart, met de variabelen uit
// Environment ingevuld, of het eerste van patroniConfigs dat bestaat.
func (a *Agent) patroniFile(unit map[string]string) string {
	env := map[string]string{}
	for f := range strings.FieldsSeq(unit["Environment"]) {
		if k, v, ok := strings.Cut(f, "="); ok {
			env[k] = v
		}
	}
	if _, argv, ok := strings.Cut(unit["ExecStart"], "argv[]="); ok {
		argv, _, _ = strings.Cut(argv, " ;")
		args := strings.Fields(argv)
		for i := len(args) - 1; i > 0; i-- {
			if strings.HasPrefix(args[i], "-") {
				continue
			}
			f := filepath.Clean(os.Expand(args[i], func(k string) string { return env[k] }))
			if filepath.IsAbs(f) && a.exists(f) {
				return f
			}
			break
		}
	}
	for _, f := range patroniConfigs {
		if a.exists(f) {
			return f
		}
	}
	return ""
}

func (a *Agent) exists(path string) bool {
	p, err := a.path(path)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// patroniConfig is wat Zelfherstel uit de config van Patroni haalt.
type patroniConfig struct {
	scope, name, dataDir, dcs, srv string
	// endpoints zijn de etcd-adressen als host:poort, elk met zijn regel.
	endpoints []protocol.HealSetting
	settings  map[string]protocol.HealSetting
}

// readPatroni leest de config; is file een map, dan zoals Patroni de
// *.yml en *.yaml erin op naam, waarbij een later bestand een waarde van
// een eerder vervangt.
func (a *Agent) readPatroni(file string) (patroniConfig, error) {
	c := patroniConfig{settings: map[string]protocol.HealSetting{}}
	p, err := a.path(file)
	if err != nil {
		return c, err
	}
	files := []string{file}
	if info, err := os.Stat(p); err == nil && info.IsDir() {
		entries, err := os.ReadDir(p)
		if err != nil {
			return c, err
		}
		files = nil
		for _, e := range entries {
			if !e.IsDir() && (strings.HasSuffix(e.Name(), ".yml") || strings.HasSuffix(e.Name(), ".yaml")) {
				files = append(files, filepath.Join(file, e.Name()))
			}
		}
		slices.Sort(files)
	}
	for _, f := range files {
		fp, err := a.path(f)
		if err != nil {
			return c, err
		}
		b, err := os.ReadFile(fp)
		if err != nil {
			return c, err
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return c, errors.New(f + ": " + err.Error())
		}
		if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
			continue
		}
		c.add(f, doc.Content[0])
	}
	return c, nil
}

func (c *patroniConfig) add(file string, root *yaml.Node) {
	if v := yamlGet(root, "scope"); v != nil {
		c.scope = v.Value
	}
	if v := yamlGet(root, "name"); v != nil {
		c.name = v.Value
	}
	for _, sec := range []string{"restapi", "postgresql"} {
		if v := yamlGet(yamlGet(root, sec), "connect_address"); v != nil && v.Kind == yaml.ScalarNode {
			k := sec + ".connect_address"
			c.settings[k] = protocol.HealSetting{Key: k, Value: v.Value, File: file, Line: v.Line}
		}
	}
	if v := yamlGet(yamlGet(root, "postgresql"), "data_dir"); v != nil && v.Kind == yaml.ScalarNode {
		c.dataDir = v.Value
	}
	for _, dcs := range patroniDCS {
		sec := yamlGet(root, dcs)
		if sec == nil {
			continue
		}
		c.dcs = dcs
		c.endpoints, c.srv = nil, ""
		if dcs == "etcd3" || dcs == "etcd" {
			c.etcd(file, dcs, sec)
		}
		return
	}
}

// etcd leest de etcd-adressen zoals Patroni: srv zoekt ze via DNS; anders
// geldt één bron, proxy, dan url, dan hosts (met port als standaardpoort),
// dan host.
func (c *patroniConfig) etcd(file, dcs string, sec *yaml.Node) {
	if v := yamlGet(sec, "srv"); v != nil && v.Value != "" {
		c.srv = v.Value
		return
	}
	port := "2379"
	if v := yamlGet(sec, "port"); v != nil && v.Kind == yaml.ScalarNode && v.Value != "" {
		port = v.Value
	}
	for _, key := range []string{"proxy", "url", "hosts", "host"} {
		v := yamlGet(sec, key)
		if v == nil {
			continue
		}
		items := []*yaml.Node{v}
		if v.Kind == yaml.SequenceNode {
			items = v.Content
		}
		for _, it := range items {
			parts := []string{it.Value}
			if key == "hosts" {
				parts = strings.Split(it.Value, ",")
			}
			for _, part := range parts {
				var ep string
				if key == "proxy" || key == "url" {
					ep = etcdURL(part)
				} else {
					ep = splitHostPort(part, port)
				}
				if ep != "" {
					c.endpoints = append(c.endpoints, protocol.HealSetting{Key: dcs + "." + key, Value: ep, File: file, Line: it.Line})
				}
			}
		}
		return
	}
}

// yamlGet is de waarde van key in een mapping; nil als hij er niet staat.
// Staat hij er twee keer, dan geldt zoals bij Patroni de laatste.
func yamlGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	var v *yaml.Node
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			v = m.Content[i+1]
		}
	}
	return v
}

// splitHostPort leest host:poort zoals split_host_port van Patroni: de
// poort staat na de laatste dubbele punt, zodat ook IPv6 zonder haken
// (fd00::1:2379) kan; zonder dubbele punt geldt port. Het antwoord is
// host:poort zoals net.Dial het wil, of leeg als de poort geen getal is.
func splitHostPort(s, port string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	host, p := s, port
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		host, p = s[:i], s[i+1:]
	}
	if _, err := strconv.ParseUint(p, 10, 16); err != nil || host == "" {
		return ""
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), p)
}

// etcdURL is host:poort van een URL, met 2379 als de poort ontbreekt.
func etcdURL(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return net.JoinHostPort(u.Hostname(), cmp.Or(u.Port(), "2379"))
}

// reach probeert elk adres tegelijk, hoogstens healDialTimeout.
func (a *Agent) reach(ctx context.Context, addrs []string) []protocol.HealEndpoint {
	out := make([]protocol.HealEndpoint, len(addrs))
	var wg sync.WaitGroup
	for i, addr := range addrs {
		out[i].Address = addr
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, healDialTimeout)
			defer cancel()
			c, err := a.dial(ctx, addr)
			if err != nil {
				out[i].Error = dialError(err)
				return
			}
			_ = c.Close()
			out[i].OK = true
		})
	}
	wg.Wait()
	return out
}

func dialError(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "verbinding geweigerd; daar luistert niets"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "geen route naar het adres"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "geen antwoord binnen " + strconv.Itoa(int(healDialTimeout/time.Second)) + " s"
	}
	return err.Error()
}
