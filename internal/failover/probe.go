package failover

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Probe is hoe de server een VIP opvraagt: een HTTP-pad met de verwachte
// status, of een TCP-poort. Er staat nooit een host in; de host is altijd
// het VIP.
type Probe struct {
	HTTP *HTTPProbe `json:"http,omitempty"`
	TCP  *TCPProbe  `json:"tcp,omitempty"`
}

type HTTPProbe struct {
	Path   string `json:"path"`
	Expect int    `json:"expect"`
}

type TCPProbe struct {
	Port int `json:"port"`
}

// DefaultProbe is wat een nieuwe test krijgt: de http-check van de
// template, of anders de startpagina.
var DefaultProbe = Probe{HTTP: &HTTPProbe{Path: "/", Expect: http.StatusOK}}

// Validate controleert de probe.
func (p Probe) Validate() error {
	switch {
	case (p.HTTP == nil) == (p.TCP == nil):
		return &FieldError{Field: "probe", Message: "kies een HTTP-pad of een TCP-poort"}
	case p.TCP != nil && (p.TCP.Port < 1 || p.TCP.Port > 65535):
		return &FieldError{Field: "probe", Message: "de poort ligt tussen 1 en 65535"}
	case p.HTTP != nil && (p.HTTP.Expect < 100 || p.HTTP.Expect > 599):
		return &FieldError{Field: "probe", Message: "de verwachte status ligt tussen 100 en 599"}
	case p.HTTP != nil && !validPath(p.HTTP.Path):
		return &FieldError{Field: "probe", Message: "het pad begint met / en heeft geen spaties, ? of #, zoals /health"}
	}
	return nil
}

func validPath(p string) bool {
	return strings.HasPrefix(p, "/") && len(p) <= 200 &&
		!strings.ContainsFunc(p, func(r rune) bool { return r <= ' ' || r == 0x7f || r == '?' || r == '#' || r == '\\' })
}

// String beschrijft de probe voor mensen.
func (p Probe) String() string {
	if p.TCP != nil {
		return fmt.Sprintf("TCP-poort %d", p.TCP.Port)
	}
	if p.HTTP != nil {
		return fmt.Sprintf("HTTP %s geeft %d", p.HTTP.Path, p.HTTP.Expect)
	}
	return ""
}

// Prober vraagt een VIP één keer op; nil betekent bereikbaar.
type Prober interface {
	Probe(ctx context.Context, vip string, p Probe) error
}

// NetProber vraagt het VIP echt op, rechtstreeks: zonder proxy, zonder
// redirects te volgen en zonder keep-alive, zodat elke poging een nieuwe
// verbinding is en de server geen ander adres kan opvragen dan het VIP.
type NetProber struct {
	Timeout time.Duration

	// dial vervangt in tests het netwerk.
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (n NetProber) Probe(ctx context.Context, vip string, p Probe) error {
	ctx, cancel := context.WithTimeout(ctx, n.Timeout)
	defer cancel()
	dial := n.dial
	if dial == nil {
		var d net.Dialer
		dial = d.DialContext
	}
	if p.TCP != nil {
		c, err := dial(ctx, "tcp", net.JoinHostPort(vip, strconv.Itoa(p.TCP.Port)))
		if err != nil {
			return err
		}
		return c.Close()
	}
	if p.HTTP == nil {
		return errors.New("probe zonder HTTP of TCP")
	}
	host := vip
	if strings.Contains(vip, ":") {
		host = "[" + vip + "]"
	}
	u := url.URL{Scheme: "http", Host: host, Path: p.HTTP.Path}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	client := http.Client{
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: dial},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	if res.StatusCode != p.HTTP.Expect {
		return fmt.Errorf("status %d in plaats van %d", res.StatusCode, p.HTTP.Expect)
	}
	return nil
}
