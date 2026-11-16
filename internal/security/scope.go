package security

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Scope is een stuk netwerk dat van de beheerder is. Scans (vanaf M3) gaan
// alleen naar adressen binnen een scope en buiten de uitsluitingen; de agent
// controleert dat opnieuw.
type Scope struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CIDRs       []string  `json:"cidrs"`
	Exclude     []string  `json:"exclude"`
	Public      bool      `json:"public"`
	PublicNote  string    `json:"public_note"`
	// Addresses is het aantal adressen (zonder uitsluitingen), afgerond.
	Addresses float64   `json:"addresses"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ScopeInput is wat de beheerder invult.
type ScopeInput struct {
	Name        string
	Description string
	CIDRs       []string
	Exclude     []string
	// PublicConfirm moet "EIGEN NETWERK" zijn als er een publiek adres in zit.
	PublicConfirm string
	PublicNote    string
}

// PublicConfirmText moet overgetikt worden voor een scope met publieke
// adressen.
const PublicConfirmText = "EIGEN NETWERK"

// MaxScopeNets begrenst het aantal netwerken per scope.
const MaxScopeNets = 64

// MinPrefix4 en MinPrefix6 zijn de grootste netwerken die een scope mag
// hebben: een /16 is 65 536 adressen; groter is geen eigen thuisnetwerk.
const (
	MinPrefix4 = 16
	MinPrefix6 = 48
)

// ParsePrefix leest een netwerk of een los adres (wordt /32 of /128).
func ParsePrefix(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("%q is geen geldig netwerk", s)
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%q is geen geldig adres of netwerk", s)
	}
	return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()), nil
}

// IsOwnRange: privé, link-local, loopback of ULA. Dat zijn netwerken die
// per definitie niet van iemand anders op internet zijn.
func IsOwnRange(p netip.Prefix) bool {
	a := p.Addr()
	return a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || cgnat.Contains(a) && p.Bits() >= cgnat.Bits()
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// NormalizeScope controleert de invoer en geeft de opgeschoonde netwerken.
func NormalizeScope(in ScopeInput) (cidrs, exclude []string, public bool, err error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 60 {
		return nil, nil, false, &FieldError{"Geef de scope een naam van hoogstens 60 tekens."}
	}
	parse := func(list []string, what string) ([]netip.Prefix, error) {
		var out []netip.Prefix
		for _, s := range list {
			if strings.TrimSpace(s) == "" {
				continue
			}
			p, err := ParsePrefix(s)
			if err != nil {
				return nil, &FieldError{err.Error() + " (" + what + ")."}
			}
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
		if len(out) > MaxScopeNets {
			return nil, &FieldError{fmt.Sprintf("Hoogstens %d netwerken per lijst.", MaxScopeNets)}
		}
		return out, nil
	}
	nets, err := parse(in.CIDRs, "netwerken")
	if err != nil {
		return nil, nil, false, err
	}
	if len(nets) == 0 {
		return nil, nil, false, &FieldError{"Geef minstens één netwerk of adres."}
	}
	ex, err := parse(in.Exclude, "uitsluitingen")
	if err != nil {
		return nil, nil, false, err
	}
	for _, p := range nets {
		if p.Addr().Is4() && p.Bits() < MinPrefix4 || p.Addr().Is6() && p.Bits() < MinPrefix6 {
			return nil, nil, false, &FieldError{fmt.Sprintf("%s is te groot; splits het in netwerken van /%d (IPv4) of /%d (IPv6) of kleiner.", p, MinPrefix4, MinPrefix6)}
		}
		if p.Addr().IsUnspecified() || p.Addr().IsMulticast() {
			return nil, nil, false, &FieldError{fmt.Sprintf("%s kan geen scope zijn.", p)}
		}
		if !IsOwnRange(p) {
			public = true
		}
	}
	if public {
		if strings.TrimSpace(in.PublicConfirm) != PublicConfirmText {
			return nil, nil, false, &FieldError{"Er zit een publiek adres in. Tik " + PublicConfirmText + " over om te bevestigen dat het van jou is of dat je schriftelijke toestemming hebt."}
		}
		if len(strings.TrimSpace(in.PublicNote)) < 10 {
			return nil, nil, false, &FieldError{"Schrijf bij een publiek adres kort waarom het van jou is (minstens 10 tekens)."}
		}
	}
	for _, p := range nets {
		cidrs = append(cidrs, p.String())
	}
	exclude = []string{}
	for _, p := range ex {
		exclude = append(exclude, p.String())
	}
	return cidrs, exclude, public, nil
}

// InScope zegt of een adres in een van de scopes valt en niet uitgesloten
// is. Dit is de poort voor elke scan.
func InScope(scopes []Scope, a netip.Addr) bool {
	a = a.Unmap()
	for _, s := range scopes {
		if containsAny(s.Exclude, a) {
			continue
		}
		if containsAny(s.CIDRs, a) {
			return true
		}
	}
	return false
}

func containsAny(list []string, a netip.Addr) bool {
	for _, c := range list {
		if p, err := ParsePrefix(c); err == nil && p.Contains(a) {
			return true
		}
	}
	return false
}

func countAddresses(cidrs []string) float64 {
	var n float64
	for _, c := range cidrs {
		p, err := ParsePrefix(c)
		if err != nil {
			continue
		}
		bits := p.Addr().BitLen() - p.Bits()
		if bits > 64 {
			bits = 64
		}
		n += float64(uint64(1) << bits)
	}
	return n
}

// FieldError: ongeldige invoer.
type FieldError struct{ Msg string }

func (e *FieldError) Error() string { return e.Msg }
