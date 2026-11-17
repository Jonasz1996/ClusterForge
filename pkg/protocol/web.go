package protocol

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Publicatie (fase 35): de agent brengt in kaart welke diensten op welke
// poorten draaien (WebOpDiscover), praat met een lokale webdienst zoals de
// API van Nginx Proxy Manager of Authentik op 127.0.0.1 (WebOpHTTP), test
// een gepubliceerd adres (WebOpCheckURL) en maakt, controleert en zet een
// kopie van NPM terug (WebOpNPM*). Alles gaat als verzoek op
// cf.node.<id>.web; de server bewaart wat er gebeurde als taak.
//
// De agent controleert de vorm. Welke paden van NPM of Authentik de GUI
// mag gebruiken, beslist de server (internal/publish).

// WebSince is de eerste protocolversie met het subject web.
const WebSince = 51

// SubjectWeb draagt de verzoeken: cf.node.<id>.web.
const SubjectWeb = "web"

const (
	TypeWebRequest = "web_request"
	TypeWebResult  = "web_result"
)

// Soorten verzoek.
const (
	// WebOpDiscover geeft de diensten van de node: luisterende poorten,
	// containers met hun gepubliceerde poorten en wat er antwoordt.
	WebOpDiscover = "discover"
	// WebOpHTTP is één HTTP-aanroep naar 127.0.0.1:<poort>.
	WebOpHTTP = "http"
	// WebOpCheckURL vraagt een adres op zoals een browser, maar verbonden
	// met een gekozen IP (zoals curl --resolve).
	WebOpCheckURL = "check_url"
	// WebOpNPMSnapshot maakt een consistente kopie van de database en de
	// nginx-configuratie van NPM op deze node.
	WebOpNPMSnapshot = "npm_snapshot"
	// WebOpNPMCheck controleert een proxy host na een wijziging: config
	// bestaat, geen .err, nginx -t, integrity_check.
	WebOpNPMCheck = "npm_check"
	// WebOpNPMRestore zet een kopie terug: NPM stoppen, database en
	// nginx-configuratie terugzetten, NPM starten.
	WebOpNPMRestore = "npm_restore"
)

const (
	// WebRequestTimeout is hoe lang een verzoek mag duren; een terugzetting
	// van NPM wacht tot NPM weer antwoordt.
	WebRequestTimeout = 3 * time.Minute
	// MaxWebResponse is het grootste antwoord van een aanroep.
	MaxWebResponse = 900 << 10
	// MaxWebBody is de grootste body van een aanroep.
	MaxWebBody = 64 << 10
	// MaxDiscoverPorts is het meeste aantal poorten dat de agent test.
	MaxDiscoverPorts = 120
)

type WebRequest struct {
	ID       string    `json:"id"`
	Deadline time.Time `json:"deadline"`
	Op       string    `json:"op"`
	HTTP     *WebHTTP  `json:"http,omitempty"`
	Check    *WebCheck `json:"check,omitempty"`
	NPM      *WebNPM   `json:"npm,omitempty"`
	// Probe: bij WebOpDiscover ook elke TCP-poort aanspreken (http, https).
	Probe bool `json:"probe,omitempty"`
}

// WebHTTP is één aanroep van een lokale webdienst.
type WebHTTP struct {
	Method string `json:"method"`
	Port   int    `json:"port"`
	// Path met query, zoals /api/nginx/proxy-hosts?expand=certificate.
	Path string `json:"path"`
	// Headers: alleen Authorization, Content-Type en Accept.
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
	// HTTPS: spreek de dienst aan met TLS (zonder het certificaat te
	// controleren; het is 127.0.0.1).
	HTTPS bool `json:"https,omitempty"`
}

// WebCheck is een controle van een gepubliceerd adres.
type WebCheck struct {
	// URL zoals https://homepage.jbogaert.be/.
	URL string `json:"url"`
	// Connect is het IP waarmee de agent verbindt in plaats van DNS, zoals
	// 127.0.0.1 op de NPM-node; leeg is gewoon DNS.
	Connect string `json:"connect,omitempty"`
}

// WebNPM zegt welke kopie of welke host.
type WebNPM struct {
	// Snapshot is de naam van een kopie (bij check en restore).
	Snapshot string `json:"snapshot,omitempty"`
	// HostID is de proxy host om te controleren; 0 controleert alleen
	// nginx en de database.
	HostID int `json:"host_id,omitempty"`
	// Domain moet in de config van de host staan.
	Domain string `json:"domain,omitempty"`
	// Absent: de host moet weg zijn (na verwijderen).
	Absent bool `json:"absent,omitempty"`
}

type WebResponse struct {
	Error string `json:"error,omitempty"`
	// Status en Body van WebOpHTTP. Body is JSON als de dienst JSON gaf,
	// anders een JSON-string.
	Status   int             `json:"status,omitempty"`
	Body     json.RawMessage `json:"body,omitempty"`
	Services []WebService    `json:"services,omitempty"`
	Check    *WebCheckResult `json:"check,omitempty"`
	NPM      *WebNPMResult   `json:"npm,omitempty"`
}

// Soorten dienst.
const (
	WebKindHTTP  = "http"
	WebKindHTTPS = "https"
	WebKindTCP   = "tcp"
	WebKindUDP   = "udp"
)

// WebService is een poort van de node met wat erachter zit.
type WebService struct {
	Proto string `json:"proto"`
	// Addr is het adres waarop geluisterd wordt (0.0.0.0, ::, een IP).
	Addr string `json:"addr"`
	Port int    `json:"port"`
	// Unit is de systemd-unit; Container en Image de Docker-container.
	Unit      string `json:"unit,omitempty"`
	Container string `json:"container,omitempty"`
	Image     string `json:"image,omitempty"`
	// Project is het compose-project van de container.
	Project string `json:"project,omitempty"`
	// Published: een gepubliceerde poort van een container (-p), ook als er
	// geen proces op luistert (zonder userland-proxy).
	Published bool `json:"published,omitempty"`
	// Kind is http, https, tcp of udp (na de probe); leeg zonder probe.
	Kind     string `json:"kind,omitempty"`
	Status   int    `json:"status,omitempty"`
	Title    string `json:"title,omitempty"`
	Server   string `json:"server,omitempty"`
	Location string `json:"location,omitempty"`
}

// WebCheckResult is de uitkomst van een WebCheck.
type WebCheckResult struct {
	Status   int    `json:"status"`
	Location string `json:"location,omitempty"`
	Server   string `json:"server,omitempty"`
	// CertOK: het certificaat is geldig voor de naam (keten en naam).
	CertOK       bool       `json:"cert_ok"`
	CertSubject  string     `json:"cert_subject,omitempty"`
	CertIssuer   string     `json:"cert_issuer,omitempty"`
	CertNotAfter *time.Time `json:"cert_not_after,omitempty"`
	CertError    string     `json:"cert_error,omitempty"`
	// Error is een fout bij het verbinden; dan is Status 0.
	Error string `json:"error,omitempty"`
}

// WebNPMResult is de uitkomst van een NPM-verzoek.
type WebNPMResult struct {
	// Snapshot is de naam van de gemaakte kopie.
	Snapshot string `json:"snapshot,omitempty"`
	// Container en DataDir zeggen welke NPM het was.
	Container string         `json:"container,omitempty"`
	DataDir   string         `json:"data_dir,omitempty"`
	Checks    []WebCheckItem `json:"checks,omitempty"`
}

// WebCheckItem is één controle.
type WebCheckItem struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// AllOK zegt of elke controle lukte.
func (r *WebNPMResult) AllOK() bool {
	if r == nil || len(r.Checks) == 0 {
		return false
	}
	for _, c := range r.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

var (
	webPath     = regexp.MustCompile(`^/[A-Za-z0-9._~!$&'()*+,;=:@%/?-]{0,1000}$`)
	webSnapshot = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[a-z0-9]{4,12}$`)
	webDomain   = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	webHeaders  = map[string]bool{"Authorization": true, "Content-Type": true, "Accept": true}
	webMethods  = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}
)

// ValidWebSnapshot is true voor de naam van een NPM-kopie.
func ValidWebSnapshot(s string) bool { return webSnapshot.MatchString(s) }

// ValidDomain is true voor een (sub)domein in kleine letters, ook *.domein.
func ValidDomain(s string) bool { return len(s) <= 253 && webDomain.MatchString(s) }

// Validate controleert de vorm van een verzoek.
func (r *WebRequest) Validate() error {
	switch r.Op {
	case WebOpDiscover:
		return nil
	case WebOpHTTP:
		h := r.HTTP
		if h == nil {
			return fmt.Errorf("geen aanroep")
		}
		if !webMethods[h.Method] {
			return fmt.Errorf("ongeldige methode %q", h.Method)
		}
		if h.Port < 1 || h.Port > 65535 {
			return fmt.Errorf("ongeldige poort %d", h.Port)
		}
		if !webPath.MatchString(h.Path) || strings.Contains(h.Path, "..") {
			return fmt.Errorf("ongeldig pad %q", h.Path)
		}
		for k, v := range h.Headers {
			if !webHeaders[k] || strings.ContainsAny(v, "\r\n") || len(v) > 4096 {
				return fmt.Errorf("ongeldige kop %q", k)
			}
		}
		if len(h.Body) > MaxWebBody {
			return fmt.Errorf("de body is te groot")
		}
		if len(h.Body) > 0 && !json.Valid(h.Body) {
			return fmt.Errorf("de body is geen JSON")
		}
		return nil
	case WebOpCheckURL:
		c := r.Check
		if c == nil {
			return fmt.Errorf("geen adres")
		}
		u, err := url.Parse(c.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || !ValidDomain(u.Hostname()) || u.User != nil {
			return fmt.Errorf("ongeldig adres %q", c.URL)
		}
		if c.Connect != "" && net.ParseIP(c.Connect) == nil {
			return fmt.Errorf("ongeldig IP %q", c.Connect)
		}
		return nil
	case WebOpNPMSnapshot:
		return nil
	case WebOpNPMCheck, WebOpNPMRestore:
		n := r.NPM
		if n == nil {
			return fmt.Errorf("geen NPM-gegevens")
		}
		if r.Op == WebOpNPMRestore && !ValidWebSnapshot(n.Snapshot) {
			return fmt.Errorf("ongeldige kopie %q", n.Snapshot)
		}
		if n.Snapshot != "" && !ValidWebSnapshot(n.Snapshot) {
			return fmt.Errorf("ongeldige kopie %q", n.Snapshot)
		}
		if n.HostID < 0 || n.HostID > 1_000_000 {
			return fmt.Errorf("ongeldige host %d", n.HostID)
		}
		if n.Domain != "" && !ValidDomain(n.Domain) {
			return fmt.Errorf("ongeldig domein %q", n.Domain)
		}
		return nil
	}
	return fmt.Errorf("onbekend verzoek %q", r.Op)
}
