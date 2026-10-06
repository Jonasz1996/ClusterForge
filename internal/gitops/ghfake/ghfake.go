// Package ghfake is een nep-GitHub voor tests en lokale ontwikkeling. Hij
// kent net genoeg van de REST-API om GitOps te bedienen: de kop van een
// branch met ETag, commits, trees en blobs, achter een token.
package ghfake

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

type commit struct {
	sha, tree, parent, message, author string
	date                               time.Time
	verified                           bool
}

// Server is één repository met één branch.
type Server struct {
	mu      sync.Mutex
	owner   string
	name    string
	branch  string
	token   string
	head    string
	commits map[string]*commit
	// trees: tree-sha naar de bestanden (pad naar inhoud).
	trees map[string]map[string]string
	blobs map[string]string
	// requests zijn de paden die binnenkwamen, met de status.
	requests []string
	// Down laat elke vraag mislukken met 503.
	down bool
}

// New maakt een lege repository owner/name met branch main.
func New(owner, name, token string) *Server {
	return &Server{
		owner: owner, name: name, branch: "main", token: token,
		commits: map[string]*commit{}, trees: map[string]map[string]string{}, blobs: map[string]string{},
	}
}

// SetToken vervangt het token dat de server aanneemt.
func (s *Server) SetToken(t string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = t
}

// SetDown laat elke vraag mislukken, of weer lukken.
func (s *Server) SetDown(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = down
}

// Files geeft de bestanden aan de kop.
func (s *Server) Files() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.head == "" {
		return map[string]string{}
	}
	return maps.Clone(s.trees[s.commits[s.head].tree])
}

// Commit zet een commit op de branch. files zijn de bestanden die
// veranderen; een lege inhoud verwijdert het bestand. Het geeft de sha.
func (s *Server) Commit(message, author string, files map[string]string) string {
	return s.commit(message, author, false, files)
}

// CommitVerified is Commit met een handtekening die GitHub geldig vindt.
func (s *Server) CommitVerified(message, author string, files map[string]string) string {
	return s.commit(message, author, true, files)
}

// Replace zet een commit met precies deze bestanden.
func (s *Server) Replace(message, author string, files map[string]string) string {
	s.mu.Lock()
	cur := map[string]string{}
	if s.head != "" {
		cur = s.trees[s.commits[s.head].tree]
	}
	change := map[string]string{}
	for p := range cur {
		change[p] = ""
	}
	maps.Copy(change, files)
	s.mu.Unlock()
	return s.commit(message, author, false, change)
}

func (s *Server) commit(message, author string, verified bool, files map[string]string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	tree := map[string]string{}
	if s.head != "" {
		maps.Copy(tree, s.trees[s.commits[s.head].tree])
	}
	for p, c := range files {
		if c == "" {
			delete(tree, p)
		} else {
			tree[p] = c
			s.blobs[BlobSHA(c)] = c
		}
	}
	h := sha1.New()
	for _, p := range slices.Sorted(maps.Keys(tree)) {
		_, _ = fmt.Fprintf(h, "%s %s\n", p, BlobSHA(tree[p]))
	}
	treeSHA := hex.EncodeToString(h.Sum(nil))
	s.trees[treeSHA] = tree
	c := &commit{tree: treeSHA, parent: s.head, message: message, author: author, verified: verified,
		date: time.Now().UTC().Truncate(time.Second)}
	sum := sha1.Sum([]byte(fmt.Sprintf("tree %s\nparent %s\n%s\n%s\n%d", c.tree, c.parent, c.author, c.message, len(s.commits))))
	c.sha = hex.EncodeToString(sum[:])
	s.commits[c.sha] = c
	s.head = c.sha
	return c.sha
}

// BlobSHA is de sha die Git aan een bestand geeft.
func BlobSHA(content string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content)))
	return hex.EncodeToString(sum[:])
}

// Requests geeft de vragen die binnenkwamen, als "GET /pad 200".
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.serve(w, r)
	s.requests = append(s.requests, fmt.Sprintf("%s %s %d", r.Method, r.URL.RequestURI(), status))
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) int {
	fail := func(status int, msg string) int {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
		return status
	}
	if s.down {
		return fail(http.StatusServiceUnavailable, "Service Unavailable")
	}
	if r.Header.Get("Authorization") != "Bearer "+s.token {
		return fail(http.StatusUnauthorized, "Bad credentials")
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/repos/"+s.owner+"/"+s.name+"/")
	if !ok || r.Method != http.MethodGet {
		return fail(http.StatusNotFound, "Not Found")
	}
	ok200 := func(v any) int {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
		return http.StatusOK
	}
	switch {
	case strings.HasPrefix(rest, "commits/"):
		if strings.TrimPrefix(rest, "commits/") != s.branch {
			return fail(http.StatusNotFound, "No commit found for SHA")
		}
		if s.head == "" {
			return fail(http.StatusConflict, "Git Repository is empty.")
		}
		etag := `"` + s.head + `"`
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return http.StatusNotModified
		}
		if r.Header.Get("Accept") == "application/vnd.github.sha" {
			_, _ = w.Write([]byte(s.head))
			return http.StatusOK
		}
		return ok200(map[string]any{"sha": s.head})
	case strings.HasPrefix(rest, "git/commits/"):
		c, found := s.commits[strings.TrimPrefix(rest, "git/commits/")]
		if !found {
			return fail(http.StatusNotFound, "Not Found")
		}
		return ok200(map[string]any{
			"sha": c.sha, "message": c.message,
			"html_url":     "http://" + r.Host + "/" + s.owner + "/" + s.name + "/commit/" + c.sha,
			"author":       map[string]any{"name": c.author, "email": strings.ToLower(c.author) + "@example.org", "date": c.date},
			"tree":         map[string]any{"sha": c.tree},
			"verification": map[string]any{"verified": c.verified, "reason": map[bool]string{true: "valid", false: "unsigned"}[c.verified]},
		})
	case strings.HasPrefix(rest, "git/trees/"):
		files, found := s.trees[strings.TrimPrefix(rest, "git/trees/")]
		if !found {
			return fail(http.StatusNotFound, "Not Found")
		}
		var entries []map[string]any
		dirs := map[string]bool{}
		for _, p := range slices.Sorted(maps.Keys(files)) {
			parts := strings.Split(p, "/")
			for i := 1; i < len(parts); i++ {
				d := strings.Join(parts[:i], "/")
				if !dirs[d] {
					dirs[d] = true
					entries = append(entries, map[string]any{"path": d, "type": "tree", "mode": "040000", "sha": BlobSHA("tree:" + d)})
				}
			}
			entries = append(entries, map[string]any{"path": p, "type": "blob", "mode": "100644", "sha": BlobSHA(files[p]), "size": len(files[p])})
		}
		return ok200(map[string]any{"sha": strings.TrimPrefix(rest, "git/trees/"), "tree": entries, "truncated": false})
	case strings.HasPrefix(rest, "git/blobs/"):
		c, found := s.blobs[strings.TrimPrefix(rest, "git/blobs/")]
		if !found {
			return fail(http.StatusNotFound, "Not Found")
		}
		return ok200(map[string]any{"sha": BlobSHA(c), "size": len(c), "encoding": "base64",
			"content": base64.StdEncoding.EncodeToString([]byte(c))})
	}
	return fail(http.StatusNotFound, "Not Found")
}
