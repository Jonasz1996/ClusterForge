package zabbixagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Client praat met de JSON-RPC-API van Zabbix (api_jsonrpc.php) met een
// API-token (Zabbix 6.4 en nieuwer: Authorization: Bearer).
type Client struct {
	URL   string
	Token string
	HTTP  *http.Client
}

// APIError is een fout die Zabbix zelf teruggaf.
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

func (e *APIError) Error() string {
	if e.Data != "" {
		return "Zabbix API: " + e.Message + " " + e.Data
	}
	return "Zabbix API: " + e.Message
}

func (c *Client) call(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params, "id": 1})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json-rpc")
	// apiinfo.version weigert een token.
	if method != "apiinfo.version" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("de Zabbix API is niet bereikbaar: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("de Zabbix API antwoordt %s (klopt het adres van api_jsonrpc.php?)", resp.Status)
	}
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *APIError       `json:"error"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return errors.New("geen JSON-RPC-antwoord van Zabbix (klopt het adres van api_jsonrpc.php?)")
	}
	if r.Error != nil {
		return r.Error
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(r.Result, out)
}

// Version is de versie van Zabbix.
func (c *Client) Version(ctx context.Context) (string, error) {
	var v string
	err := c.call(ctx, "apiinfo.version", map[string]any{}, &v)
	return v, err
}

// Check test de versie en het token.
func (c *Client) Check(ctx context.Context) (string, error) {
	v, err := c.Version(ctx)
	if err != nil {
		return "", err
	}
	var hosts []map[string]any
	if err := c.call(ctx, "host.get", map[string]any{"output": []string{"hostid"}, "limit": 1}, &hosts); err != nil {
		return v, err
	}
	return v, nil
}

// HostID zoekt een host op zijn technische naam; leeg als hij er niet is.
func (c *Client) HostID(ctx context.Context, name string) (string, error) {
	var hosts []struct {
		HostID string `json:"hostid"`
	}
	if err := c.call(ctx, "host.get", map[string]any{"output": []string{"hostid"}, "filter": map[string]any{"host": []string{name}}}, &hosts); err != nil {
		return "", err
	}
	if len(hosts) == 0 {
		return "", nil
	}
	return hosts[0].HostID, nil
}

// SetPSK zet de PSK van een host: verbindingen van en naar de agent alleen
// nog met PSK.
func (c *Client) SetPSK(ctx context.Context, hostID, identity, psk string) error {
	return c.call(ctx, "host.update", map[string]any{
		"hostid": hostID, "tls_connect": 2, "tls_accept": 2, "tls_psk_identity": identity, "tls_psk": psk,
	}, nil)
}

// NewHost is een host die ClusterForge in Zabbix aanmaakt.
type NewHost struct {
	Name      string
	IP        string
	Group     string
	Templates []string
	Proxy     string
	Identity  string
	PSK       string
}

// CreateHost maakt de host aan met een agent-interface en PSK; de groep
// komt er bij als hij nog niet bestaat.
func (c *Client) CreateHost(ctx context.Context, h NewHost) (string, error) {
	gid, err := c.groupID(ctx, h.Group)
	if err != nil {
		return "", err
	}
	params := map[string]any{
		"host":   h.Name,
		"groups": []map[string]string{{"groupid": gid}},
		"interfaces": []map[string]any{{
			"type": 1, "main": 1, "useip": 1, "ip": h.IP, "dns": "", "port": "10050",
		}},
		"tls_connect": 2, "tls_accept": 2, "tls_psk_identity": h.Identity, "tls_psk": h.PSK,
	}
	if len(h.Templates) > 0 {
		ids, err := c.templateIDs(ctx, h.Templates)
		if err != nil {
			return "", err
		}
		var ts []map[string]string
		for _, id := range ids {
			ts = append(ts, map[string]string{"templateid": id})
		}
		params["templates"] = ts
	}
	if h.Proxy != "" {
		pid, err := c.proxyID(ctx, h.Proxy)
		if err != nil {
			return "", err
		}
		params["monitored_by"], params["proxyid"] = 1, pid
	}
	var r struct {
		HostIDs []string `json:"hostids"`
	}
	if err := c.call(ctx, "host.create", params, &r); err != nil {
		return "", err
	}
	if len(r.HostIDs) == 0 {
		return "", errors.New("geen hostid in het antwoord van Zabbix")
	}
	return r.HostIDs[0], nil
}

func (c *Client) groupID(ctx context.Context, name string) (string, error) {
	var gs []struct {
		GroupID string `json:"groupid"`
	}
	if err := c.call(ctx, "hostgroup.get", map[string]any{"output": []string{"groupid"}, "filter": map[string]any{"name": []string{name}}}, &gs); err != nil {
		return "", err
	}
	if len(gs) > 0 {
		return gs[0].GroupID, nil
	}
	var r struct {
		GroupIDs []string `json:"groupids"`
	}
	if err := c.call(ctx, "hostgroup.create", map[string]any{"name": name}, &r); err != nil {
		return "", err
	}
	if len(r.GroupIDs) == 0 {
		return "", errors.New("geen groupid in het antwoord van Zabbix")
	}
	return r.GroupIDs[0], nil
}

type zbxTemplate struct {
	TemplateID string `json:"templateid"`
	Host       string `json:"host"`
	Name       string `json:"name"`
}

func (c *Client) templateIDs(ctx context.Context, names []string) ([]string, error) {
	var ts []zbxTemplate
	if err := c.call(ctx, "template.get", map[string]any{"output": []string{"templateid", "host", "name"}}, &ts); err != nil {
		return nil, err
	}
	var ids, missing []string
	for _, n := range names {
		i := slices.IndexFunc(ts, func(t zbxTemplate) bool {
			return strings.EqualFold(t.Host, n) || strings.EqualFold(t.Name, n)
		})
		if i < 0 {
			missing = append(missing, n)
			continue
		}
		ids = append(ids, ts[i].TemplateID)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("template niet gevonden in Zabbix: %s", strings.Join(missing, ", "))
	}
	return ids, nil
}

func (c *Client) proxyID(ctx context.Context, name string) (string, error) {
	var ps []struct {
		ProxyID string `json:"proxyid"`
	}
	if err := c.call(ctx, "proxy.get", map[string]any{"output": []string{"proxyid"}, "filter": map[string]any{"name": []string{name}}}, &ps); err != nil {
		return "", err
	}
	if len(ps) == 0 {
		return "", fmt.Errorf("proxy %q niet gevonden in Zabbix", name)
	}
	return ps[0].ProxyID, nil
}
