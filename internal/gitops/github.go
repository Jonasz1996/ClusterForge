package gitops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	ownerRe  = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	repoRe   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	branchRe = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
	shaRe    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// DefaultAPIURL is de API van github.com.
const DefaultAPIURL = "https://api.github.com"

// NormalizeAPIURL controleert het adres van de API: https, of http naar
// het eigen toestel voor tests en make dev. Het token gaat alleen naar
// deze host.
func NormalizeAPIURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultAPIURL, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("het API-adres is geen geldige URL, zoals %s", DefaultAPIURL)
	}
	switch u.Scheme {
	case "https":
	case "http":
		host := u.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", errors.New("het API-adres moet https gebruiken; http mag alleen naar dit toestel zelf, voor tests")
		}
	default:
		return "", errors.New("het API-adres moet met https:// beginnen")
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/"), nil
}

// CheckRepo controleert eigenaar, naam en branch.
func CheckRepo(owner, name, branch string) error {
	switch {
	case !ownerRe.MatchString(owner):
		return errors.New("de eigenaar van de repository is ongeldig")
	case !repoRe.MatchString(name) || name == "." || name == "..":
		return errors.New("de naam van de repository is ongeldig")
	case !branchRe.MatchString(branch) || strings.Contains(branch, "..") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/"):
		return errors.New("de branch is ongeldig")
	}
	return nil
}

// SplitRepository leest eigenaar/naam, ook uit een link zoals
// https://github.com/eigenaar/naam of git@github.com:eigenaar/naam.git.
func SplitRepository(s string) (owner, name string) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "git@"); ok {
		_, s, _ = strings.Cut(rest, ":")
	} else if u, err := url.Parse(s); err == nil && u.Host != "" {
		s = u.Path
	}
	s = strings.TrimSuffix(strings.Trim(s, "/"), ".git")
	owner, name, _ = strings.Cut(s, "/")
	return owner, name
}

// WebURL is het adres van de website bij een API-adres: github.com bij
// api.github.com, de host zelf bij GitHub Enterprise (/api/v3) of een
// testserver.
func WebURL(apiURL string) string {
	u, err := url.Parse(apiURL)
	if err != nil {
		return apiURL
	}
	if u.Host == "api.github.com" {
		return "https://github.com"
	}
	return u.Scheme + "://" + u.Host + strings.TrimSuffix(strings.TrimSuffix(u.Path, "/api/v3"), "/")
}

// FileURL is de link naar een bestand op de website.
func FileURL(apiURL, owner, name, branch, path string) string {
	return WebURL(apiURL) + "/" + owner + "/" + name + "/blob/" + branch + "/" + path
}

// GitHub leest een repository met de REST-API van GitHub.
type GitHub struct {
	cfg  Config
	base string
	http *http.Client
}

// NewGitHub maakt een client. Het token gaat alleen mee naar de host van
// het API-adres; een omleiding naar een andere host wordt niet gevolgd.
func NewGitHub(cfg Config) (*GitHub, error) {
	api, err := NormalizeAPIURL(cfg.APIURL)
	if err != nil {
		return nil, err
	}
	if err := CheckRepo(cfg.Owner, cfg.Name, cfg.Branch); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("het token ontbreekt")
	}
	host := mustHost(api)
	return &GitHub{
		cfg:  cfg,
		base: api + "/repos/" + url.PathEscape(cfg.Owner) + "/" + url.PathEscape(cfg.Name),
		http: &http.Client{
			Timeout: 20 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				downgrade := req.URL.Scheme != "https" && !strings.HasPrefix(api, "http://")
				if req.URL.Host != host || downgrade || len(via) > 3 {
					return errors.New("GitHub stuurt door naar een ander adres; het token gaat daar niet heen")
				}
				return nil
			},
		},
	}, nil
}

func mustHost(raw string) string {
	u, _ := url.Parse(raw)
	return u.Host
}

// APIError is een antwoord van GitHub dat geen succes is.
type APIError struct {
	Status int
	Msg    string
}

func (e *APIError) Error() string { return e.Msg }

func (g *GitHub) get(ctx context.Context, path, accept, etag string, limit int64) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.base+path, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.cfg.Token)
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "ClusterForge")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		// De fout van net/http noemt de URL, nooit de headers.
		return nil, nil, fmt.Errorf("GitHub is niet bereikbaar: %w", unwrapURL(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotModified {
		return nil, resp.Header, ErrNotModified
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, nil, fmt.Errorf("antwoord van GitHub lezen: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, statusError(resp, body)
	}
	if int64(len(body)) > limit {
		return nil, nil, errors.New("het antwoord van GitHub is te groot")
	}
	return body, resp.Header, nil
}

func unwrapURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func statusError(resp *http.Response, body []byte) error {
	var msg struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &msg)
	e := &APIError{Status: resp.StatusCode}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		e.Msg = "GitHub weigert het token: het is ongeldig of verlopen"
	case http.StatusForbidden, http.StatusTooManyRequests:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.StatusCode == http.StatusTooManyRequests {
			e.Msg = "de limiet van GitHub is bereikt; ClusterForge probeert het straks opnieuw"
		} else {
			e.Msg = "het token mag deze repository niet lezen; geef het Contents: read-only op alleen deze repository"
		}
	case http.StatusNotFound:
		e.Msg = "repository of branch niet gevonden, of het token mag hem niet zien"
	case http.StatusConflict:
		e.Msg = "de repository is leeg"
	default:
		e.Msg = fmt.Sprintf("GitHub antwoordt met %d", resp.StatusCode)
		if msg.Message != "" && len(msg.Message) < 200 {
			e.Msg += ": " + msg.Message
		}
	}
	return e
}

func (g *GitHub) Head(ctx context.Context, etag string) (string, string, error) {
	path := "/commits/" + escapeRef(g.cfg.Branch)
	body, hdr, err := g.get(ctx, path, "application/vnd.github.sha", etag, 200)
	if errors.Is(err, ErrNotModified) {
		return "", etag, err
	}
	if err != nil {
		return "", "", err
	}
	sha := strings.TrimSpace(string(body))
	if !shaRe.MatchString(sha) {
		return "", "", errors.New("GitHub gaf geen commit voor de branch")
	}
	return sha, hdr.Get("ETag"), nil
}

func escapeRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func (g *GitHub) Commit(ctx context.Context, sha string) (Commit, error) {
	if !shaRe.MatchString(sha) {
		return Commit{}, errors.New("ongeldige commit")
	}
	body, _, err := g.get(ctx, "/git/commits/"+sha, "application/vnd.github+json", "", 1<<20)
	if err != nil {
		return Commit{}, err
	}
	var c struct {
		SHA     string `json:"sha"`
		Message string `json:"message"`
		HTMLURL string `json:"html_url"`
		Author  struct {
			Name string    `json:"name"`
			Date time.Time `json:"date"`
		} `json:"author"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
		Verification struct {
			Verified bool `json:"verified"`
		} `json:"verification"`
	}
	if err := json.Unmarshal(body, &c); err != nil || !shaRe.MatchString(c.Tree.SHA) {
		return Commit{}, errors.New("GitHub gaf een onleesbare commit")
	}
	msg := c.Message
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	return Commit{
		SHA: sha, Message: strings.TrimRight(msg, "\n"), Author: c.Author.Name, Date: c.Author.Date,
		Verified: c.Verification.Verified, URL: c.HTMLURL, Tree: c.Tree.SHA,
	}, nil
}

func (g *GitHub) Tree(ctx context.Context, sha string) ([]TreeEntry, error) {
	if !shaRe.MatchString(sha) {
		return nil, errors.New("ongeldige tree")
	}
	body, _, err := g.get(ctx, "/git/trees/"+sha+"?recursive=1", "application/vnd.github+json", "", 16<<20)
	if err != nil {
		return nil, err
	}
	var t struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
			Size int64  `json:"size"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, errors.New("GitHub gaf een onleesbare tree")
	}
	if t.Truncated {
		return nil, errors.New("de repository is te groot om in één keer te lezen; zet de clusterbestanden in een kleinere repository")
	}
	out := make([]TreeEntry, 0, len(t.Tree))
	for _, e := range t.Tree {
		out = append(out, TreeEntry{Path: e.Path, Type: e.Type, SHA: e.SHA, Size: e.Size})
	}
	return out, nil
}

func (g *GitHub) Blob(ctx context.Context, sha string) ([]byte, error) {
	if !shaRe.MatchString(sha) {
		return nil, errors.New("ongeldige blob")
	}
	body, _, err := g.get(ctx, "/git/blobs/"+sha, "application/vnd.github+json", "", 2*MaxFileSize)
	if err != nil {
		return nil, err
	}
	var b struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		Size     int64  `json:"size"`
	}
	if err := json.Unmarshal(body, &b); err != nil || b.Encoding != "base64" {
		return nil, errors.New("GitHub gaf een onleesbaar bestand")
	}
	if b.Size > MaxFileSize {
		return nil, fmt.Errorf("het bestand is groter dan %d KB", MaxFileSize>>10)
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(b.Content, "\n", ""))
	if err != nil {
		return nil, errors.New("GitHub gaf een onleesbaar bestand")
	}
	return data, nil
}
