package inventory

import (
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

// ClusterTypes zijn de clustertypes die de webinterface en de API kennen. De
// database slaat het type als tekst op, zodat een nieuw type geen migratie vraagt.
var ClusterTypes = []string{"keepalived", "nginx", "docker", "cron", "postgresql_ha", "mariadb_ha", "generic"}

const maxTags = 20

var (
	slugRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	tagRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:=/-]{0,62}$`)
	hostnameRe  = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]{0,251}[a-zA-Z0-9])?$`)
	interfaceRe = regexp.MustCompile(`^[a-zA-Z0-9_.:@-]{0,15}$`)
)

func invalid(format string, a ...any) error { return ValidationError{Msg: fmt.Sprintf(format, a...)} }

func (f *ClusterFields) normalize() error {
	f.Slug = strings.TrimSpace(f.Slug)
	f.Name = strings.TrimSpace(f.Name)
	f.Description = strings.TrimSpace(f.Description)
	f.GitRepoURL = strings.TrimSpace(f.GitRepoURL)
	if !slugRe.MatchString(f.Slug) {
		return invalid("slug mag alleen kleine letters, cijfers en streepjes bevatten (max. 63 tekens)")
	}
	if f.Name == "" || len(f.Name) > 128 {
		return invalid("naam is verplicht en hoogstens 128 tekens")
	}
	if len(f.Description) > 2000 {
		return invalid("beschrijving is hoogstens 2000 tekens")
	}
	if !slices.Contains(ClusterTypes, f.Type) {
		return invalid("onbekend clustertype %q", f.Type)
	}
	if !validEnvironment(f.Environment) {
		return invalid("onbekende omgeving %q", f.Environment)
	}
	if f.GitRepoURL != "" {
		ok := strings.HasPrefix(f.GitRepoURL, "https://") || strings.HasPrefix(f.GitRepoURL, "ssh://") ||
			strings.HasPrefix(f.GitRepoURL, "git@")
		if !ok || len(f.GitRepoURL) > 512 || strings.ContainsAny(f.GitRepoURL, " \t\r\n") {
			return invalid("git-repository moet beginnen met https://, ssh:// of git@")
		}
		if credentialsInURL(f.GitRepoURL) {
			return invalid("zet geen gebruikersnaam, wachtwoord of token in de git-URL; die komt dan in het logboek terecht")
		}
	}
	tags, err := normalizeTags(f.Tags)
	f.Tags = tags
	return err
}

func (f *NodeFields) normalize() error {
	f.Hostname = strings.TrimSpace(f.Hostname)
	f.Role = strings.TrimSpace(f.Role)
	f.Description = strings.TrimSpace(f.Description)
	f.PrimaryIP = strings.TrimSpace(f.PrimaryIP)
	if !hostnameRe.MatchString(f.Hostname) {
		return invalid("ongeldige hostname %q", f.Hostname)
	}
	if len(f.Role) > 64 {
		return invalid("rol is hoogstens 64 tekens")
	}
	if len(f.Description) > 2000 {
		return invalid("beschrijving is hoogstens 2000 tekens")
	}
	if !validLifecycle(f.Lifecycle) {
		return invalid("onbekende lifecycle %q", f.Lifecycle)
	}
	if f.PrimaryIP != "" {
		ip, err := netip.ParseAddr(f.PrimaryIP)
		if err != nil {
			return invalid("ongeldig IP-adres %q", f.PrimaryIP)
		}
		f.PrimaryIP = ip.String()
	}
	// VMID's in Proxmox lopen van 100 tot 999999999.
	if f.Proxmox != nil && (f.Proxmox.VMID < 100 || f.Proxmox.VMID > 999999999) {
		return invalid("ongeldig VMID %d", f.Proxmox.VMID)
	}
	tags, err := normalizeTags(f.Tags)
	f.Tags = tags
	return err
}

func (f *VIPFields) normalize() error {
	f.Address = strings.TrimSpace(f.Address)
	f.Interface = strings.TrimSpace(f.Interface)
	f.Description = strings.TrimSpace(f.Description)
	ip, err := netip.ParseAddr(f.Address)
	if err != nil {
		return invalid("ongeldig VIP-adres %q", f.Address)
	}
	f.Address = ip.String()
	if !interfaceRe.MatchString(f.Interface) {
		return invalid("ongeldige interfacenaam %q", f.Interface)
	}
	if f.VRID != nil && (*f.VRID < 1 || *f.VRID > 255) {
		return invalid("VRID moet tussen 1 en 255 liggen")
	}
	if len(f.Description) > 2000 {
		return invalid("beschrijving is hoogstens 2000 tekens")
	}
	return nil
}

// normalizeTags zet tags in kleine letters en haalt dubbele en lege weg; de
// volgorde blijft zoals opgegeven.
func normalizeTags(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || slices.Contains(out, t) {
			continue
		}
		if !tagRe.MatchString(t) {
			return nil, invalid("ongeldige tag %q", t)
		}
		out = append(out, t)
	}
	if len(out) > maxTags {
		return nil, invalid("hoogstens %d tags", maxTags)
	}
	return out, nil
}

func validEnvironment(e store.Environment) bool {
	switch e {
	case store.EnvironmentLab, store.EnvironmentTest, store.EnvironmentProd:
		return true
	}
	return false
}

func validLifecycle(l store.NodeLifecycle) bool {
	switch l {
	case store.NodeLifecycleProvisioning, store.NodeLifecycleActive, store.NodeLifecycleMaintenance,
		store.NodeLifecycleDraining, store.NodeLifecycleDecommissioned:
		return true
	}
	return false
}

// credentialsInURL is true als een git-URL een wachtwoord of token draagt.
// Bij SSH is een gebruikersnaam als git@ gewoon; bij HTTPS is elke
// gebruikersnaam verdacht, want daar staat meestal een token.
func credentialsInURL(raw string) bool {
	if strings.HasPrefix(raw, "git@") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		// Geen geldige URL: zoek dan zelf naar gebruiker:wachtwoord@.
		at := strings.LastIndex(raw, "@")
		return at > 0 && strings.Contains(raw[:at], ":") && strings.Contains(raw[:at], "//")
	}
	if u.User == nil {
		return false
	}
	_, hasPassword := u.User.Password()
	return u.Scheme == "https" || hasPassword
}
