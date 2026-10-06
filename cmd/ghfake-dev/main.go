package main

// ghfake-dev draait een nep-GitHub voor handwerk tegen de dev-server. Hij
// zet de bestanden uit een map in een repository en commit opnieuw zodra
// er in die map iets verandert; zo test je GitOps zonder echte GitHub.
//
//	make dev-github DIR=/tmp/cf-config
//
// Koppel daarna bij GitOps met API-adres http://127.0.0.1:8098, repository
// jonas/cf-config en token dev-token.
import (
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/gitops/ghfake"
)

func main() {
	addr := flag.String("listen", "127.0.0.1:8098", "adres van de nep-GitHub")
	dir := flag.String("dir", "", "map met de inhoud van de repository")
	token := flag.String("token", "dev-token", "het token dat de nep-GitHub aanneemt")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "geef met -dir de map met de inhoud van de repository")
		os.Exit(2)
	}
	gh := ghfake.New("jonas", "cf-config", *token)
	var last map[string]string
	scan := func() {
		files := map[string]string{}
		err := filepath.WalkDir(*dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(*dir, p)
			files[filepath.ToSlash(rel)] = string(b)
			return nil
		})
		if err != nil {
			fmt.Println("map lezen:", err)
			return
		}
		if last != nil && maps.Equal(files, last) {
			return
		}
		msg := "Begin"
		if last != nil {
			msg = "Wijziging van " + time.Now().Format("15:04:05")
		}
		sha := gh.Replace(msg, "Jonas", files)
		fmt.Printf("commit %s %q met %d bestanden\n", sha[:7], msg, len(files))
		last = files
	}
	scan()
	go func() {
		for range time.Tick(2 * time.Second) {
			scan()
		}
	}()
	fmt.Printf("nep-GitHub op http://%s: repository jonas/cf-config, branch main, token %s\n", *addr, *token)
	srv := &http.Server{Addr: *addr, Handler: gh, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
