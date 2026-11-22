// Package crowdsec beheert CrowdSec op de nodes (fase 14): welke software
// er draait, welke profielen (hub-collections en logbronnen) daarbij horen,
// het script dat ze toepast, en de alerts en bans die de agents melden.
package crowdsec

import (
	"path"
	"slices"
	"strings"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// Signals is wat ClusterForge over een node weet om software te herkennen.
type Signals struct {
	// Units zijn de systemd-units (zonder .service) die actief of enabled
	// zijn.
	Units map[string]bool
	// Containers zijn de draaiende Docker-containers.
	Containers []protocol.CrowdSecContainer
	// Probes zegt per glob welke bestanden bestaan.
	Probes map[string][]string
	// Template is het template van het cluster, zoals nginx-proxy-manager.
	Template string
	// Docker is true als Docker op de node draait.
	Docker bool
}

func (s Signals) unit(names ...string) string {
	for _, n := range names {
		if strings.HasSuffix(n, "*") {
			for u := range s.Units {
				if strings.HasPrefix(u, strings.TrimSuffix(n, "*")) && s.Units[u] {
					return u
				}
			}
			continue
		}
		if s.Units[n] {
			return n
		}
	}
	return ""
}

// imageRepo is het image zonder registry-tag: ghcr.io/goauthentik/server:2025.6
// → ghcr.io/goauthentik/server.
func imageRepo(image string) string {
	if i := strings.LastIndexByte(image, '@'); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndexByte(image, ':'); i > strings.LastIndexByte(image, '/') {
		image = image[:i]
	}
	return image
}

// containers geeft de containers waarvan het image (zonder tag) eindigt op
// een van de namen, zoals goauthentik/server of traefik.
func (s Signals) containers(images ...string) []protocol.CrowdSecContainer {
	var res []protocol.CrowdSecContainer
	for _, c := range s.Containers {
		repo := imageRepo(c.Image)
		for _, im := range images {
			if repo == im || strings.HasSuffix(repo, "/"+im) {
				res = append(res, c)
				break
			}
		}
	}
	return res
}

func (s Signals) probe(glob string) []string { return s.Probes[glob] }

// Source is één bron van de acquisitie.
type Source struct {
	// Kind is file, journalctl of docker.
	Kind       string
	Files      []string
	Journal    []string
	Containers []string
	// Type is het label type: het programma waarop de parsers filteren.
	Type string
}

// Match is een herkend profiel op een node.
type Match struct {
	// Reason zegt waaraan het herkend is, zoals "unit nginx".
	Reason  string
	Sources []Source
	// Note is een beperking, zoals logs die de parser niet herkent.
	Note string
}

// Profile is software met de collections en logbronnen voor CrowdSec.
type Profile struct {
	Key         string
	Name        string
	Description string
	Collections []string
	// Community is true als de collection niet van CrowdSec zelf is.
	Community bool
	// Probes zijn de globs die de server bij status meestuurt.
	Probes []string
	// Detect herkent het profiel; nil als het niet herkend is.
	Detect func(s Signals) *Match
}

func fileSource(typ string, files ...string) Source {
	return Source{Kind: "file", Files: files, Type: typ}
}

func journalSource(typ string, units ...string) Source {
	f := make([]string, len(units))
	for i, u := range units {
		f[i] = "_SYSTEMD_UNIT=" + u + ".service"
	}
	return Source{Kind: "journalctl", Journal: f, Type: typ}
}

func dockerSource(typ string, cs []protocol.CrowdSecContainer) Source {
	names := make([]string, len(cs))
	for i, c := range cs {
		names[i] = c.Name
	}
	slices.Sort(names)
	return Source{Kind: "docker", Containers: names, Type: typ}
}

// mountPath is de map op de host achter dir in de container, of fallback.
func mountPath(cs []protocol.CrowdSecContainer, dir, fallback string) string {
	for _, c := range cs {
		if src, ok := c.Mounts[dir]; ok {
			return src
		}
	}
	return fallback
}

func names(cs []protocol.CrowdSecContainer) string {
	n := make([]string, len(cs))
	for i, c := range cs {
		n[i] = c.Name
	}
	slices.Sort(n)
	return strings.Join(n, ", ")
}

// unitOrImage is de gewone herkenning: een systemd-unit met logs in
// bestanden of journald, of een container met zijn eigen log.
func unitOrImage(typ string, units []string, unitSource func(u string) Source, images ...string) func(Signals) *Match {
	return func(s Signals) *Match {
		if u := s.unit(units...); u != "" {
			return &Match{Reason: "unit " + u, Sources: []Source{unitSource(u)}}
		}
		if cs := s.containers(images...); len(cs) > 0 {
			return &Match{Reason: "container " + names(cs), Sources: []Source{dockerSource(typ, cs)}}
		}
		return nil
	}
}

const (
	authLog     = "/var/log/auth.log"
	nextcloudA  = "/var/www/*/data/nextcloud.log"
	nextcloudB  = "/var/www/nextcloud/data/nextcloud.log"
	nextcloudC  = "/srv/nextcloud/data/nextcloud.log"
	haproxyLog  = "/var/log/haproxy.log"
	grafanaLog  = "/var/log/grafana/grafana.log"
	mysqlErrLog = "/var/log/mysql/error.log"
)

// Catalog zijn alle profielen, in de volgorde van de GUI.
var Catalog = []Profile{
	{
		Key: "linux", Name: "Linux + SSH", Description: "Altijd: SSH-brute force en de basis (syslog, GeoIP, goede bots).",
		Collections: []string{"crowdsecurity/linux"},
		Probes:      []string{authLog},
		Detect: func(s Signals) *Match {
			if len(s.probe(authLog)) > 0 {
				return &Match{Reason: "altijd", Sources: []Source{fileSource("syslog", authLog, "/var/log/syslog")}}
			}
			// Debian 13 zonder rsyslog: alleen journald.
			return &Match{Reason: "altijd", Sources: []Source{journalSource("syslog", "ssh", "sshd")}}
		},
	},
	{
		Key: "nginx", Name: "nginx", Description: "Webserver: scans, bekende CVE's, brute force op HTTP.",
		Collections: []string{"crowdsecurity/nginx"},
		Detect: unitOrImage("nginx", []string{"nginx"}, func(string) Source {
			return fileSource("nginx", "/var/log/nginx/*.log")
		}, "nginx"),
	},
	{
		Key: "apache2", Name: "Apache", Description: "Webserver (ook de Zabbix-frontend en Nextcloud op Apache).",
		Collections: []string{"crowdsecurity/apache2"},
		Detect: unitOrImage("apache2", []string{"apache2"}, func(string) Source {
			return fileSource("apache2", "/var/log/apache2/*.log")
		}, "httpd"),
	},
	{
		Key: "haproxy", Name: "HAProxy", Description: "Load balancer: HTTP-scans en -aanvallen via HAProxy.",
		Collections: []string{"crowdsecurity/haproxy"},
		Probes:      []string{haproxyLog},
		Detect: func(s Signals) *Match {
			u := s.unit("haproxy")
			if u == "" {
				return nil
			}
			if len(s.probe(haproxyLog)) > 0 {
				return &Match{Reason: "unit haproxy", Sources: []Source{fileSource("syslog", haproxyLog)}}
			}
			return &Match{Reason: "unit haproxy", Sources: []Source{journalSource("syslog", "haproxy")}}
		},
	},
	{
		Key: "nginx-proxy-manager", Name: "Nginx Proxy Manager", Description: "Proxy-hosts van NPM: scans, CVE's, brute force.",
		Collections: []string{"crowdsecurity/nginx-proxy-manager"},
		Detect: func(s Signals) *Match {
			cs := s.containers("jc21/nginx-proxy-manager")
			switch {
			case len(cs) > 0:
				dir := mountPath(cs, "/data", "/opt/cf-npm/data")
				return &Match{Reason: "container " + names(cs), Sources: []Source{fileSource("nginx-proxy-manager", dir+"/logs/*.log")}}
			case s.Template == "nginx-proxy-manager":
				// NPM draait alleen op de node met het VIP; de logs staan er
				// ook als het hier even stilstaat.
				return &Match{Reason: "template nginx-proxy-manager", Sources: []Source{fileSource("nginx-proxy-manager", "/opt/cf-npm/data/logs/*.log")}}
			}
			return nil
		},
	},
	{
		Key: "traefik", Name: "Traefik", Description: "Reverse proxy: scans, CVE's, brute force.",
		Collections: []string{"crowdsecurity/traefik"},
		Detect: unitOrImage("traefik", []string{"traefik"}, func(u string) Source {
			return journalSource("traefik", u)
		}, "traefik"),
	},
	{
		Key: "caddy", Name: "Caddy", Description: "Webserver: scans, CVE's, brute force.",
		Collections: []string{"crowdsecurity/caddy"},
		Detect: unitOrImage("caddy", []string{"caddy"}, func(u string) Source {
			return journalSource("caddy", u)
		}, "caddy"),
	},
	{
		Key: "mariadb", Name: "MariaDB / Galera", Description: "Brute force op de databaselogin.",
		Collections: []string{"crowdsecurity/mariadb"},
		Detect: func(s Signals) *Match {
			if u := s.unit("mariadb"); u != "" {
				return &Match{Reason: "unit mariadb", Sources: []Source{journalSource("syslog", "mariadb")}}
			}
			if cs := s.containers("mariadb"); len(cs) > 0 {
				return &Match{Reason: "container " + names(cs), Sources: []Source{dockerSource("mariadb", cs)}}
			}
			return nil
		},
	},
	{
		Key: "mysql", Name: "MySQL", Description: "Brute force op de databaselogin.",
		Collections: []string{"crowdsecurity/mysql"},
		Probes:      []string{mysqlErrLog},
		Detect: func(s Signals) *Match {
			if s.unit("mysql") != "" && s.unit("mariadb") == "" {
				return &Match{Reason: "unit mysql", Sources: []Source{fileSource("mysql", mysqlErrLog)}}
			}
			return nil
		},
	},
	{
		Key: "postgresql", Name: "PostgreSQL", Description: "Brute force op de databaselogin.",
		Collections: []string{"crowdsecurity/pgsql"},
		Detect: func(s Signals) *Match {
			if s.unit("patroni") != "" {
				return &Match{Reason: "unit patroni", Note: "PostgreSQL onder Patroni logt via Patroni; die regels herkent de pgsql-parser niet. Alleen de collection staat erop."}
			}
			if u := s.unit("postgresql@*"); u != "" {
				return &Match{Reason: "unit " + u, Sources: []Source{fileSource("postgres", "/var/log/postgresql/*.log")}}
			}
			if cs := s.containers("postgres"); len(cs) > 0 {
				return &Match{Reason: "container " + names(cs), Sources: []Source{dockerSource("postgres", cs)}}
			}
			return nil
		},
	},
	{
		Key: "authentik", Name: "Authentik", Description: "Brute force op de Authentik-login.",
		Collections: []string{"firix/authentik"}, Community: true,
		Detect: func(s Signals) *Match {
			if cs := s.containers("goauthentik/server"); len(cs) > 0 {
				return &Match{Reason: "container " + names(cs), Sources: []Source{dockerSource("authentik", cs)}}
			}
			if s.Template == "authentik" {
				return &Match{Reason: "template authentik", Sources: []Source{{Kind: "docker", Containers: []string{"cf-authentik-server", "cf-authentik-worker"}, Type: "authentik"}}}
			}
			return nil
		},
	},
	{
		Key: "home-assistant", Name: "Home Assistant", Description: "Mislukte logins op Home Assistant.",
		Collections: []string{"crowdsecurity/home-assistant"},
		Detect: func(s Signals) *Match {
			cs := s.containers("home-assistant/home-assistant", "homeassistant/home-assistant", "home-assistant")
			switch {
			case len(cs) > 0:
				dir := mountPath(cs, "/config", "/opt/cf-homeassistant/config")
				return &Match{Reason: "container " + names(cs), Sources: []Source{fileSource("home-assistant", dir+"/home-assistant.log")}}
			case s.Template == "home-assistant":
				return &Match{Reason: "template home-assistant", Sources: []Source{fileSource("home-assistant", "/opt/cf-homeassistant/config/home-assistant.log")}}
			}
			return nil
		},
	},
	{
		Key: "nextcloud", Name: "Nextcloud", Description: "Brute force op de Nextcloud-login.",
		Collections: []string{"crowdsecurity/nextcloud"},
		Probes:      []string{nextcloudA, nextcloudB, nextcloudC},
		Detect: func(s Signals) *Match {
			var files []string
			for _, g := range []string{nextcloudA, nextcloudB, nextcloudC} {
				files = append(files, s.probe(g)...)
			}
			if cs := s.containers("nextcloud"); len(cs) > 0 {
				if dir := mountPath(cs, "/var/www/html", ""); dir != "" {
					files = append(files, dir+"/data/nextcloud.log")
				}
			}
			slices.Sort(files)
			files = slices.Compact(files)
			if len(files) == 0 {
				return nil
			}
			return &Match{Reason: "bestand " + path.Base(path.Dir(path.Dir(files[0]))) + "/data/nextcloud.log", Sources: []Source{fileSource("Nextcloud", files...)}}
		},
	},
	{
		Key: "grafana", Name: "Grafana", Description: "Brute force op de Grafana-login.",
		Collections: []string{"LePresidente/grafana"}, Community: true,
		Detect: unitOrImage("grafana", []string{"grafana-server"}, func(string) Source {
			return fileSource("grafana", grafanaLog)
		}, "grafana/grafana", "grafana/grafana-oss", "grafana/grafana-enterprise"),
	},
	{
		Key: "gitea", Name: "Gitea", Description: "Brute force op de Gitea-login.",
		Collections: []string{"LePresidente/gitea"}, Community: true,
		Detect: unitOrImage("gitea", []string{"gitea"}, func(u string) Source { return journalSource("gitea", u) }, "gitea/gitea"),
	},
	{
		Key: "vaultwarden", Name: "Vaultwarden", Description: "Brute force op de Vaultwarden-login.",
		Collections: []string{"Dominic-Wagner/vaultwarden"}, Community: true,
		Detect: unitOrImage("Vaultwarden", nil, nil, "vaultwarden/server"),
	},
	{
		Key: "jellyfin", Name: "Jellyfin", Description: "Brute force op de Jellyfin-login.",
		Collections: []string{"LePresidente/jellyfin"}, Community: true,
		Detect: unitOrImage("jellyfin", []string{"jellyfin"}, func(u string) Source { return journalSource("jellyfin", u) }, "jellyfin/jellyfin"),
	},
	{
		Key: "immich", Name: "Immich", Description: "Brute force op de Immich-login.",
		Collections: []string{"gauth-fr/immich"}, Community: true,
		Detect: unitOrImage("immich", nil, nil, "immich-app/immich-server"),
	},
	{
		Key: "plex", Name: "Plex", Description: "Allowlist van Plex' eigen adressen, zodat Plex nooit geband wordt.",
		Collections: []string{"crowdsecurity/plex"},
		Detect: func(s Signals) *Match {
			if u := s.unit("plexmediaserver"); u != "" {
				return &Match{Reason: "unit " + u}
			}
			if cs := s.containers("plexinc/pms-docker", "linuxserver/plex"); len(cs) > 0 {
				return &Match{Reason: "container " + names(cs)}
			}
			return nil
		},
	},
	{
		Key: "proxmox", Name: "Proxmox VE", Description: "Brute force op de Proxmox-webinterface.",
		Collections: []string{"fulljackz/proxmox"}, Community: true,
		Detect: func(s Signals) *Match {
			if s.unit("pvedaemon") != "" {
				return &Match{Reason: "unit pvedaemon", Sources: []Source{journalSource("syslog", "pvedaemon")}}
			}
			return nil
		},
	},
}

// ProfileByKey zoekt een profiel in de catalogus.
func ProfileByKey(key string) (Profile, bool) {
	for _, p := range Catalog {
		if p.Key == key {
			return p, true
		}
	}
	return Profile{}, false
}

// AllProbes zijn de globs die de server bij status meestuurt.
func AllProbes() []string {
	var res []string
	for _, p := range Catalog {
		res = append(res, p.Probes...)
	}
	slices.Sort(res)
	return slices.Compact(res)
}
