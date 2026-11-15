package updates

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Release notes van de bron van een image: GitHub (ghcr.io of het label
// org.opencontainers.image.source) en Docker Hub. Ze gaan mee naar de
// assistent, die zo ziet of een omgevingsvariabele hernoemd of verplicht
// werd.

// Notes is wat één bron over een image zegt.
type Notes struct {
	// Where is "GitHub-releases", "GitHub-README" of "Docker Hub".
	Where string `json:"where"`
	URL   string `json:"url"`
	Text  string `json:"text"`
}

// Sources haalt de release notes op. Zonder netwerk naar buiten geeft het
// niets terug, met de fout erbij.
type Sources struct {
	Client *http.Client
	// GitHubAPI en DockerHubAPI zijn voor tests; leeg is het echte adres.
	GitHubAPI    string
	DockerHubAPI string
}

const (
	maxNotes       = 30 << 10
	maxReleaseBody = 6 << 10
	maxReleases    = 10
)

func (s *Sources) client() *http.Client {
	if s != nil && s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (s *Sources) gh() string {
	if s != nil && s.GitHubAPI != "" {
		return strings.TrimRight(s.GitHubAPI, "/")
	}
	return "https://api.github.com"
}

func (s *Sources) hub() string {
	if s != nil && s.DockerHubAPI != "" {
		return strings.TrimRight(s.DockerHubAPI, "/")
	}
	return "https://hub.docker.com"
}

// ImageRepo splitst een imagenaam in registry en pad zonder tag of digest:
// nginx:1.27 → docker.io, library/nginx; ghcr.io/a/b:1 → ghcr.io, a/b.
func ImageRepo(image string) (registry, repo string) {
	if i := strings.IndexByte(image, '@'); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndexByte(image, ':'); i > strings.LastIndexByte(image, '/') {
		image = image[:i]
	}
	parts := strings.Split(image, "/")
	registry = "docker.io"
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		registry, parts = parts[0], parts[1:]
	}
	if registry == "index.docker.io" || registry == "registry-1.docker.io" {
		registry = "docker.io"
	}
	if registry == "docker.io" && len(parts) == 1 {
		parts = []string{"library", parts[0]}
	}
	return registry, strings.Join(parts, "/")
}

// GitHubRepo zoekt owner/repo uit het label source of een ghcr.io-image.
func GitHubRepo(image, source string) string {
	if u, err := url.Parse(strings.TrimSuffix(source, ".git")); err == nil && strings.EqualFold(u.Host, "github.com") {
		p := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(p) >= 2 {
			return p[0] + "/" + p[1]
		}
	}
	if reg, repo := ImageRepo(image); reg == "ghcr.io" {
		p := strings.Split(repo, "/")
		if len(p) >= 2 {
			return p[0] + "/" + p[1]
		}
	}
	return ""
}

// Fetch haalt de notes voor een image. Een bron die niet antwoordt, slaat
// het over; errs zegt welke.
func (s *Sources) Fetch(ctx context.Context, image, source string) (notes []Notes, errs []string) {
	if repo := GitHubRepo(image, source); repo != "" {
		n, err := s.githubReleases(ctx, repo)
		if err == nil && n.Text == "" {
			n, err = s.githubReadme(ctx, repo)
		}
		if err != nil {
			errs = append(errs, "GitHub "+repo+": "+err.Error())
		} else if n.Text != "" {
			notes = append(notes, n)
		}
	}
	if reg, repo := ImageRepo(image); reg == "docker.io" {
		n, err := s.dockerHub(ctx, repo)
		if err != nil {
			errs = append(errs, "Docker Hub "+repo+": "+err.Error())
		} else if n.Text != "" {
			notes = append(notes, n)
		}
	}
	return notes, errs
}

func (s *Sources) get(ctx context.Context, u, accept string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "ClusterForge")
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 2<<20))
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…(ingekort)"
}

func (s *Sources) githubReleases(ctx context.Context, repo string) (Notes, error) {
	data, err := s.get(ctx, fmt.Sprintf("%s/repos/%s/releases?per_page=%d", s.gh(), repo, maxReleases), "application/vnd.github+json")
	if err != nil {
		return Notes{}, err
	}
	var rels []struct {
		Name        string `json:"name"`
		Tag         string `json:"tag_name"`
		Body        string `json:"body"`
		PublishedAt string `json:"published_at"`
		Draft       bool   `json:"draft"`
	}
	if err := json.Unmarshal(data, &rels); err != nil {
		return Notes{}, err
	}
	var b strings.Builder
	for _, r := range rels {
		if r.Draft {
			continue
		}
		fmt.Fprintf(&b, "### %s (%s, %s)\n%s\n\n", r.Tag, r.Name, r.PublishedAt, clip(strings.TrimSpace(r.Body), maxReleaseBody))
		if b.Len() > maxNotes {
			break
		}
	}
	return Notes{Where: "GitHub-releases", URL: "https://github.com/" + repo + "/releases", Text: clip(b.String(), maxNotes)}, nil
}

func (s *Sources) githubReadme(ctx context.Context, repo string) (Notes, error) {
	data, err := s.get(ctx, fmt.Sprintf("%s/repos/%s/readme", s.gh(), repo), "application/vnd.github.raw")
	if err != nil {
		return Notes{}, err
	}
	return Notes{Where: "GitHub-README", URL: "https://github.com/" + repo, Text: clip(string(data), maxNotes)}, nil
}

func (s *Sources) dockerHub(ctx context.Context, repo string) (Notes, error) {
	data, err := s.get(ctx, fmt.Sprintf("%s/v2/repositories/%s/", s.hub(), repo), "application/json")
	if err != nil {
		return Notes{}, err
	}
	var r struct {
		Description     string `json:"description"`
		FullDescription string `json:"full_description"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return Notes{}, err
	}
	text := strings.TrimSpace(r.Description + "\n\n" + r.FullDescription)
	page := "https://hub.docker.com/r/" + repo
	if strings.HasPrefix(repo, "library/") {
		page = "https://hub.docker.com/_/" + strings.TrimPrefix(repo, "library/")
	}
	return Notes{Where: "Docker Hub", URL: page, Text: clip(text, maxNotes)}, nil
}
