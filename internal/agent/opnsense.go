package agent

// Fase 34: cf-agent op een OPNsense-firewall. Lezen gaat met configctl en
// config.xml; veranderen alleen via de lokale REST-API
// (https://127.0.0.1/api), die valideert en de diensten correct
// herlaadt. De agent maakt daarvoor zelf een API-gebruiker clusterforge
// met een sleutel aan (zie opnProvisionPHP); de sleutel blijft op de
// firewall in opnKeyFile en gaat nooit naar de server.

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	opnConfigctl   = "/usr/local/sbin/configctl"
	opnPHP         = "/usr/local/bin/php"
	opnConfigXML   = "/conf/config.xml"
	opnBackupDir   = "/conf/backup"
	opnVersionFile = "/usr/local/opnsense/version/core"
	// opnStateDir bewaart de sleutel en de herstelpunten van wijzigingen.
	opnStateDir = "/usr/local/etc/cf-agent"
	opnKeyFile  = opnStateDir + "/opnsense.key"
	// opnAPIUser is de gebruiker die de agent in OPNsense aanmaakt.
	opnAPIUser = "clusterforge"
	// opnProvisionEvery is hoe vaak de agent hoogstens zelf een sleutel
	// probeert te maken als de vorige poging mislukte.
	opnProvisionEvery = 10 * time.Minute
)

// isOPNsense zegt of dit een OPNsense-firewall is.
func (c *Collector) isOPNsense() bool {
	if !c.bsd() {
		return false
	}
	_, err := os.Stat(c.path(opnVersionFile))
	return err == nil
}

// opnVersion leest het versiebestand van OPNsense.
type opnVersion struct {
	Version  string `json:"product_version"`
	Series   string `json:"product_series"`
	Name     string `json:"product_name"`
	Nickname string `json:"product_nickname"`
}

func (c *Collector) opnVersion() opnVersion {
	var v opnVersion
	_ = json.Unmarshal([]byte(c.read(opnVersionFile)), &v)
	// product_version kan een pakketrevisie hebben: 26.1.2_1.
	v.Version, _, _ = strings.Cut(v.Version, "_")
	return v
}

// configctlJSON draait een configctl-actie en leest het JSON-antwoord.
func (c *Collector) configctlJSON(ctx context.Context, v any, action ...string) error {
	out, err := c.run(ctx, opnConfigctl, action...)
	if err != nil {
		return fmt.Errorf("configctl %s: %w", strings.Join(action, " "), err)
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), v); err != nil {
		return fmt.Errorf("configctl %s gaf geen JSON: %w", strings.Join(action, " "), err)
	}
	return nil
}

// opnIfconfig is één interface uit configctl interface list ifconfig.
type opnIfconfig struct {
	Flags  []string `json:"flags"`
	Status string   `json:"status"`
	IPv4   []struct {
		Addr string `json:"ipaddr"`
		Bits int    `json:"subnetbits"`
		VHID any    `json:"vhid"`
	} `json:"ipv4"`
	CARP map[string]struct {
		Status  string `json:"status"`
		VHID    any    `json:"vhid"`
		AdvBase any    `json:"advbase"`
		AdvSkew any    `json:"advskew"`
	} `json:"carp"`
	Pfsync *struct {
		SyncDev  string `json:"syncdev"`
		SyncPeer string `json:"syncpeer"`
		SyncOK   any    `json:"syncok"`
	} `json:"pfsync"`
}

// carpMap leest de carp-waarden; PHP geeft een object, of een lijst als
// de vhid's 0..n zijn.
func (i *opnIfconfig) UnmarshalJSON(b []byte) error {
	type plain opnIfconfig
	var raw struct {
		plain
		CARP json.RawMessage `json:"carp"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*i = opnIfconfig(raw.plain)
	i.CARP = nil
	if len(raw.CARP) == 0 || raw.CARP[0] == '[' && bytes.Equal(bytes.TrimSpace(raw.CARP), []byte("[]")) {
		return nil
	}
	if raw.CARP[0] == '[' {
		var list []struct {
			Status  string `json:"status"`
			VHID    any    `json:"vhid"`
			AdvBase any    `json:"advbase"`
			AdvSkew any    `json:"advskew"`
		}
		if err := json.Unmarshal(raw.CARP, &list); err != nil {
			return nil
		}
		i.CARP = map[string]struct {
			Status  string `json:"status"`
			VHID    any    `json:"vhid"`
			AdvBase any    `json:"advbase"`
			AdvSkew any    `json:"advskew"`
		}{}
		for _, e := range list {
			i.CARP[jsonScalar(e.VHID)] = e
		}
		return nil
	}
	return json.Unmarshal(raw.CARP, &i.CARP)
}

func atoiAny(v any) int {
	n, _ := strconv.Atoi(strings.TrimSpace(jsonScalar(v)))
	return n
}

// opnsense beschrijft de firewall; nil op een andere machine.
func (c *Collector) opnsense(ctx context.Context) *protocol.OPNsenseFacts {
	if !c.isOPNsense() {
		return nil
	}
	v := c.opnVersion()
	f := &protocol.OPNsenseFacts{Version: v.Version, Series: v.Series}
	if out, err := c.run(ctx, "freebsd-version"); err == nil {
		f.FreeBSD = strings.TrimSpace(string(out))
	}
	cfg, _ := parseXMLTree([]byte(c.read(opnConfigXML)))
	var ifc map[string]opnIfconfig
	_ = c.configctlJSON(ctx, &ifc, "interface", "list", "ifconfig")
	var carp struct {
		Demotion    any  `json:"demotion"`
		Allow       any  `json:"allow"`
		Maintenance bool `json:"maintenancemode"`
	}
	if err := c.configctlJSON(ctx, &carp, "interface", "show", "carp"); err == nil {
		f.CARP = protocol.OPNsenseCARP{Demotion: atoiAny(carp.Demotion), Allow: atoiAny(carp.Allow) == 1, Maintenance: carp.Maintenance}
	}
	fillOPNConfig(f, cfg, ifc)
	if base := c.opnAPI().base(); base != "" {
		proto, rest, _ := strings.Cut(base, "://")
		_, port, _ := net.SplitHostPort(rest)
		f.GUI = proto + ":" + port
	}
	f.Config.Backups = c.opnBackupCount()
	f.API = c.opnAPIState(ctx)
	return f
}

// fillOPNConfig haalt uit config.xml en ifconfig de hostnaam, interfaces,
// VIP's en HA-instellingen.
func fillOPNConfig(f *protocol.OPNsenseFacts, cfg *xmlNode, ifc map[string]opnIfconfig) {
	if cfg == nil {
		return
	}
	if h := cfg.text("system/hostname"); h != "" {
		f.Hostname = h
		if d := cfg.text("system/domain"); d != "" {
			f.Hostname += "." + d
		}
	}
	if t, err := strconv.ParseFloat(cfg.text("revision/time"), 64); err == nil {
		f.Config.Time = int64(t)
	}
	f.Config.Description = cfg.text("revision/description")
	f.Config.User = cfg.text("revision/username")
	if !f.CARP.Maintenance && cfg.child("virtualip_carp_maintenancemode") != nil {
		f.CARP.Maintenance = true
	}

	devices := map[string]string{}
	if ifs := cfg.child("interfaces"); ifs != nil {
		for _, n := range ifs.Children {
			dev := n.text("if")
			if dev == "" {
				continue
			}
			devices[n.Name] = dev
			oi := protocol.OPNsenseInterface{Name: n.Name, Device: dev, Descr: n.text("descr")}
			if e := n.child("enable"); e != nil && strings.TrimSpace(e.Text) != "0" {
				oi.Enabled = true
			}
			if d, ok := ifc[dev]; ok {
				oi.Up = slices.Contains(d.Flags, "up") && d.Status != "no carrier"
				for _, a := range d.IPv4 {
					if jsonScalar(a.VHID) == "" && a.Addr != "" {
						oi.Addresses = append(oi.Addresses, a.Addr+"/"+strconv.Itoa(a.Bits))
					}
				}
			}
			f.Interfaces = append(f.Interfaces, oi)
		}
	}

	for _, vip := range cfg.path("virtualip").all("vip") {
		vhid := vip.text("vhid")
		mode := vip.text("mode")
		if vhid == "" || (mode != "carp" && mode != "ipalias") {
			continue
		}
		v := protocol.OPNsenseVIP{
			Interface: vip.text("interface"), Mode: mode, Descr: vip.text("descr"),
			Address: vip.text("subnet"), State: "DISABLED",
		}
		if b := vip.text("subnet_bits"); b != "" {
			v.Address += "/" + b
		}
		v.VHID, _ = strconv.Atoi(vhid)
		v.AdvBase, _ = strconv.Atoi(vip.text("advbase"))
		v.AdvSkew, _ = strconv.Atoi(vip.text("advskew"))
		v.Device = devices[v.Interface]
		if d, ok := ifc[v.Device]; ok {
			if st, ok := d.CARP[vhid]; ok {
				v.State = strings.ToUpper(st.Status)
				v.AdvBase, v.AdvSkew = atoiAny(st.AdvBase), atoiAny(st.AdvSkew)
			}
		}
		f.VIPs = append(f.VIPs, v)
	}

	ha := cfg.child("hasync")
	f.HA = protocol.OPNsenseHA{
		PfsyncInterface: ha.text("pfsyncinterface"), PfsyncPeer: ha.text("pfsyncpeerip"),
		SyncTo: ha.text("synchronizetoip"), Username: ha.text("username"),
		DisablePreempt: ha.text("disablepreempt") == "1",
	}
	if items := ha.text("syncitems"); items != "" {
		f.HA.SyncItems = strings.Split(items, ",")
	} else if ha != nil {
		// Oudere versies: een element synchronize<onderdeel> per onderdeel.
		for _, n := range ha.Children {
			if name, ok := strings.CutPrefix(n.Name, "synchronize"); ok && name != "toip" && strings.TrimSpace(n.Text) == "on" {
				f.HA.SyncItems = append(f.HA.SyncItems, name)
			}
		}
	}
	if p, ok := ifc["pfsync0"]; ok && p.Pfsync != nil {
		f.HA.PfsyncUp = p.Pfsync.SyncDev != "" && jsonScalar(p.Pfsync.SyncOK) == "1"
	}
}

func (c *Collector) opnBackupCount() int {
	files, _ := filepath.Glob(c.path(opnBackupDir + "/config-*.xml"))
	return len(files)
}

// opnKey is de API-sleutel van de agent.
type opnKey struct {
	Key    string `json:"key"`
	Secret string `json:"secret"`
	User   string `json:"user"`
	// Pasted: de sleutel kwam uit de GUI; de agent vervangt hem niet zelf.
	Pasted bool `json:"pasted,omitempty"`
}

// opnAPI praat met de lokale API van OPNsense.
type opnAPI struct {
	mu   sync.Mutex
	c    *Collector
	key  *opnKey
	last time.Time // laatste poging om zelf een sleutel te maken
	// Client is er voor tests; nil maakt er een die 127.0.0.1 vertrouwt.
	Client *http.Client
	// Base is er voor tests; leeg leest poort en protocol uit config.xml.
	Base string
}

// opnAPI geeft de API-client van deze collector.
func (c *Collector) opnAPI() *opnAPI {
	c.opnOnce.Do(func() { c.opn = &opnAPI{c: c} })
	return c.opn
}

func (o *opnAPI) loadKey() *opnKey {
	if o.key != nil {
		return o.key
	}
	b, err := os.ReadFile(o.c.path(opnKeyFile))
	if err != nil {
		return nil
	}
	var k opnKey
	if json.Unmarshal(b, &k) != nil || k.Key == "" || k.Secret == "" {
		return nil
	}
	o.key = &k
	return o.key
}

func (o *opnAPI) saveKey(k opnKey) error {
	dir := o.c.path(opnStateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(k)
	tmp := o.c.path(opnKeyFile) + ".new"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, o.c.path(opnKeyFile)); err != nil {
		return err
	}
	o.key = &k
	return nil
}

// base is https://127.0.0.1:<poort> volgens system/webgui in config.xml.
func (o *opnAPI) base() string {
	if o.Base != "" {
		return o.Base
	}
	cfg, _ := parseXMLTree([]byte(o.c.read(opnConfigXML)))
	proto := cfg.text("system/webgui/protocol")
	if proto != "http" {
		proto = "https"
	}
	port := cfg.text("system/webgui/port")
	if _, err := strconv.Atoi(port); err != nil || port == "" {
		port = map[string]string{"http": "80", "https": "443"}[proto]
	}
	return proto + "://" + net.JoinHostPort("127.0.0.1", port)
}

func (o *opnAPI) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	// De GUI van OPNsense heeft standaard een zelfgetekend certificaat. We
	// praten alleen met 127.0.0.1 (lo0, buiten pf), dus het certificaat
	// controleren voegt niets toe.
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // alleen loopback
		Proxy:           nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil || host != "127.0.0.1" {
				return nil, errors.New("de OPNsense-API is alleen via 127.0.0.1 bereikbaar")
			}
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
		},
	}
	o.Client = &http.Client{Transport: tr, Timeout: 90 * time.Second}
	return o.Client
}

// opnHTTPError is een antwoord van de API dat geen 200 is.
type opnHTTPError struct {
	Status int
	Body   string
}

func (e *opnHTTPError) Error() string {
	switch e.Status {
	case http.StatusUnauthorized:
		return "de API-sleutel van ClusterForge wordt geweigerd (401)"
	case http.StatusForbidden:
		return "de API-gebruiker van ClusterForge mag dit niet (403)"
	}
	return fmt.Sprintf("OPNsense-API gaf %d: %s", e.Status, lastLines(strings.TrimSpace(e.Body), 2))
}

// do voert een aanroep uit met de sleutel van de agent. body nil is een
// GET; anders een POST met JSON (een lege map is een POST zonder velden).
func (o *opnAPI) do(ctx context.Context, path string, body any) (json.RawMessage, error) {
	o.mu.Lock()
	k := o.loadKey()
	o.mu.Unlock()
	if k == nil {
		return nil, errors.New("de agent heeft nog geen API-sleutel van OPNsense")
	}
	return o.doWith(ctx, k, path, body)
}

func (o *opnAPI) doWith(ctx context.Context, k *opnKey, path string, body any) (json.RawMessage, error) {
	if !protocol.ValidOPNsenseAPIPath(path) {
		return nil, fmt.Errorf("ongeldig API-pad %q", path)
	}
	method := http.MethodGet
	var rd io.Reader
	if body != nil {
		method = http.MethodPost
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, o.base()+"/api/"+path, rd)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(k.Key, k.Secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := o.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("de lokale OPNsense-API is niet bereikbaar: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, pveGetMax+1))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &opnHTTPError{Status: resp.StatusCode, Body: string(data)}
	}
	if len(data) > pveGetMax {
		return nil, fmt.Errorf("antwoord van %s is te groot", path)
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return json.RawMessage("null"), nil
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("OPNsense gaf geen JSON voor %s", path)
	}
	return data, nil
}

// opnPing is een goedkope aanroep die bewijst dat de sleutel werkt.
const opnPing = "core/firmware/running"

// opnAPIState test de sleutel en maakt er zelf een als hij ontbreekt of
// geweigerd wordt (hoogstens elke opnProvisionEvery).
func (c *Collector) opnAPIState(ctx context.Context) protocol.OPNsenseAPIState {
	o := c.opnAPI()
	o.mu.Lock()
	k := o.loadKey()
	o.mu.Unlock()
	st := protocol.OPNsenseAPIState{User: opnAPIUser}
	if k != nil {
		st.User, st.Pasted = k.User, k.Pasted
		_, err := o.doWith(ctx, k, opnPing, nil)
		if err == nil {
			st.OK = true
			return st
		}
		st.Error = err.Error()
		var he *opnHTTPError
		if k.Pasted || !errors.As(err, &he) || he.Status != http.StatusUnauthorized {
			return st
		}
	}
	nk, err := o.provision(ctx)
	if err != nil {
		if st.Error == "" {
			st.Error = err.Error()
		}
		return st
	}
	st.User, st.Pasted = nk.User, false
	if _, err := o.doWith(ctx, nk, opnPing, nil); err != nil {
		st.Error = "de nieuwe API-sleutel werkt niet: " + err.Error()
		return st
	}
	st.OK, st.Error = true, ""
	return st
}

// provision maakt de gebruiker clusterforge en een nieuwe sleutel aan met
// het PHP-script, en bewaart de sleutel.
func (o *opnAPI) provision(ctx context.Context) (*opnKey, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.last.IsZero() && time.Since(o.last) < opnProvisionEvery {
		return nil, errors.New("de API-sleutel aanmaken mislukte net; de agent probeert het straks opnieuw")
	}
	o.last = time.Now()
	if err := os.MkdirAll(o.c.path(opnStateDir), 0o700); err != nil {
		return nil, err
	}
	script := o.c.path(opnStateDir + "/provision.php")
	if err := os.WriteFile(script, []byte(opnProvisionPHP), 0o600); err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(script) }()
	out, err := o.c.run(ctx, opnPHP, script, opnAPIUser)
	var res struct {
		Key    string `json:"key"`
		Secret string `json:"secret"`
		Error  string `json:"error"`
	}
	if jerr := json.Unmarshal(bytes.TrimSpace(lastOutputLine(out)), &res); jerr != nil {
		if err == nil {
			err = jerr
		}
		return nil, fmt.Errorf("de API-gebruiker aanmaken mislukte: %v", err)
	}
	if res.Error != "" || res.Key == "" || res.Secret == "" {
		return nil, fmt.Errorf("de API-gebruiker aanmaken mislukte: %s", res.Error)
	}
	k := opnKey{Key: res.Key, Secret: res.Secret, User: opnAPIUser}
	if err := o.saveKey(k); err != nil {
		return nil, err
	}
	o.last = time.Time{}
	return &k, nil
}

// opnProvisionPHP maakt (of herstelt) de API-gebruiker van ClusterForge
// met OPNsense' eigen modelklassen, zoals System → Access → Users doet: een
// gebruiker zonder bruikbaar wachtwoord, met recht page-all, en één verse
// sleutel (oude sleutels van deze gebruiker gaan weg). Het schrijft een
// revisie in config.xml, zodat de wijziging in de geschiedenis staat.
const opnProvisionPHP = `<?php
require_once('script/load_phalcon.php');

use OPNsense\Core\Config;

$name = $argv[1] ?? 'clusterforge';
function fail($msg) { echo json_encode(['error' => $msg]) . "\n"; exit(1); }
if (!class_exists('\OPNsense\Auth\User')) {
    fail('deze OPNsense-versie heeft het gebruikersmodel nog niet; maak de sleutel in de GUI en plak hem in ClusterForge');
}
try {
    Config::getInstance()->lock();
    $mdl = new \OPNsense\Auth\User();
    $user = $mdl->getUserByName($name);
    if ($user === null) {
        $uids = [];
        foreach ($mdl->user->iterateItems() as $u) {
            $uids[] = (int)(string)$u->uid;
        }
        $uid = 2000;
        while (in_array($uid, $uids)) {
            $uid++;
        }
        $user = $mdl->user->Add();
        $user->uid->updateValue((string)$uid);
        $user->name = $name;
        $user->scope = 'user';
        $user->descr = 'ClusterForge-agent (alleen API, aangemaakt door cf-agent)';
        $hash = $mdl->generatePasswordHash(bin2hex(random_bytes(32)));
        if ($hash === false) {
            fail('wachtwoord-hash maken mislukte');
        }
        $user->password = $hash;
    }
    $user->disabled = '0';
    $user->priv = 'page-all';
    foreach ($user->apikeys->all() as $old) {
        $user->apikeys->del($old['key']);
    }
    $key = $user->apikeys->add();
    if (empty($key['key'])) {
        fail('sleutel maken mislukte');
    }
    $user->apikeys->setDescription($key['key'], 'ClusterForge-agent');
    $mdl->serializeToConfig(false, true);
    Config::getInstance()->save(['description' => 'ClusterForge: API-gebruiker ' . $name . ' voor de agent', 'username' => 'cf-agent']);
    echo json_encode(['key' => $key['key'], 'secret' => $key['secret']]) . "\n";
} catch (\Throwable $e) {
    fail($e->getMessage());
}
`
