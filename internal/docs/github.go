package docs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/gitops"
)

var (
	// errNoBranch: de repository bestaat, de branch nog niet.
	errNoBranch = errors.New("de branch bestaat nog niet")
	// errMoved: de branch veranderde tussen lezen en bijwerken.
	errMoved = errors.New("de branch veranderde intussen")
)

// gitHub schrijft met de Git Data API van GitHub: blobs, een tree, een
// commit en dan de branch bijwerken zonder force. Het token gaat alleen naar
// de host van het API-adres, zoals bij de GitHub-client van GitOps.
type gitHub struct {
	token string
	base  string
	http  *http.Client
}

func newGitHub(apiURL, owner, name, token string) (*gitHub, error) {
	api, err := gitops.NormalizeAPIURL(apiURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("het token ontbreekt")
	}
	u, _ := url.Parse(api)
	host := u.Host
	return &gitHub{
		token: token,
		base:  api + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name),
		http: &http.Client{
			Timeout: 30 * time.Second,
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

// ghError is een antwoord van GitHub dat geen succes is.
type ghError struct {
	Status int
	Msg    string
	// Raw is de melding van GitHub zelf.
	Raw string
}

func (e *ghError) Error() string { return e.Msg }

func (g *gitHub) do(ctx context.Context, method, path string, in, out any, limit int64) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "ClusterForge")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.http.Do(req)
	if err != nil {
		// De fout van net/http noemt de URL, nooit de headers.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("GitHub is niet bereikbaar: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("antwoord van GitHub lezen: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return statusError(resp, data)
	}
	if int64(len(data)) > limit {
		return errors.New("het antwoord van GitHub is te groot")
	}
	if out != nil && json.Unmarshal(data, out) != nil {
		return errors.New("GitHub gaf een onleesbaar antwoord")
	}
	return nil
}

func statusError(resp *http.Response, body []byte) error {
	var msg struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &msg)
	e := &ghError{Status: resp.StatusCode, Raw: msg.Message}
	limited := resp.Header.Get("X-RateLimit-Remaining") == "0" || strings.Contains(strings.ToLower(msg.Message), "rate limit")
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		e.Msg = "GitHub weigert het token: het is ongeldig of verlopen"
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusForbidden && limited:
		e.Msg = "de limiet van GitHub is bereikt; ClusterForge probeert het straks opnieuw"
	case resp.StatusCode == http.StatusForbidden:
		e.Msg = "het token mag niet schrijven in deze repository; geef het Contents: Read and write op alleen deze repository"
	case resp.StatusCode == http.StatusNotFound:
		e.Msg = "repository niet gevonden, of het token mag hem niet zien"
	case resp.StatusCode == http.StatusConflict:
		e.Msg = "de repository is leeg; maak hem op GitHub aan met een README, dan kan ClusterForge erin schrijven"
	default:
		e.Msg = fmt.Sprintf("GitHub antwoordt met %d", resp.StatusCode)
		if msg.Message != "" && len(msg.Message) < 200 {
			e.Msg += ": " + msg.Message
		}
	}
	return e
}

func status(err error) int {
	var e *ghError
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func escapeRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// repo controleert of de repository er is en het token hem ziet.
func (g *gitHub) repo(ctx context.Context) error {
	return g.do(ctx, http.MethodGet, "", nil, nil, 1<<20)
}

// ref geeft de commit aan de kop van een branch, of errNoBranch.
func (g *gitHub) ref(ctx context.Context, branch string) (string, error) {
	var out struct {
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	err := g.do(ctx, http.MethodGet, "/git/ref/heads/"+escapeRef(branch), nil, &out, 1<<20)
	if status(err) == http.StatusNotFound {
		return "", errNoBranch
	}
	if err != nil {
		return "", err
	}
	if out.Object.Type != "commit" || !shaRe.MatchString(out.Object.SHA) {
		return "", errors.New("GitHub gaf geen commit voor de branch")
	}
	return out.Object.SHA, nil
}

// commitTree geeft de tree van een commit.
func (g *gitHub) commitTree(ctx context.Context, sha string) (string, error) {
	var out struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := g.do(ctx, http.MethodGet, "/git/commits/"+sha, nil, &out, 1<<20); err != nil {
		return "", err
	}
	if !shaRe.MatchString(out.Tree.SHA) {
		return "", errors.New("GitHub gaf een onleesbare commit")
	}
	return out.Tree.SHA, nil
}

// blobs geeft van elk bestand in een tree de sha van zijn inhoud.
func (g *gitHub) blobs(ctx context.Context, tree string) (map[string]string, error) {
	var out struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"tree"`
	}
	if err := g.do(ctx, http.MethodGet, "/git/trees/"+tree+"?recursive=1", nil, &out, 32<<20); err != nil {
		return nil, err
	}
	if out.Truncated {
		return nil, errors.New("de repository is te groot om in één keer te lezen; zet de documentatie in een eigen repository")
	}
	files := map[string]string{}
	for _, e := range out.Tree {
		if e.Type == "blob" {
			files[e.Path] = e.SHA
		}
	}
	return files, nil
}

// blob maakt een blob en geeft zijn sha.
func (g *gitHub) blob(ctx context.Context, content []byte) (string, error) {
	var out struct {
		SHA string `json:"sha"`
	}
	in := map[string]string{"content": base64.StdEncoding.EncodeToString(content), "encoding": "base64"}
	if err := g.do(ctx, http.MethodPost, "/git/blobs", in, &out, 1<<20); err != nil {
		return "", err
	}
	if !shaRe.MatchString(out.SHA) {
		return "", errors.New("GitHub gaf geen blob terug")
	}
	return out.SHA, nil
}

type treeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	// SHA nil verwijdert het bestand.
	SHA *string `json:"sha"`
}

// tree maakt een tree op base (leeg: een nieuwe tree) met deze wijzigingen.
func (g *gitHub) tree(ctx context.Context, base string, entries []treeEntry) (string, error) {
	in := map[string]any{"tree": entries}
	if base != "" {
		in["base_tree"] = base
	}
	var out struct {
		SHA string `json:"sha"`
	}
	if err := g.do(ctx, http.MethodPost, "/git/trees", in, &out, 4<<20); err != nil {
		return "", err
	}
	if !shaRe.MatchString(out.SHA) {
		return "", errors.New("GitHub gaf geen tree terug")
	}
	return out.SHA, nil
}

// commit maakt een commit met ClusterForge als auteur en geeft de sha en
// de link.
func (g *gitHub) commit(ctx context.Context, message, tree string, parents []string, at time.Time) (string, string, error) {
	if parents == nil {
		parents = []string{}
	}
	in := map[string]any{
		"message": message, "tree": tree, "parents": parents,
		"author": map[string]string{"name": authorName, "email": authorEmail, "date": at.UTC().Format(time.RFC3339)},
	}
	var out struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
	}
	if err := g.do(ctx, http.MethodPost, "/git/commits", in, &out, 1<<20); err != nil {
		return "", "", err
	}
	if !shaRe.MatchString(out.SHA) {
		return "", "", errors.New("GitHub gaf geen commit terug")
	}
	return out.SHA, out.HTMLURL, nil
}

// update zet de branch op sha, alleen als dat een fast-forward is. Een
// nieuwe branch wordt gemaakt.
func (g *gitHub) update(ctx context.Context, branch, sha string, create bool) error {
	var err error
	if create {
		err = g.do(ctx, http.MethodPost, "/git/refs", map[string]string{"ref": "refs/heads/" + branch, "sha": sha}, nil, 1<<20)
	} else {
		err = g.do(ctx, http.MethodPatch, "/git/refs/heads/"+escapeRef(branch), map[string]any{"sha": sha, "force": false}, nil, 1<<20)
	}
	var e *ghError
	if errors.As(err, &e) && e.Status == http.StatusUnprocessableEntity {
		raw := strings.ToLower(e.Raw)
		if strings.Contains(raw, "fast forward") || strings.Contains(raw, "already exists") {
			return errMoved
		}
		// Bijvoorbeeld een beschermde branch.
		return &ghError{Status: e.Status, Raw: e.Raw, Msg: "GitHub weigert de branch bij te werken: " + clip(e.Raw, 200)}
	}
	return err
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
