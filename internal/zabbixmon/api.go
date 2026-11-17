// Package zabbixmon leest de Zabbix-API uit voor de dashboards van fase 33:
// problemen, hosts met beschikbaarheid, proxy's, de HA-nodes van de
// Zabbix-server en zijn gezondheidsitems. Alleen lezen; de browser stuurt
// nooit zelf iets naar Zabbix.
package zabbixmon

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// API is wat de verzamelaar van de Zabbix-client nodig heeft.
type API interface {
	Call(ctx context.Context, method string, params, out any) error
}

// zint leest een getal dat Zabbix als tekst ("3") of als getal geeft.
type zint int64

func (z *zint) UnmarshalJSON(b []byte) error {
	b = bytes.Trim(b, `"`)
	if len(b) == 0 || string(b) == "null" {
		*z = 0
		return nil
	}
	v, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return fmt.Errorf("geen geheel getal: %s", b)
	}
	*z = zint(v)
	return nil
}

func (z zint) time() *time.Time {
	if z <= 0 {
		return nil
	}
	t := time.Unix(int64(z), 0).UTC()
	return &t
}

type rawInterface struct {
	InterfaceID string `json:"interfaceid"`
	IP          string `json:"ip"`
	DNS         string `json:"dns"`
	Type        zint   `json:"type"`
	Main        zint   `json:"main"`
	Available   zint   `json:"available"`
	Error       string `json:"error"`
}

type rawHost struct {
	HostID            string         `json:"hostid"`
	Host              string         `json:"host"`
	Name              string         `json:"name"`
	Status            zint           `json:"status"`
	MaintenanceStatus zint           `json:"maintenance_status"`
	ActiveAvailable   zint           `json:"active_available"`
	MonitoredBy       zint           `json:"monitored_by"`
	ProxyID           string         `json:"proxyid"`
	Interfaces        []rawInterface `json:"interfaces"`
	HostGroups        []struct {
		Name string `json:"name"`
	} `json:"hostgroups"`
}

func (c *collector) hosts(ctx context.Context) ([]rawHost, error) {
	var hs []rawHost
	err := c.api.Call(ctx, "host.get", map[string]any{
		"output":           []string{"hostid", "host", "name", "status", "maintenance_status", "active_available", "monitored_by", "proxyid"},
		"selectInterfaces": []string{"interfaceid", "ip", "dns", "type", "main", "available", "error"},
		"selectHostGroups": []string{"name"},
		"sortfield":        "name",
		"limit":            5000,
	}, &hs)
	return hs, err
}

type rawTag struct {
	Tag   string `json:"tag"`
	Value string `json:"value"`
}

type rawProblem struct {
	EventID      string   `json:"eventid"`
	ObjectID     string   `json:"objectid"`
	Clock        zint     `json:"clock"`
	Name         string   `json:"name"`
	Severity     zint     `json:"severity"`
	Acknowledged zint     `json:"acknowledged"`
	Suppressed   zint     `json:"suppressed"`
	Opdata       string   `json:"opdata"`
	Tags         []rawTag `json:"tags"`
}

// maxProblems is hoeveel open problemen we hoogstens ophalen.
const maxProblems = 2000

func (c *collector) problems(ctx context.Context) ([]rawProblem, error) {
	var ps []rawProblem
	err := c.api.Call(ctx, "problem.get", map[string]any{
		"output":     []string{"eventid", "objectid", "clock", "name", "severity", "acknowledged", "suppressed", "opdata"},
		"selectTags": []string{"tag", "value"},
		"source":     0,
		"object":     0,
		"recent":     false,
		"sortfield":  []string{"eventid"},
		"sortorder":  "DESC",
		"limit":      maxProblems,
	}, &ps)
	return ps, err
}

// triggerHosts geeft per trigger de hosts.
func (c *collector) triggerHosts(ctx context.Context, ids []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(ids) == 0 {
		return out, nil
	}
	var ts []struct {
		TriggerID string `json:"triggerid"`
		Hosts     []struct {
			HostID string `json:"hostid"`
		} `json:"hosts"`
	}
	if err := c.api.Call(ctx, "trigger.get", map[string]any{
		"triggerids":  ids,
		"output":      []string{"triggerid"},
		"selectHosts": []string{"hostid"},
	}, &ts); err != nil {
		return nil, err
	}
	for _, t := range ts {
		for _, h := range t.Hosts {
			out[t.TriggerID] = append(out[t.TriggerID], h.HostID)
		}
	}
	return out, nil
}

type rawProxy struct {
	ProxyID       string `json:"proxyid"`
	Name          string `json:"name"`
	OperatingMode zint   `json:"operating_mode"`
	LastAccess    zint   `json:"lastaccess"`
	Version       zint   `json:"version"`
	Compatibility zint   `json:"compatibility"`
	State         zint   `json:"state"`
}

func (c *collector) proxies(ctx context.Context) ([]rawProxy, error) {
	var ps []rawProxy
	err := c.api.Call(ctx, "proxy.get", map[string]any{
		"output":    []string{"proxyid", "name", "operating_mode", "lastaccess", "version", "compatibility", "state"},
		"sortfield": "name",
	}, &ps)
	return ps, err
}

type rawHANode struct {
	Name       string `json:"name"`
	Address    string `json:"address"`
	Port       string `json:"port"`
	LastAccess zint   `json:"lastaccess"`
	Status     zint   `json:"status"`
}

func (c *collector) haNodes(ctx context.Context) ([]rawHANode, error) {
	var ns []rawHANode
	err := c.api.Call(ctx, "hanode.get", map[string]any{
		"output":    []string{"name", "address", "port", "lastaccess", "status"},
		"sortfield": "name",
	}, &ns)
	return ns, err
}

type rawItem struct {
	ItemID    string `json:"itemid"`
	HostID    string `json:"hostid"`
	Name      string `json:"name"`
	Key       string `json:"key_"`
	LastValue string `json:"lastvalue"`
	LastClock zint   `json:"lastclock"`
	Units     string `json:"units"`
	State     zint   `json:"state"`
}

// healthItems geeft de interne items van Zabbix zelf (zabbix[...]), van de
// templates Zabbix server health en Zabbix proxy health.
func (c *collector) healthItems(ctx context.Context) ([]rawItem, error) {
	var is []rawItem
	err := c.api.Call(ctx, "item.get", map[string]any{
		"output":      []string{"itemid", "hostid", "name", "key_", "lastvalue", "lastclock", "units", "state"},
		"search":      map[string]any{"key_": "zabbix["},
		"startSearch": true,
		"monitored":   true,
		"templated":   false,
		"limit":       5000,
	}, &is)
	if err != nil {
		return nil, err
	}
	out := is[:0]
	for _, it := range is {
		if healthKey(it.Key) != "" {
			out = append(out, it)
		}
	}
	return out, nil
}

// healthKey zet een sleutel om naar het veld van Health; leeg = niet nodig.
func healthKey(key string) string {
	k := strings.ReplaceAll(key, " ", "")
	switch k {
	case "zabbix[requiredperformance]":
		return "nvps"
	case "zabbix[queue]":
		return "queue"
	case "zabbix[queue,10m]":
		return "queue_10m"
	case "zabbix[preprocessing_queue]":
		return "preprocessing_queue"
	case "zabbix[rcache,buffer,pused]":
		return "cache_config"
	case "zabbix[wcache,history,pused]":
		return "cache_history"
	case "zabbix[vcache,buffer,pused]":
		return "cache_value"
	case "zabbix[wcache,values]":
		return "values"
	case "zabbix[uptime]":
		return "uptime"
	}
	if strings.HasPrefix(k, "zabbix[process,") && strings.HasSuffix(k, ",avg,busy]") {
		return "process"
	}
	return ""
}

// version zet 80002 om in 8.0.2.
func version(v zint) string {
	if v <= 0 {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", v/10000, (v/100)%100, v%100)
}

// apiVersion leest apiinfo.version (zonder token).
func (c *collector) apiVersion(ctx context.Context) (string, error) {
	var v string
	err := c.api.Call(ctx, "apiinfo.version", map[string]any{}, &v)
	return v, err
}
