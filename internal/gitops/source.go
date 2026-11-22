// Package gitops leest clusters uit een Git-repository: per cluster een
// bestand clusters/<slug>/cluster.yaml met naam, omgeving, template,
// parameters en Proxmox-doel. Het valideert elk bestand, en maakt van een
// bestand dat afwijkt van de gewenste staat van zijn cluster een plan met
// per node wat er zou veranderen. Plannen en lezen veranderen niets op de
// nodes, en ClusterForge schrijft nooit naar Git.
package gitops

import (
	"context"
	"errors"
	"time"
)

// ErrNotModified betekent dat de kop van de branch niet veranderde sinds de
// ETag; GitHub rekent zo'n vraag niet aan.
var ErrNotModified = errors.New("niet gewijzigd")

// Commit is een commit zoals de UI hem toont.
type Commit struct {
	SHA      string    `json:"sha"`
	Message  string    `json:"message"`
	Author   string    `json:"author"`
	Date     time.Time `json:"date"`
	Verified bool      `json:"verified"`
	URL      string    `json:"url"`
	// Tree is de tree van de commit.
	Tree string `json:"tree,omitempty"`
}

// Title is de eerste regel van het commitbericht.
func (c Commit) Title() string {
	for i, r := range c.Message {
		if r == '\n' {
			return c.Message[:i]
		}
	}
	return c.Message
}

// TreeEntry is een bestand of map in een tree.
type TreeEntry struct {
	Path string
	// Type is blob of tree.
	Type string
	SHA  string
	Size int64
}

// Source leest een repository. GitHub is de enige nu; Gitea of Forgejo
// kunnen later met dezelfde vier vragen.
type Source interface {
	// Head geeft de commit aan de kop van de branch. Met een etag die nog
	// klopt, geeft hij ErrNotModified.
	Head(ctx context.Context, etag string) (sha, newETag string, err error)
	Commit(ctx context.Context, sha string) (Commit, error)
	// Tree geeft alle bestanden onder een tree, recursief.
	Tree(ctx context.Context, sha string) ([]TreeEntry, error)
	Blob(ctx context.Context, sha string) ([]byte, error)
}

// Config is wat een Source nodig heeft.
type Config struct {
	APIURL string
	Owner  string
	Name   string
	Branch string
	Token  string
}

// Grenzen van een scan.
const (
	MaxFiles    = 200
	MaxFileSize = 64 << 10
)
