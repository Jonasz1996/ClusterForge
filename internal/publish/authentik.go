package publish

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Authentik (API v3) via de agent op een Authentik-node. ClusterForge
// gebruikt het API-token uit de instellingen.

// AKApplication is een applicatie.
type AKApplication struct {
	PK            string `json:"pk"`
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	Provider      *int   `json:"provider"`
	MetaLaunchURL string `json:"meta_launch_url"`
	Group         string `json:"group"`
}

// AKProxyProvider is een proxy provider.
type AKProxyProvider struct {
	PK                int    `json:"pk"`
	Name              string `json:"name"`
	ExternalHost      string `json:"external_host"`
	InternalHost      string `json:"internal_host"`
	Mode              string `json:"mode"`
	AuthorizationFlow string `json:"authorization_flow"`
	AssignedAppSlug   string `json:"assigned_application_slug"`
	AssignedAppName   string `json:"assigned_application_name"`
}

// Host is de naam uit external_host.
func (p AKProxyProvider) Host() string {
	u, err := url.Parse(p.ExternalHost)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// AKOutpost is een outpost.
type AKOutpost struct {
	PK        string `json:"pk"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Managed   string `json:"managed"`
	Providers []int  `json:"providers"`
}

// Embedded zegt of dit de ingebouwde outpost is.
func (o AKOutpost) Embedded() bool { return o.Managed == "goauthentik.io/outposts/embedded" }

// AKFlow is een flow.
type AKFlow struct {
	PK          string `json:"pk"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Designation string `json:"designation"`
}

// AKRead is wat ClusterForge laatst in Authentik las.
type AKRead struct {
	Node         uuid.UUID         `json:"node_id"`
	Hostname     string            `json:"hostname"`
	Version      string            `json:"version,omitempty"`
	Applications []AKApplication   `json:"applications"`
	Providers    []AKProxyProvider `json:"providers"`
	Outposts     []AKOutpost       `json:"outposts"`
	AuthFlows    []AKFlow          `json:"authorization_flows"`
	InvalFlows   []AKFlow          `json:"invalidation_flows"`
}

// EmbeddedOutpost geeft de ingebouwde outpost; nil zonder.
func (r *AKRead) EmbeddedOutpost() *AKOutpost {
	for i := range r.Outposts {
		if r.Outposts[i].Embedded() {
			return &r.Outposts[i]
		}
	}
	return nil
}

// InOutpost zegt of een provider in een outpost zit.
func (r *AKRead) InOutpost(pk int) bool {
	for _, o := range r.Outposts {
		for _, p := range o.Providers {
			if p == pk {
				return true
			}
		}
	}
	return false
}

func (s *Service) akCall(ctx context.Context, st Settings, node nodeRow, method, path string, body any) (int, json.RawMessage, error) {
	token, err := s.authentikToken(st)
	if err != nil {
		return 0, nil, err
	}
	h := &protocol.WebHTTP{Method: method, Port: st.AuthentikPort, Path: "/api/v3" + path, Headers: map[string]string{"Authorization": "Bearer " + token}}
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		h.Body = b
	}
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	res, err := s.bus.Web(cctx, node.ID, protocol.WebRequest{Op: protocol.WebOpHTTP, HTTP: h})
	if err != nil {
		return 0, nil, fmt.Errorf("authentik via %s: %w", node.Hostname, err)
	}
	if res.Error != "" {
		return 0, nil, fmt.Errorf("authentik via %s: %s", node.Hostname, res.Error)
	}
	if res.Status == http.StatusForbidden || res.Status == http.StatusUnauthorized {
		return res.Status, res.Body, invalid("Authentik weigert het token (HTTP %d): maak in Authentik een token met intent API voor een beheerder", res.Status)
	}
	return res.Status, res.Body, nil
}

// akMessage haalt de fout uit een antwoord van Authentik.
func akMessage(status int, data []byte) string {
	var m map[string]any
	if json.Unmarshal(data, &m) == nil && len(m) > 0 {
		if d, ok := m["detail"].(string); ok {
			return fmt.Sprintf("%s (HTTP %d)", d, status)
		}
		b, _ := json.Marshal(m)
		return fmt.Sprintf("HTTP %d: %s", status, trimText(string(b), 300))
	}
	return fmt.Sprintf("HTTP %d", status)
}

// akList leest alle pagina's van een lijst.
func akList[T any](ctx context.Context, s *Service, st Settings, node nodeRow, path string) ([]T, error) {
	var out []T
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for page := 1; page <= 50; page++ {
		status, data, err := s.akCall(ctx, st, node, http.MethodGet, fmt.Sprintf("%s%spage=%d&page_size=200", path, sep, page), nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("authentik %s: %s", path, akMessage(status, data))
		}
		var res struct {
			Pagination struct {
				Next float64 `json:"next"`
			} `json:"pagination"`
			Results []T `json:"results"`
		}
		if err := json.Unmarshal(data, &res); err != nil {
			return nil, fmt.Errorf("authentik %s: %w", path, err)
		}
		out = append(out, res.Results...)
		if res.Pagination.Next == 0 {
			break
		}
	}
	return out, nil
}

func (s *Service) akNode(ctx context.Context, st Settings) (nodeRow, error) {
	return s.pickNode(ctx, st.AuthentikClusterID, st.AuthentikNodeID, "Authentik")
}

// readAuthentik leest applicaties, proxy providers, outposts en flows.
func (s *Service) readAuthentik(ctx context.Context, st Settings) (*AKRead, error) {
	node, err := s.akNode(ctx, st)
	if err != nil {
		return nil, err
	}
	out := &AKRead{Node: node.ID, Hostname: node.Hostname}
	if status, data, err := s.akCall(ctx, st, node, http.MethodGet, "/admin/version/", nil); err != nil {
		return nil, err
	} else if status == http.StatusOK {
		var v struct {
			Current string `json:"version_current"`
		}
		_ = json.Unmarshal(data, &v)
		out.Version = v.Current
	}
	if out.Applications, err = akList[AKApplication](ctx, s, st, node, "/core/applications/?superuser_full_list=true"); err != nil {
		return nil, err
	}
	if out.Providers, err = akList[AKProxyProvider](ctx, s, st, node, "/providers/proxy/"); err != nil {
		return nil, err
	}
	if out.Outposts, err = akList[AKOutpost](ctx, s, st, node, "/outposts/instances/"); err != nil {
		return nil, err
	}
	if out.AuthFlows, err = akList[AKFlow](ctx, s, st, node, "/flows/instances/?designation=authorization"); err != nil {
		return nil, err
	}
	// De invalidatieflow bestaat sinds 2024.10; een oudere Authentik geeft
	// een lege lijst.
	out.InvalFlows, _ = akList[AKFlow](ctx, s, st, node, "/flows/instances/?designation=invalidation")
	for _, l := range []*[]AKApplication{&out.Applications} {
		if *l == nil {
			*l = []AKApplication{}
		}
	}
	if out.Providers == nil {
		out.Providers = []AKProxyProvider{}
	}
	if out.Outposts == nil {
		out.Outposts = []AKOutpost{}
	}
	return out, nil
}

// preferFlow kiest een flow: de standaard van Authentik als die er is.
func preferFlow(flows []AKFlow, slugs ...string) string {
	for _, want := range slugs {
		for _, f := range flows {
			if f.Slug == want {
				return f.PK
			}
		}
	}
	if len(flows) > 0 {
		return flows[0].PK
	}
	return ""
}

// akProtect maakt provider en applicatie en zet de provider in de
// ingebouwde outpost. Het geeft terug wat het maakte, ook bij een fout, zodat
// de taak het kan opruimen.
type akCreated struct {
	ProviderPK int `json:"provider_pk,omitempty"`
	// ProviderCreated: ClusterForge maakte de provider (en mag hem dus weer
	// verwijderen).
	ProviderCreated bool   `json:"provider_created,omitempty"`
	AppSlug         string `json:"app_slug,omitempty"`
	OutpostPK       string `json:"outpost_pk,omitempty"`
	// OutpostAdded: de provider kwam in de outpost door ClusterForge.
	OutpostAdded bool `json:"outpost_added,omitempty"`
	// AppRelinked: een bestaande applicatie met dezelfde slug kreeg deze
	// provider; AppOldProvider is wat ze eerst had (0 = geen).
	AppRelinked    string `json:"app_relinked,omitempty"`
	AppOldProvider int    `json:"app_old_provider,omitempty"`
}

func (s *Service) akProtect(ctx context.Context, st Settings, node nodeRow, name, slug, domain, group string, log func(string, ...any)) (akCreated, error) {
	var done akCreated
	r, err := s.readAuthentik(ctx, st)
	if err != nil {
		return done, err
	}
	external := "https://" + domain
	// Bestaat er al een provider voor deze naam, dan hergebruiken.
	var prov *AKProxyProvider
	for i, p := range r.Providers {
		if p.Host() == domain {
			prov = &r.Providers[i]
			break
		}
	}
	if prov == nil {
		auth := preferFlow(r.AuthFlows, "default-provider-authorization-implicit-consent", "default-provider-authorization-explicit-consent")
		if auth == "" {
			return done, fmt.Errorf("authentik heeft geen autorisatieflow")
		}
		body := map[string]any{"name": "ClusterForge: " + domain, "authorization_flow": auth, "external_host": external, "mode": "forward_single"}
		if inval := preferFlow(r.InvalFlows, "default-provider-invalidation-flow", "default-invalidation-flow"); inval != "" {
			body["invalidation_flow"] = inval
		}
		status, data, err := s.akCall(ctx, st, node, http.MethodPost, "/providers/proxy/", body)
		if err != nil {
			return done, err
		}
		if status >= 300 {
			return done, fmt.Errorf("proxy provider maken: %s", akMessage(status, data))
		}
		var p AKProxyProvider
		if err := json.Unmarshal(data, &p); err != nil {
			return done, err
		}
		prov = &p
		done.ProviderPK, done.ProviderCreated = p.PK, true
		log("Proxy provider %q gemaakt (forward auth, %s)", p.Name, external)
	} else {
		log("Proxy provider %q bestaat al voor %s; hergebruikt", prov.Name, domain)
	}
	var sameSlug *AKApplication
	for i, a := range r.Applications {
		if a.Slug == slug {
			sameSlug = &r.Applications[i]
		}
	}
	if prov.AssignedAppSlug == "" && sameSlug != nil {
		// De applicatie bestaat al (met een andere of geen provider): die
		// krijgt deze provider, in plaats van een tweede aan te maken.
		status, data, err := s.akCall(ctx, st, node, http.MethodPatch, "/core/applications/"+url.PathEscape(slug)+"/", map[string]any{"provider": prov.PK})
		if err != nil {
			return done, err
		}
		if status >= 300 {
			return done, fmt.Errorf("bestaande applicatie %s koppelen: %s", slug, akMessage(status, data))
		}
		done.AppRelinked = slug
		if sameSlug.Provider != nil {
			done.AppOldProvider = *sameSlug.Provider
			log("Bestaande applicatie %q (%s) gekoppeld aan deze provider (had provider %d)", sameSlug.Name, slug, *sameSlug.Provider)
		} else {
			log("Bestaande applicatie %q (%s) gekoppeld aan deze provider", sameSlug.Name, slug)
		}
	} else if prov.AssignedAppSlug == "" {
		body := map[string]any{"name": name, "slug": slug, "provider": prov.PK, "meta_launch_url": external}
		if group != "" {
			body["group"] = group
		}
		status, data, err := s.akCall(ctx, st, node, http.MethodPost, "/core/applications/", body)
		if err != nil {
			return done, err
		}
		if status >= 300 {
			return done, fmt.Errorf("applicatie maken: %s", akMessage(status, data))
		}
		done.AppSlug = slug
		log("Applicatie %q (%s) gemaakt", name, slug)
	} else {
		log("Applicatie %s hoort al bij de provider", prov.AssignedAppSlug)
	}
	out := r.EmbeddedOutpost()
	if out == nil {
		return done, fmt.Errorf("authentik heeft geen ingebouwde outpost (authentik Embedded Outpost)")
	}
	done.OutpostPK = out.PK
	if !r.InOutpost(prov.PK) {
		providers := append(append([]int{}, out.Providers...), prov.PK)
		status, data, err := s.akCall(ctx, st, node, http.MethodPatch, "/outposts/instances/"+out.PK+"/", map[string]any{"providers": providers})
		if err != nil {
			return done, err
		}
		if status >= 300 {
			return done, fmt.Errorf("provider aan de outpost toevoegen: %s", akMessage(status, data))
		}
		done.OutpostAdded = true
		log("Provider toegevoegd aan %s", out.Name)
	}
	done.ProviderPK = prov.PK
	return done, nil
}

// akUndo ruimt op wat akProtect maakte: uit de outpost, de applicatie en de
// provider weg, alleen wat ClusterForge zelf maakte of toevoegde.
func (s *Service) akUndo(ctx context.Context, st Settings, node nodeRow, c akCreated, log func(string, ...any)) error {
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if c.OutpostAdded && c.ProviderPK > 0 {
		r, err := s.readAuthentik(ctx, st)
		keep(err)
		if err == nil {
			for _, o := range r.Outposts {
				// Zonder bekende outpost: elke outpost die de provider bevat.
				if c.OutpostPK != "" && o.PK != c.OutpostPK {
					continue
				}
				if !containsInt(o.Providers, c.ProviderPK) {
					continue
				}
				var rest []int
				for _, p := range o.Providers {
					if p != c.ProviderPK {
						rest = append(rest, p)
					}
				}
				if rest == nil {
					rest = []int{}
				}
				status, data, err := s.akCall(ctx, st, node, http.MethodPatch, "/outposts/instances/"+o.PK+"/", map[string]any{"providers": rest})
				if err == nil && status >= 300 {
					err = fmt.Errorf("outpost: %s", akMessage(status, data))
				}
				keep(err)
				if err == nil {
					log("Provider uit de outpost gehaald")
				}
			}
		}
	}
	if c.AppRelinked != "" {
		var prov any
		if c.AppOldProvider > 0 {
			prov = c.AppOldProvider
		}
		status, data, err := s.akCall(ctx, st, node, http.MethodPatch, "/core/applications/"+url.PathEscape(c.AppRelinked)+"/", map[string]any{"provider": prov})
		if err == nil && status >= 300 && status != http.StatusNotFound {
			err = fmt.Errorf("applicatie terugzetten: %s", akMessage(status, data))
		}
		keep(err)
		if err == nil {
			log("Applicatie %s terug naar haar vorige provider", c.AppRelinked)
		}
	}
	if c.AppSlug != "" {
		status, data, err := s.akCall(ctx, st, node, http.MethodDelete, "/core/applications/"+url.PathEscape(c.AppSlug)+"/", nil)
		if err == nil && status >= 300 && status != http.StatusNotFound {
			err = fmt.Errorf("applicatie verwijderen: %s", akMessage(status, data))
		}
		keep(err)
		if err == nil {
			log("Applicatie %s verwijderd", c.AppSlug)
		}
	}
	if c.ProviderCreated && c.ProviderPK > 0 {
		status, data, err := s.akCall(ctx, st, node, http.MethodDelete, fmt.Sprintf("/providers/proxy/%d/", c.ProviderPK), nil)
		if err == nil && status >= 300 && status != http.StatusNotFound {
			err = fmt.Errorf("provider verwijderen: %s", akMessage(status, data))
		}
		keep(err)
		if err == nil {
			log("Proxy provider %d verwijderd", c.ProviderPK)
		}
	}
	return firstErr
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
