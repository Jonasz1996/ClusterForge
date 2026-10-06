// Package proxmox praat met de REST-API van Proxmox VE en houdt een kopie van
// de hosts, VM's, containers en storage bij.
package proxmox

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// API is wat de rest van ClusterForge van Proxmox gebruikt. Tests zetten er
// een nep-Proxmox achter.
type API interface {
	Version(ctx context.Context) (Version, error)
	Resources(ctx context.Context) ([]Resource, error)
	// Power voert start, stop, shutdown of reboot uit en geeft het id van de
	// Proxmox-taak terug.
	Power(ctx context.Context, g Guest, action string) (string, error)
	Snapshot(ctx context.Context, g Guest, name, description string, vmstate bool) (string, error)
	Snapshots(ctx context.Context, g Guest) ([]Snapshot, error)
	Migrate(ctx context.Context, g Guest, target string, online bool) (string, error)
	TaskStatus(ctx context.Context, upid string) (TaskStatus, error)
	TaskLog(ctx context.Context, upid string) ([]string, error)
	StopTask(ctx context.Context, upid string) error

	// Voor nieuwe VM's uit een golden image.
	NextID(ctx context.Context) (int, error)
	// Clone maakt een volledige kopie van src met id newID op host target.
	Clone(ctx context.Context, src Guest, newID int, name, target, storage string) (string, error)
	Config(ctx context.Context, g Guest) (map[string]any, error)
	// SetConfig wijzigt de configuratie; het taak-id is leeg als Proxmox
	// het meteen deed.
	SetConfig(ctx context.Context, g Guest, form url.Values) (string, error)
	// Resize maakt een schijf groter tot size, zoals "20G".
	Resize(ctx context.Context, g Guest, disk, size string) (string, error)
	// AgentPing lukt als de QEMU guest agent in de VM antwoordt.
	AgentPing(ctx context.Context, g Guest) error
	// AgentFileWrite schrijft een bestand in de VM via de guest agent.
	AgentFileWrite(ctx context.Context, g Guest, file, content string) error

	// BackupContent geeft de back-ups op een storage, gelezen via host node.
	// Zonder de rechten VM.Backup en Datastore.AllocateSpace laat Proxmox
	// back-ups stilzwijgend weg.
	BackupContent(ctx context.Context, node, storage string) ([]BackupVolume, error)
	// NotBackedUp geeft de VM's en containers die in geen back-upjob zitten.
	NotBackedUp(ctx context.Context) ([]UncoveredGuest, error)

	// Voor de back-upcontrole. Alleen de bewaakte functies van het
	// sandbox-register in internal/backups roepen Restore, Destroy en
	// AgentInfo aan; een test controleert dat.

	// ExtractConfig leest de VM-configuratie uit een back-up, zonder iets
	// terug te zetten.
	ExtractConfig(ctx context.Context, node, volid string) (map[string]string, error)
	// Restore zet een back-up terug als nieuwe VM vmid in pool, met nieuwe
	// MAC-adressen. Nooit over een bestaande VM (geen force) en nooit
	// gestart.
	Restore(ctx context.Context, node string, vmid int, archive, storage, pool string) (string, error)
	// Destroy verwijdert een VM met purge, maar nooit met
	// destroy-unreferenced-disks: op gedeelde storage kan die een schijf van
	// een andere VM met hetzelfde VMID wissen.
	Destroy(ctx context.Context, g Guest) (string, error)
	// AgentInfo stelt de guest agent één vaste, alleen lezende vraag:
	// get-host-name, get-osinfo of get-fsinfo.
	AgentInfo(ctx context.Context, g Guest, command string) (json.RawMessage, error)
	// AgentRunVerify start via de guest agent het enige programma dat
	// ClusterForge in een VM start: cf-agent verify -, met request als
	// stdin. Het geeft het pid van het proces in de VM.
	AgentRunVerify(ctx context.Context, g Guest, request []byte) (int, error)
	// AgentExecStatus vraagt of een proces van AgentRunVerify klaar is, met
	// zijn exitcode en uitvoer.
	AgentExecStatus(ctx context.Context, g Guest, pid int) (ExecStatus, error)
}

// ExecStatus is de toestand van een proces dat via de guest agent loopt.
// Proxmox geeft out-data en err-data al gedecodeerd.
type ExecStatus struct {
	Exited       flag   `json:"exited"`
	ExitCode     int    `json:"exitcode"`
	Signal       int    `json:"signal"`
	OutData      string `json:"out-data"`
	ErrData      string `json:"err-data"`
	OutTruncated flag   `json:"out-truncated"`
}

// flag is een boolean die Proxmox als true of als 1 kan geven.
type flag bool

func (f *flag) UnmarshalJSON(b []byte) error {
	switch strings.Trim(string(b), `"`) {
	case "true", "1":
		*f = true
	default:
		*f = false
	}
	return nil
}

type Version struct {
	Version string `json:"version"`
	Release string `json:"release"`
}

// Resource is één regel uit /cluster/resources. Welke velden gevuld zijn,
// hangt af van het type.
type Resource struct {
	ID       string  `json:"id"`
	Type     string  `json:"type"`
	Node     string  `json:"node,omitempty"`
	VMID     int     `json:"vmid,omitempty"`
	Name     string  `json:"name,omitempty"`
	Status   string  `json:"status,omitempty"`
	Template int     `json:"template,omitempty"`
	Storage  string  `json:"storage,omitempty"`
	CPU      float64 `json:"cpu,omitempty"`
	MaxCPU   float64 `json:"maxcpu,omitempty"`
	Mem      int64   `json:"mem,omitempty"`
	MaxMem   int64   `json:"maxmem,omitempty"`
	Disk     int64   `json:"disk,omitempty"`
	MaxDisk  int64   `json:"maxdisk,omitempty"`
	Uptime   int64   `json:"uptime,omitempty"`
	Tags     string  `json:"tags,omitempty"`
	HAState  string  `json:"hastate,omitempty"`
	Lock     string  `json:"lock,omitempty"`
	Shared   int     `json:"shared,omitempty"`
	Content  string  `json:"content,omitempty"`
	Plugin   string  `json:"plugintype,omitempty"`
	// Pool is de resource pool van een VM of container.
	Pool string `json:"pool,omitempty"`
}

// SandboxPool is de resource pool in Proxmox waarin ClusterForge de
// tijdelijke VM's van een back-upcontrole terugzet. Alleen een VM in deze pool
// mag ClusterForge verwijderen.
const SandboxPool = "cf-sandbox"

// Guest is een VM (qemu) of container (lxc) op een host.
type Guest struct {
	Type string // qemu of lxc
	Node string
	VMID int
}

func (g Guest) path() string {
	return "/nodes/" + url.PathEscape(g.Node) + "/" + g.Type + "/" + strconv.Itoa(g.VMID)
}

type Snapshot struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parent      string `json:"parent"`
	Time        int64  `json:"snaptime"`
	VMState     int    `json:"vmstate"`
}

// BackupVolume is één back-up op een storage: een vzdump-bestand of een
// snapshot in Proxmox Backup Server.
type BackupVolume struct {
	Volid     string  `json:"volid"`
	Content   string  `json:"content"`
	VMID      int     `json:"vmid"`
	Subtype   string  `json:"subtype"`
	Ctime     int64   `json:"ctime"`
	Size      int64   `json:"size"`
	Format    string  `json:"format"`
	Notes     string  `json:"notes"`
	Protected pveBool `json:"protected"`
	// Verification is de controle van Proxmox Backup Server zelf.
	Verification *struct {
		State string `json:"state"`
	} `json:"verification"`
}

// UncoveredGuest is een VM of container die in geen back-upjob zit.
type UncoveredGuest struct {
	VMID int    `json:"vmid"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// pveBool leest een vlag die Proxmox als 0/1 of als true/false geeft.
type pveBool bool

func (b *pveBool) UnmarshalJSON(data []byte) error {
	switch strings.Trim(string(data), `"`) {
	case "1", "true":
		*b = true
	default:
		*b = false
	}
	return nil
}

type TaskStatus struct {
	Status     string `json:"status"`
	ExitStatus string `json:"exitstatus"`
}

// Running is true zolang de taak loopt.
func (t TaskStatus) Running() bool { return t.Status == "running" }

// OK is true als de taak geslaagd is; waarschuwingen tellen als geslaagd.
func (t TaskStatus) OK() bool {
	return t.ExitStatus == "OK" || strings.HasPrefix(t.ExitStatus, "WARNINGS")
}

// Error is een fout die Proxmox zelf teruggeeft.
type Error struct {
	Code    int
	Message string
}

func (e *Error) Error() string {
	if e.Code == http.StatusUnauthorized {
		return "Proxmox weigert het API-token (401)"
	}
	if e.Code == http.StatusForbidden {
		return "het API-token mag dit niet in Proxmox (403): " + e.Message
	}
	return fmt.Sprintf("Proxmox: %s (%d)", e.Message, e.Code)
}

// Client is een dunne HTTP-client voor de Proxmox-API met een API-token.
type Client struct {
	base  string
	auth  string
	httpc *http.Client
}

// Config beschrijft een verbinding.
type Config struct {
	URL         string
	TokenID     string
	TokenSecret string
	// Fingerprint is de SHA-256 van het certificaat; leeg gebruikt de gewone
	// controle tegen de CA's van het systeem.
	Fingerprint string
	Timeout     time.Duration
}

func NewClient(c Config) (*Client, error) {
	base, err := NormalizeURL(c.URL)
	if err != nil {
		return nil, err
	}
	tlsConf := &tls.Config{MinVersion: tls.VersionTLS12}
	if fp := NormalizeFingerprint(c.Fingerprint); fp != "" {
		// Self-signed is de norm in Proxmox; we vertrouwen precies dit
		// certificaat in plaats van een CA.
		tlsConf.InsecureSkipVerify = true //nolint:gosec // vervangen door VerifyConnection
		tlsConf.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("geen certificaat ontvangen van Proxmox")
			}
			if got := Fingerprint(cs.PeerCertificates[0]); got != fp {
				return &FingerprintError{Got: got}
			}
			return nil
		}
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	// Bewust geen proxy uit de omgeving: Proxmox staat in je eigen netwerk.
	tr := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSClientConfig:     tlsConf,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Client{
		base:  base + "/api2/json",
		auth:  "PVEAPIToken=" + c.TokenID + "=" + c.TokenSecret,
		httpc: &http.Client{Transport: tr, Timeout: timeout},
	}, nil
}

// CloseIdle sluit open verbindingen, als de client niet meer gebruikt wordt.
func (c *Client) CloseIdle() { c.httpc.CloseIdleConnections() }

// FingerprintError betekent dat het certificaat van Proxmox niet de
// verwachte vingerafdruk heeft.
type FingerprintError struct{ Got string }

func (e *FingerprintError) Error() string {
	return "het certificaat van Proxmox heeft een andere vingerafdruk: " + FormatFingerprint(e.Got)
}

// NormalizeURL maakt van "pve1.lan" of "https://pve1.lan:8006/api2/json/"
// het basisadres https://pve1.lan:8006. Zonder schema en poort gaat het om
// de standaardpoort 8006.
func NormalizeURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("API-adres ontbreekt")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
		if u, err := url.Parse(s); err == nil && u.Port() == "" {
			u.Host = net.JoinHostPort(u.Hostname(), "8006")
			s = u.String()
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", errors.New("ongeldig API-adres")
	}
	if u.Scheme != "https" {
		return "", errors.New("het API-adres moet met https:// beginnen")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("ongeldig API-adres")
	}
	p := strings.TrimRight(u.Path, "/")
	p = strings.TrimSuffix(p, "/api2/json")
	if p != "" {
		return "", errors.New("geef alleen het adres van Proxmox, zoals https://pve1.lan:8006")
	}
	return "https://" + u.Host, nil
}

// Fingerprint geeft de SHA-256 van een certificaat als kleine hex.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// NormalizeFingerprint aanvaardt de vorm die Proxmox toont (AA:BB:...) en
// geeft kleine hex zonder dubbele punten; ongeldig geeft "".
func NormalizeFingerprint(s string) string {
	s = strings.ToLower(strings.NewReplacer(":", "", " ", "").Replace(strings.TrimSpace(s)))
	if len(s) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(s); err != nil {
		return ""
	}
	return s
}

// FormatFingerprint toont een vingerafdruk zoals Proxmox: AA:BB:CC:...
func FormatFingerprint(fp string) string {
	fp = strings.ToUpper(fp)
	parts := make([]string, 0, len(fp)/2)
	for i := 0; i+1 < len(fp); i += 2 {
		parts = append(parts, fp[i:i+2])
	}
	return strings.Join(parts, ":")
}

// Probe haalt het certificaat van een Proxmox-adres op zonder het te
// vertrouwen, zodat de gebruiker de vingerafdruk kan controleren.
type ProbeResult struct {
	Fingerprint string
	Subject     string
	Issuer      string
	NotAfter    time.Time
	// Trusted is true als het certificaat ook zonder vingerafdruk geldig is.
	Trusted bool
}

func Probe(ctx context.Context, rawURL string) (ProbeResult, error) {
	base, err := NormalizeURL(rawURL)
	if err != nil {
		return ProbeResult{}, err
	}
	u, _ := url.Parse(base)
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	d := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 10 * time.Second},
		Config:    &tls.Config{InsecureSkipVerify: true, ServerName: u.Hostname()}, //nolint:gosec // alleen om het certificaat te tonen
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("geen verbinding met %s: %w", host, err)
	}
	defer func() { _ = conn.Close() }()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return ProbeResult{}, errors.New("geen certificaat ontvangen van Proxmox")
	}
	leaf := certs[0]
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	_, verr := leaf.Verify(x509.VerifyOptions{DNSName: u.Hostname(), Intermediates: inter})
	return ProbeResult{
		Fingerprint: Fingerprint(leaf), Subject: leaf.Subject.String(), Issuer: leaf.Issuer.String(),
		NotAfter: leaf.NotAfter, Trusted: verr == nil,
	}, nil
}

// --- API-aanroepen ---

func (c *Client) Version(ctx context.Context) (Version, error) {
	var v Version
	return v, c.do(ctx, http.MethodGet, "/version", nil, &v)
}

func (c *Client) Resources(ctx context.Context) ([]Resource, error) {
	var out []Resource
	return out, c.do(ctx, http.MethodGet, "/cluster/resources", nil, &out)
}

// powerActions zijn de toegestane statusacties.
var powerActions = map[string]bool{"start": true, "stop": true, "shutdown": true, "reboot": true}

func (c *Client) Power(ctx context.Context, g Guest, action string) (string, error) {
	if !powerActions[action] {
		return "", fmt.Errorf("onbekende actie %q", action)
	}
	form := url.Values{}
	if action == "shutdown" {
		// Netjes afsluiten krijgt drie minuten; daarna faalt de taak in
		// plaats van de VM hard uit te zetten.
		form.Set("timeout", "180")
	}
	var upid string
	return upid, c.do(ctx, http.MethodPost, g.path()+"/status/"+action, form, &upid)
}

func (c *Client) Snapshot(ctx context.Context, g Guest, name, description string, vmstate bool) (string, error) {
	form := url.Values{"snapname": {name}}
	if description != "" {
		form.Set("description", description)
	}
	if vmstate && g.Type == "qemu" {
		form.Set("vmstate", "1")
	}
	var upid string
	return upid, c.do(ctx, http.MethodPost, g.path()+"/snapshot", form, &upid)
}

func (c *Client) Snapshots(ctx context.Context, g Guest) ([]Snapshot, error) {
	var out []Snapshot
	if err := c.do(ctx, http.MethodGet, g.path()+"/snapshot", nil, &out); err != nil {
		return nil, err
	}
	// "current" is geen snapshot maar de huidige toestand.
	snaps := out[:0]
	for _, s := range out {
		if s.Name != "current" {
			snaps = append(snaps, s)
		}
	}
	return snaps, nil
}

func (c *Client) Migrate(ctx context.Context, g Guest, target string, online bool) (string, error) {
	form := url.Values{"target": {target}}
	if online {
		if g.Type == "qemu" {
			// Live migratie; lokale schijven gaan mee naar de nieuwe host.
			form.Set("online", "1")
			form.Set("with-local-disks", "1")
		} else {
			// Containers kunnen niet live; ze herstarten op de nieuwe host.
			form.Set("restart", "1")
		}
	}
	var upid string
	return upid, c.do(ctx, http.MethodPost, g.path()+"/migrate", form, &upid)
}

func (c *Client) TaskStatus(ctx context.Context, upid string) (TaskStatus, error) {
	node, err := upidNode(upid)
	if err != nil {
		return TaskStatus{}, err
	}
	var st TaskStatus
	return st, c.do(ctx, http.MethodGet, "/nodes/"+url.PathEscape(node)+"/tasks/"+url.PathEscape(upid)+"/status", nil, &st)
}

// TaskLog geeft de laatste regels van de uitvoer van een taak.
func (c *Client) TaskLog(ctx context.Context, upid string) ([]string, error) {
	node, err := upidNode(upid)
	if err != nil {
		return nil, err
	}
	var lines []struct {
		N int    `json:"n"`
		T string `json:"t"`
	}
	q := url.Values{"start": {"0"}, "limit": {"1000"}}
	if err := c.do(ctx, http.MethodGet, "/nodes/"+url.PathEscape(node)+"/tasks/"+url.PathEscape(upid)+"/log?"+q.Encode(), nil, &lines); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		// Proxmox sluit af met "no content" als er geen uitvoer is.
		if l.T != "" && l.T != "no content" {
			out = append(out, l.T)
		}
	}
	return out, nil
}

func (c *Client) StopTask(ctx context.Context, upid string) error {
	node, err := upidNode(upid)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, "/nodes/"+url.PathEscape(node)+"/tasks/"+url.PathEscape(upid), nil, nil)
}

func (c *Client) NextID(ctx context.Context) (int, error) {
	// Proxmox geeft het id als tekst of als getal, afhankelijk van de versie.
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/cluster/nextid", nil, &raw); err != nil {
		return 0, err
	}
	id, err := strconv.Atoi(strings.Trim(string(raw), `"`))
	if err != nil {
		return 0, fmt.Errorf("onverwacht vmid van Proxmox: %s", raw)
	}
	return id, nil
}

func (c *Client) Clone(ctx context.Context, src Guest, newID int, name, target, storage string) (string, error) {
	form := url.Values{"newid": {strconv.Itoa(newID)}, "name": {name}, "full": {"1"}}
	if target != "" && target != src.Node {
		form.Set("target", target)
	}
	if storage != "" {
		form.Set("storage", storage)
	}
	var upid string
	return upid, c.do(ctx, http.MethodPost, src.path()+"/clone", form, &upid)
}

func (c *Client) Config(ctx context.Context, g Guest) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, http.MethodGet, g.path()+"/config", nil, &out)
}

func (c *Client) SetConfig(ctx context.Context, g Guest, form url.Values) (string, error) {
	var upid string
	return upid, c.do(ctx, http.MethodPost, g.path()+"/config", form, &upid)
}

func (c *Client) Resize(ctx context.Context, g Guest, disk, size string) (string, error) {
	var upid string
	return upid, c.do(ctx, http.MethodPut, g.path()+"/resize", url.Values{"disk": {disk}, "size": {size}}, &upid)
}

func (c *Client) AgentPing(ctx context.Context, g Guest) error {
	return c.do(ctx, http.MethodPost, g.path()+"/agent/ping", url.Values{}, nil)
}

func (c *Client) AgentFileWrite(ctx context.Context, g Guest, file, content string) error {
	return c.do(ctx, http.MethodPost, g.path()+"/agent/file-write", url.Values{"file": {file}, "content": {content}}, nil)
}

func (c *Client) BackupContent(ctx context.Context, node, storage string) ([]BackupVolume, error) {
	var out []BackupVolume
	path := "/nodes/" + url.PathEscape(node) + "/storage/" + url.PathEscape(storage) + "/content?content=backup"
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	// Alleen back-ups van een VM of container, ook als het filter op
	// content genegeerd wordt.
	vols := out[:0]
	for _, v := range out {
		if v.Content == "backup" && v.VMID > 0 {
			vols = append(vols, v)
		}
	}
	return vols, nil
}

func (c *Client) NotBackedUp(ctx context.Context) ([]UncoveredGuest, error) {
	var out []UncoveredGuest
	return out, c.do(ctx, http.MethodGet, "/cluster/backup-info/not-backed-up", nil, &out)
}

func (c *Client) ExtractConfig(ctx context.Context, node, volid string) (map[string]string, error) {
	var raw string
	q := url.Values{"volume": {volid}}
	if err := c.do(ctx, http.MethodGet, "/nodes/"+url.PathEscape(node)+"/vzdump/extractconfig?"+q.Encode(), nil, &raw); err != nil {
		return nil, err
	}
	return ParseConfig(raw), nil
}

// ParseConfig leest een VM-configuratie in de tekstvorm van Proxmox. De
// beschrijving (regels met #) en snapshots ([naam]) tellen niet mee.
func ParseConfig(raw string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			break
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

func (c *Client) Restore(ctx context.Context, node string, vmid int, archive, storage, pool string) (string, error) {
	form := url.Values{
		"vmid": {strconv.Itoa(vmid)}, "archive": {archive}, "storage": {storage}, "pool": {pool}, "unique": {"1"},
	}
	var upid string
	return upid, c.do(ctx, http.MethodPost, "/nodes/"+url.PathEscape(node)+"/qemu", form, &upid)
}

func (c *Client) Destroy(ctx context.Context, g Guest) (string, error) {
	if g.Type != "qemu" {
		return "", fmt.Errorf("alleen een VM kan als sandbox verwijderd worden, geen %s", g.Type)
	}
	var upid string
	return upid, c.do(ctx, http.MethodDelete, g.path()+"?purge=1", nil, &upid)
}

// agentInfo zijn de vragen die AgentInfo aan de guest agent mag stellen.
var agentInfo = map[string]bool{"get-host-name": true, "get-osinfo": true, "get-fsinfo": true}

func (c *Client) AgentInfo(ctx context.Context, g Guest, command string) (json.RawMessage, error) {
	if !agentInfo[command] {
		return nil, fmt.Errorf("onbekende vraag aan de guest agent: %q", command)
	}
	var out struct {
		Result json.RawMessage `json:"result"`
	}
	if err := c.do(ctx, http.MethodGet, g.path()+"/agent/"+command, nil, &out); err != nil {
		return nil, err
	}
	return out.Result, nil
}

// verifyArgv is het enige commando dat AgentRunVerify start; het staat hier
// vast en komt nooit uit een aanvraag.
var verifyArgv = []string{protocol.VerifyPath, "verify", "-"}

// maxVerifyInput is wat Proxmox als input-data aanneemt.
const maxVerifyInput = 64 << 10

func (c *Client) AgentRunVerify(ctx context.Context, g Guest, request []byte) (int, error) {
	if g.Type != "qemu" {
		return 0, fmt.Errorf("alleen een VM heeft een guest agent, geen %s", g.Type)
	}
	if len(request) > maxVerifyInput {
		return 0, fmt.Errorf("de aanvraag voor cf-agent verify is groter dan %d bytes", maxVerifyInput)
	}
	form := url.Values{"command": slices.Clone(verifyArgv), "input-data": {string(request)}}
	var out struct {
		PID int `json:"pid"`
	}
	if err := c.do(ctx, http.MethodPost, g.path()+"/agent/exec", form, &out); err != nil {
		return 0, err
	}
	return out.PID, nil
}

func (c *Client) AgentExecStatus(ctx context.Context, g Guest, pid int) (ExecStatus, error) {
	var out ExecStatus
	q := url.Values{"pid": {strconv.Itoa(pid)}}
	return out, c.do(ctx, http.MethodGet, g.path()+"/agent/exec-status?"+q.Encode(), nil, &out)
}

// upidNode haalt de host uit een taak-id: UPID:pve1:0000ABCD:...
func upidNode(upid string) (string, error) {
	parts := strings.Split(upid, ":")
	if len(parts) < 3 || parts[0] != "UPID" || parts[1] == "" {
		return "", fmt.Errorf("ongeldig taak-id %q", upid)
	}
	return parts[1], nil
}

func (c *Client) do(ctx context.Context, method, path string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.auth)
	req.Header.Set("Accept", "application/json")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		var fe *FingerprintError
		if errors.As(err, &fe) {
			return fe
		}
		return fmt.Errorf("geen verbinding met Proxmox: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return apiError(resp, data)
	}
	if out == nil {
		return nil
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("onverwacht antwoord van Proxmox: %w", err)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("onverwacht antwoord van Proxmox: %w", err)
	}
	return nil
}

// apiError leest de fout uit een Proxmox-antwoord. Proxmox zet de reden in
// de statusregel en details per parameter in "errors".
func apiError(resp *http.Response, data []byte) error {
	msg := strings.TrimSpace(strings.TrimPrefix(resp.Status, strconv.Itoa(resp.StatusCode)))
	var env struct {
		Errors  map[string]string `json:"errors"`
		Message string            `json:"message"`
	}
	if json.Unmarshal(data, &env) == nil {
		if env.Message != "" {
			msg = strings.TrimSpace(env.Message)
		}
		for k, v := range env.Errors {
			msg += "; " + k + ": " + strings.TrimSpace(v)
		}
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return &Error{Code: resp.StatusCode, Message: msg}
}
