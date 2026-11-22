// Package ghfake is een nep-GitHub voor tests en lokale ontwikkeling. Hij
// kent net genoeg van de REST-API om GitOps en de documentatie-export te
// bedienen: de kop van een branch met ETag, commits, trees en blobs, en om
// te schrijven blobs, trees, commits en refs, achter tokens met of zonder
// schrijfrecht.
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
	sha, tree, message, author string
	parents                    []string
	date                       time.Time
	verified                   bool
}

// Call is één vraag die binnenkwam, met het token dat meekwam en of dat
// mocht schrijven; zo kan een test nagaan wie wat vroeg.
type Call struct {
	Method string
	Path   string
	Status int
	Token  string
	Write  bool
}

// Server is één repository met een of meer branches.
type Server struct {
	mu    sync.Mutex
	owner string
	name  string
	// tokens: token naar schrijfrecht.
	tokens map[string]bool
	// branches: naam naar de sha van de kop. main is de standaardbranch.
	branches map[string]string
	commits  map[string]*commit
	// trees: tree-sha naar de bestanden (pad naar inhoud).
	trees map[string]map[string]string
	blobs map[string]string
	// requests zijn de paden die binnenkwamen, met de status.
	requests []string
	calls    []Call
	// Down laat elke vraag mislukken met 503.
	down bool
	// race is een commit die vlak voor de volgende update van zijn branch
	// landt, alsof iemand anders tegelijk pushte.
	race *race
}

type race struct {
	branch, message string
	files           map[string]string
}

// New maakt een lege repository owner/name. token mag lezen en schrijven.
func New(owner, name, token string) *Server {
	return &Server{
		owner: owner, name: name, tokens: map[string]bool{token: true}, branches: map[string]string{},
		commits: map[string]*commit{}, trees: map[string]map[string]string{}, blobs: map[string]string{},
	}
}

// SetToken vervangt alle tokens door dit ene, met schrijfrecht.
func (s *Server) SetToken(t string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = map[string]bool{t: true}
}

// AddToken laat nog een token toe; zonder write mag het alleen lezen, zoals
// een fine-grained token met Contents: read-only.
func (s *Server) AddToken(t string, write bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[t] = write
}

// SetDown laat elke vraag mislukken, of weer lukken.
func (s *Server) SetDown(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = down
}

// Files geeft de bestanden aan de kop van main.
func (s *Server) Files() map[string]string { return s.FilesOn("main") }

// FilesOn geeft de bestanden aan de kop van een branch.
func (s *Server) FilesOn(branch string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	head, ok := s.branches[branch]
	if !ok {
		return map[string]string{}
	}
	return maps.Clone(s.trees[s.commits[head].tree])
}

// Head geeft de kop van een branch, leeg als die niet bestaat.
func (s *Server) Head(branch string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.branches[branch]
}

// CommitInfo is wat een test van een commit wil weten.
type CommitInfo struct {
	SHA, Message, Author string
	Parents              []string
	Files                map[string]string
}

// CommitOf geeft een commit met zijn bestanden.
func (s *Server) CommitOf(sha string) (CommitInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.commits[sha]
	if !ok {
		return CommitInfo{}, false
	}
	return CommitInfo{SHA: c.sha, Message: c.message, Author: c.author, Parents: slices.Clone(c.parents),
		Files: maps.Clone(s.trees[c.tree])}, true
}

// Commit zet een commit op main. files zijn de bestanden die veranderen;
// een lege inhoud verwijdert het bestand. Het geeft de sha.
func (s *Server) Commit(message, author string, files map[string]string) string {
	return s.commit("main", message, author, false, files)
}

// CommitTo zet een commit op een branch, en maakt die als hij nog niet
// bestaat, met alleen deze bestanden.
func (s *Server) CommitTo(branch, message, author string, files map[string]string) string {
	return s.commit(branch, message, author, false, files)
}

// CommitVerified is Commit met een handtekening die GitHub geldig vindt.
func (s *Server) CommitVerified(message, author string, files map[string]string) string {
	return s.commit("main", message, author, true, files)
}

// Replace zet een commit op main met precies deze bestanden.
func (s *Server) Replace(message, author string, files map[string]string) string {
	s.mu.Lock()
	cur := map[string]string{}
	if head, ok := s.branches["main"]; ok {
		cur = s.trees[s.commits[head].tree]
	}
	change := map[string]string{}
	for p := range cur {
		change[p] = ""
	}
	maps.Copy(change, files)
	s.mu.Unlock()
	return s.commit("main", message, author, false, change)
}

// RaceOnce laat vlak voor de volgende update van branch een andere commit
// landen, zodat die update geen fast-forward meer is.
func (s *Server) RaceOnce(branch, message string, files map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.race = &race{branch: branch, message: message, files: files}
}

func (s *Server) commit(branch, message, author string, verified bool, files map[string]string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commitLocked(branch, message, author, verified, files)
}

func (s *Server) commitLocked(branch, message, author string, verified bool, files map[string]string) string {
	tree := map[string]string{}
	head, ok := s.branches[branch]
	if ok {
		maps.Copy(tree, s.trees[s.commits[head].tree])
	}
	for p, c := range files {
		if c == "" {
			delete(tree, p)
		} else {
			tree[p] = c
			s.blobs[BlobSHA(c)] = c
		}
	}
	var parents []string
	if ok {
		parents = []string{head}
	}
	c := s.newCommit(s.putTree(tree), parents, message, author, time.Now().UTC().Truncate(time.Second))
	c.verified = verified
	s.branches[branch] = c.sha
	return c.sha
}

func (s *Server) putTree(tree map[string]string) string {
	h := sha1.New()
	for _, p := range slices.Sorted(maps.Keys(tree)) {
		_, _ = fmt.Fprintf(h, "%s %s\n", p, BlobSHA(tree[p]))
	}
	sha := hex.EncodeToString(h.Sum(nil))
	s.trees[sha] = tree
	return sha
}

func (s *Server) newCommit(tree string, parents []string, message, author string, date time.Time) *commit {
	c := &commit{tree: tree, parents: parents, message: message, author: author, date: date}
	sum := sha1.Sum([]byte(fmt.Sprintf("tree %s\nparents %v\n%s\n%s\n%d", tree, parents, author, message, len(s.commits))))
	c.sha = hex.EncodeToString(sum[:])
	s.commits[c.sha] = c
	return c
}

// ancestor zegt of a gelijk is aan b of een voorouder ervan.
func (s *Server) ancestor(a, b string) bool {
	seen := map[string]bool{}
	todo := []string{b}
	for len(todo) > 0 {
		x := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if x == a {
			return true
		}
		if seen[x] {
			continue
		}
		seen[x] = true
		if c, ok := s.commits[x]; ok {
			todo = append(todo, c.parents...)
		}
	}
	return false
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

// Calls geeft de vragen die binnenkwamen, met of hun token mocht schrijven.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.serve(w, r)
	s.requests = append(s.requests, fmt.Sprintf("%s %s %d", r.Method, r.URL.RequestURI(), status))
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.calls = append(s.calls, Call{Method: r.Method, Path: r.URL.Path, Status: status, Token: token, Write: s.tokens[token]})
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) int {
	fail := func(status int, msg string) int {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
		return status
	}
	reply := func(status int, v any) int {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
		return status
	}
	if s.down {
		return fail(http.StatusServiceUnavailable, "Service Unavailable")
	}
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	write, known := s.tokens[token]
	if !known {
		return fail(http.StatusUnauthorized, "Bad credentials")
	}
	// Eigenaar en naam tellen zonder hoofdletters, zoals op GitHub.
	repo := "/repos/" + s.owner + "/" + s.name
	path := r.URL.Path
	if strings.EqualFold(path, repo) && r.Method == http.MethodGet {
		return reply(http.StatusOK, map[string]any{"full_name": s.owner + "/" + s.name, "default_branch": "main", "private": true})
	}
	if len(path) <= len(repo) || !strings.EqualFold(path[:len(repo)+1], repo+"/") {
		return fail(http.StatusNotFound, "Not Found")
	}
	rest := path[len(repo)+1:]
	if r.Method != http.MethodGet {
		if !write {
			return fail(http.StatusForbidden, "Resource not accessible by personal access token")
		}
		if len(s.branches) == 0 {
			return fail(http.StatusConflict, "Git Repository is empty.")
		}
		return s.write(w, r, rest, fail, reply)
	}
	switch {
	case strings.HasPrefix(rest, "commits/"):
		head, found := s.branches[strings.TrimPrefix(rest, "commits/")]
		if len(s.branches) == 0 {
			return fail(http.StatusConflict, "Git Repository is empty.")
		}
		if !found {
			return fail(http.StatusNotFound, "No commit found for SHA")
		}
		etag := `"` + head + `"`
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return http.StatusNotModified
		}
		if r.Header.Get("Accept") == "application/vnd.github.sha" {
			_, _ = w.Write([]byte(head))
			return http.StatusOK
		}
		return reply(http.StatusOK, map[string]any{"sha": head})
	case strings.HasPrefix(rest, "git/ref/heads/"):
		if len(s.branches) == 0 {
			return fail(http.StatusConflict, "Git Repository is empty.")
		}
		branch := strings.TrimPrefix(rest, "git/ref/heads/")
		head, found := s.branches[branch]
		if !found {
			return fail(http.StatusNotFound, "Not Found")
		}
		return reply(http.StatusOK, map[string]any{"ref": "refs/heads/" + branch, "object": map[string]any{"sha": head, "type": "commit"}})
	case strings.HasPrefix(rest, "git/commits/"):
		c, found := s.commits[strings.TrimPrefix(rest, "git/commits/")]
		if !found {
			return fail(http.StatusNotFound, "Not Found")
		}
		return reply(http.StatusOK, s.commitJSON(r, c))
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
		return reply(http.StatusOK, map[string]any{"sha": strings.TrimPrefix(rest, "git/trees/"), "tree": entries, "truncated": false})
	case strings.HasPrefix(rest, "git/blobs/"):
		c, found := s.blobs[strings.TrimPrefix(rest, "git/blobs/")]
		if !found {
			return fail(http.StatusNotFound, "Not Found")
		}
		return reply(http.StatusOK, map[string]any{"sha": BlobSHA(c), "size": len(c), "encoding": "base64",
			"content": base64.StdEncoding.EncodeToString([]byte(c))})
	}
	return fail(http.StatusNotFound, "Not Found")
}

func (s *Server) commitJSON(r *http.Request, c *commit) map[string]any {
	parents := []map[string]any{}
	for _, p := range c.parents {
		parents = append(parents, map[string]any{"sha": p})
	}
	return map[string]any{
		"sha": c.sha, "message": c.message,
		"html_url":     "http://" + r.Host + "/" + s.owner + "/" + s.name + "/commit/" + c.sha,
		"author":       map[string]any{"name": c.author, "email": strings.ToLower(c.author) + "@example.org", "date": c.date},
		"tree":         map[string]any{"sha": c.tree},
		"parents":      parents,
		"verification": map[string]any{"verified": c.verified, "reason": map[bool]string{true: "valid", false: "unsigned"}[c.verified]},
	}
}

// write bedient de schrijvende endpoints van de Git Data API.
func (s *Server) write(w http.ResponseWriter, r *http.Request, rest string, fail func(int, string) int, reply func(int, any) int) int {
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return fail(http.StatusBadRequest, "Problems parsing JSON")
	}
	str := func(k string) string {
		var v string
		_ = json.Unmarshal(body[k], &v)
		return v
	}
	switch {
	case r.Method == http.MethodPost && rest == "git/blobs":
		content := str("content")
		if str("encoding") == "base64" {
			b, err := base64.StdEncoding.DecodeString(content)
			if err != nil {
				return fail(http.StatusUnprocessableEntity, "Invalid base64")
			}
			content = string(b)
		}
		s.blobs[BlobSHA(content)] = content
		return reply(http.StatusCreated, map[string]any{"sha": BlobSHA(content)})
	case r.Method == http.MethodPost && rest == "git/trees":
		tree := map[string]string{}
		if base := str("base_tree"); base != "" {
			files, ok := s.trees[base]
			if !ok {
				return fail(http.StatusUnprocessableEntity, "base_tree is not a valid tree")
			}
			maps.Copy(tree, files)
		}
		var entries []struct {
			Path string  `json:"path"`
			Mode string  `json:"mode"`
			Type string  `json:"type"`
			SHA  *string `json:"sha"`
		}
		if err := json.Unmarshal(body["tree"], &entries); err != nil {
			return fail(http.StatusUnprocessableEntity, "Invalid tree")
		}
		for _, e := range entries {
			if e.Type != "blob" || e.Mode != "100644" || e.Path == "" || strings.HasPrefix(e.Path, "/") {
				return fail(http.StatusUnprocessableEntity, "Invalid tree entry")
			}
			if e.SHA == nil {
				delete(tree, e.Path)
				continue
			}
			c, ok := s.blobs[*e.SHA]
			if !ok {
				return fail(http.StatusUnprocessableEntity, "Invalid sha for tree entry")
			}
			tree[e.Path] = c
		}
		return reply(http.StatusCreated, map[string]any{"sha": s.putTree(tree)})
	case r.Method == http.MethodPost && rest == "git/commits":
		tree := str("tree")
		if _, ok := s.trees[tree]; !ok {
			return fail(http.StatusUnprocessableEntity, "Tree SHA does not exist")
		}
		var parents []string
		_ = json.Unmarshal(body["parents"], &parents)
		for _, p := range parents {
			if _, ok := s.commits[p]; !ok {
				return fail(http.StatusUnprocessableEntity, "Parent SHA does not exist or is not a commit object")
			}
		}
		var author struct {
			Name string    `json:"name"`
			Date time.Time `json:"date"`
		}
		_ = json.Unmarshal(body["author"], &author)
		if author.Name == "" {
			author.Name = "token"
		}
		if author.Date.IsZero() {
			author.Date = time.Now().UTC().Truncate(time.Second)
		}
		c := s.newCommit(tree, parents, str("message"), author.Name, author.Date)
		return reply(http.StatusCreated, s.commitJSON(r, c))
	case r.Method == http.MethodPatch && strings.HasPrefix(rest, "git/refs/heads/"):
		branch := strings.TrimPrefix(rest, "git/refs/heads/")
		if _, ok := s.branches[branch]; !ok {
			return fail(http.StatusUnprocessableEntity, "Reference does not exist")
		}
		if s.race != nil && s.race.branch == branch {
			s.commitLocked(branch, s.race.message, "Iemand anders", false, s.race.files)
			s.race = nil
		}
		sha := str("sha")
		if _, ok := s.commits[sha]; !ok {
			return fail(http.StatusUnprocessableEntity, "Object does not exist")
		}
		var force bool
		_ = json.Unmarshal(body["force"], &force)
		if !force && !s.ancestor(s.branches[branch], sha) {
			return fail(http.StatusUnprocessableEntity, "Update is not a fast forward")
		}
		s.branches[branch] = sha
		return reply(http.StatusOK, map[string]any{"ref": "refs/heads/" + branch, "object": map[string]any{"sha": sha, "type": "commit"}})
	case r.Method == http.MethodPost && rest == "git/refs":
		branch, ok := strings.CutPrefix(str("ref"), "refs/heads/")
		if !ok || branch == "" {
			return fail(http.StatusUnprocessableEntity, "Reference name is invalid")
		}
		if _, exists := s.branches[branch]; exists {
			return fail(http.StatusUnprocessableEntity, "Reference already exists")
		}
		sha := str("sha")
		if _, ok := s.commits[sha]; !ok {
			return fail(http.StatusUnprocessableEntity, "Object does not exist")
		}
		s.branches[branch] = sha
		return reply(http.StatusCreated, map[string]any{"ref": "refs/heads/" + branch, "object": map[string]any{"sha": sha, "type": "commit"}})
	}
	return fail(http.StatusNotFound, "Not Found")
}
