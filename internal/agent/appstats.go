package agent

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// App-cijfers voor de homepage-API v2 (fase 36). De agent leest de sleutel
// van de app zelf op de node en stuurt alleen de cijfers door.

const maxKeyFile = 1 << 20

var errNoKey = errors.New("sleutel van de app niet gevonden op de node")

func (a *Agent) appStats(ctx context.Context, req *protocol.WebAppStats) (*protocol.AppStatsResult, error) {
	dirs, err := a.appConfigDirs(ctx, req)
	if err != nil {
		return nil, err
	}
	switch req.Kind {
	case protocol.AppStatsPlex:
		token, err := readKey(dirs, []string{
			"Library/Application Support/Plex Media Server/Preferences.xml",
			"Plex Media Server/Preferences.xml",
			"Preferences.xml",
		}, plexToken)
		if err != nil {
			return nil, err
		}
		return a.plexStats(ctx, req, token)
	case protocol.AppStatsArr:
		key, err := readKey(dirs, []string{"config.xml"}, arrKey)
		if err != nil {
			return nil, err
		}
		return a.arrStats(ctx, req, key)
	}
	return nil, fmt.Errorf("onbekende app-meter %q", req.Kind)
}

// appConfigDirs: de map die op /config van de container hangt, of de
// gewone mappen van een app die rechtstreeks op de node draait.
func (a *Agent) appConfigDirs(ctx context.Context, req *protocol.WebAppStats) ([]string, error) {
	if req.Container != "" {
		d := a.docker()
		if !d.present() {
			return nil, errors.New("geen Docker op deze node")
		}
		_, body, err := d.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(req.Container)+"/json", nil, nil, protocol.MaxDockerResponse)
		if err != nil {
			return nil, err
		}
		var in struct {
			Mounts []struct {
				Source      string `json:"Source"`
				Destination string `json:"Destination"`
			} `json:"Mounts"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			return nil, err
		}
		var out []string
		for _, m := range in.Mounts {
			if m.Destination == "/config" || strings.HasPrefix(m.Destination, "/config/") {
				if p, err := a.path(filepath.Clean(m.Source)); err == nil {
					out = append(out, p)
				}
			}
		}
		if len(out) == 0 {
			return nil, errors.New("de container heeft geen map op /config")
		}
		return out, nil
	}
	var out []string
	switch req.Kind {
	case protocol.AppStatsPlex:
		out = []string{"/var/lib/plexmediaserver"}
	case protocol.AppStatsArr:
		for _, n := range []string{"sonarr", "radarr", "lidarr", "readarr", "prowlarr", "Sonarr", "Radarr", "Lidarr", "Readarr", "Prowlarr"} {
			out = append(out, "/var/lib/"+n, "/home/"+strings.ToLower(n)+"/.config/"+n, "/root/.config/"+n)
		}
	}
	var paths []string
	for _, d := range out {
		if p, err := a.path(d); err == nil {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// readKey zoekt het eerste bestand dat bestaat en leest er de sleutel uit.
// Nooit via een symlink en hoogstens 1 MB.
func readKey(dirs, names []string, parse func([]byte) string) (string, error) {
	for _, d := range dirs {
		for _, n := range names {
			f, err := os.OpenFile(filepath.Join(d, n), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			if err != nil {
				continue
			}
			data, err := io.ReadAll(io.LimitReader(f, maxKeyFile))
			_ = f.Close()
			if err != nil {
				continue
			}
			if k := parse(data); k != "" {
				return k, nil
			}
		}
	}
	return "", errNoKey
}

func plexToken(data []byte) string {
	var p struct {
		Token string `xml:"PlexOnlineToken,attr"`
	}
	if xml.Unmarshal(data, &p) != nil {
		return ""
	}
	return strings.TrimSpace(p.Token)
}

func arrKey(data []byte) string {
	var c struct {
		Key     string `xml:"ApiKey"`
		URLBase string `xml:"UrlBase"`
	}
	if xml.Unmarshal(data, &c) != nil {
		return ""
	}
	return strings.TrimSpace(c.Key)
}

func (a *Agent) appGet(ctx context.Context, req *protocol.WebAppStats, path string, headers map[string]string, out any) error {
	status, body, err := localHTTP(ctx, a.webDial, &protocol.WebHTTP{Method: http.MethodGet, Port: req.Port, Path: path, Headers: headers, HTTPS: req.HTTPS})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%s antwoordt %d", strings.SplitN(path, "?", 2)[0], status)
	}
	return json.Unmarshal(body, out)
}

func (a *Agent) plexStats(ctx context.Context, req *protocol.WebAppStats, token string) (*protocol.AppStatsResult, error) {
	h := map[string]string{"X-Plex-Token": token}
	v := map[string]any{}
	var id struct {
		MC struct {
			Version string `json:"version"`
		} `json:"MediaContainer"`
	}
	if err := a.appGet(ctx, req, "/identity", h, &id); err == nil && id.MC.Version != "" {
		v["version"] = strings.SplitN(id.MC.Version, "-", 2)[0]
	}
	var sess struct {
		MC struct {
			Size     int `json:"size"`
			Metadata []struct {
				Transcode *struct{} `json:"TranscodeSession"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := a.appGet(ctx, req, "/status/sessions", h, &sess); err != nil {
		return nil, err
	}
	tc := 0
	for _, m := range sess.MC.Metadata {
		if m.Transcode != nil {
			tc++
		}
	}
	v["streams"], v["transcodes"] = sess.MC.Size, tc
	var libs struct {
		MC struct {
			Directory []struct {
				Type string `json:"type"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := a.appGet(ctx, req, "/library/sections", h, &libs); err == nil {
		v["libraries"] = len(libs.MC.Directory)
		for _, d := range libs.MC.Directory {
			k := map[string]string{"movie": "libraries_movies", "show": "libraries_shows", "artist": "libraries_music", "photo": "libraries_photos"}[d.Type]
			if k != "" {
				n, _ := v[k].(int)
				v[k] = n + 1
			}
		}
	}
	return &protocol.AppStatsResult{Values: v}, nil
}

func (a *Agent) arrStats(ctx context.Context, req *protocol.WebAppStats, key string) (*protocol.AppStatsResult, error) {
	h := map[string]string{"X-Api-Key": key}
	var st struct {
		Version string `json:"version"`
		AppName string `json:"appName"`
	}
	api := ""
	for _, p := range []string{"/api/v3", "/api/v1"} {
		if err := a.appGet(ctx, req, p+"/system/status", h, &st); err == nil {
			api = p
			break
		}
	}
	if api == "" {
		return nil, errors.New("de API van de app antwoordt niet (v3 of v1)")
	}
	v := map[string]any{"version": st.Version}
	var health []struct {
		Type string `json:"type"`
	}
	if a.appGet(ctx, req, api+"/health", h, &health) == nil {
		issues := 0
		for _, x := range health {
			if x.Type == "warning" || x.Type == "error" {
				issues++
			}
		}
		v["health_issues"] = issues
	}
	var page struct {
		Total int `json:"totalRecords"`
	}
	if strings.EqualFold(st.AppName, "prowlarr") {
		var idx []json.RawMessage
		if a.appGet(ctx, req, api+"/indexer", h, &idx) == nil {
			v["indexers"] = len(idx)
		}
		return &protocol.AppStatsResult{Values: v}, nil
	}
	if a.appGet(ctx, req, api+"/queue?pageSize=1", h, &page) == nil {
		v["queue"] = page.Total
	}
	page.Total = 0
	if a.appGet(ctx, req, api+"/wanted/missing?pageSize=1", h, &page) == nil {
		v["missing"] = page.Total
	}
	return &protocol.AppStatsResult{Values: v}, nil
}
