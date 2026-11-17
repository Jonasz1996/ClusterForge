// Package homeapps herkent de apps van het homelab (fase 36) uit de
// dienstenkaart van fase 35 (poorten, containers, units en wat er
// antwoordt per node) en maakt er tegels van voor de homepage-API v2.
// Herkende apps komen er vanzelf bij en verdwijnen vanzelf; de GUI dient
// alleen om bij te sturen.
package homeapps

import "strings"

// Entry is een app die ClusterForge herkent.
type Entry struct {
	Slug  string
	Name  string
	Group string
	// Icon is de naam in dashboard-icons (homepage), zoals plex.png.
	Icon string
	// Images: het laatste deel van de image-naam (zonder registry en tag)
	// begint met een van deze namen, zoals "pms-docker" of "plex".
	Images []string
	// Units: systemd-units (zonder .service).
	Units []string
	// Templates: een webdienst zonder eigen herkenning in een cluster van
	// dit template hoort bij deze app.
	Templates []string
	// Port is de hoofdpoort (voor de link); 0 = de laagste webpoort.
	Port int
	// Stats: welke app-meter de agent kan draaien (fase 36 M4).
	Stats string
}

// Groepen zoals ze op de homepage staan.
const (
	GroupMedia      = "Media"
	GroupNetwork    = "Netwerk"
	GroupSecurity   = "Beveiliging"
	GroupInfra      = "Infrastructuur"
	GroupMonitoring = "Monitoring"
	GroupHome       = "Smart home"
	GroupOffice     = "Productiviteit"
	GroupDownloads  = "Downloads"
	GroupOther      = "Overig"
)

// Catalog is de ingebouwde herkenning. De lijst groeit per versie; wat er
// niet in staat maar wel gepubliceerd is in NPM, wordt een gewone app.
var Catalog = []Entry{
	{Slug: "plex", Name: "Plex", Group: GroupMedia, Icon: "plex.png", Images: []string{"pms-docker", "plex"}, Units: []string{"plexmediaserver"}, Port: 32400, Stats: "plex"},
	{Slug: "jellyfin", Name: "Jellyfin", Group: GroupMedia, Icon: "jellyfin.png", Images: []string{"jellyfin"}, Units: []string{"jellyfin"}, Port: 8096, Stats: "jellyfin"},
	{Slug: "emby", Name: "Emby", Group: GroupMedia, Icon: "emby.png", Images: []string{"emby", "embyserver"}, Units: []string{"emby-server"}, Port: 8096},
	{Slug: "tautulli", Name: "Tautulli", Group: GroupMedia, Icon: "tautulli.png", Images: []string{"tautulli"}, Port: 8181},
	{Slug: "overseerr", Name: "Overseerr", Group: GroupMedia, Icon: "overseerr.png", Images: []string{"overseerr"}, Port: 5055},
	{Slug: "jellyseerr", Name: "Jellyseerr", Group: GroupMedia, Icon: "jellyseerr.png", Images: []string{"jellyseerr"}, Port: 5055},
	{Slug: "immich", Name: "Immich", Group: GroupMedia, Icon: "immich.png", Images: []string{"immich-server"}, Port: 2283},
	{Slug: "media-platform", Name: "Media-platform", Group: GroupMedia, Icon: "nextcloud.png", Units: []string{"cf-platform"}, Templates: []string{"media-platform"}},
	{Slug: "sonarr", Name: "Sonarr", Group: GroupDownloads, Icon: "sonarr.png", Images: []string{"sonarr"}, Units: []string{"sonarr"}, Port: 8989, Stats: "arr"},
	{Slug: "radarr", Name: "Radarr", Group: GroupDownloads, Icon: "radarr.png", Images: []string{"radarr"}, Units: []string{"radarr"}, Port: 7878, Stats: "arr"},
	{Slug: "lidarr", Name: "Lidarr", Group: GroupDownloads, Icon: "lidarr.png", Images: []string{"lidarr"}, Units: []string{"lidarr"}, Port: 8686, Stats: "arr"},
	{Slug: "readarr", Name: "Readarr", Group: GroupDownloads, Icon: "readarr.png", Images: []string{"readarr"}, Units: []string{"readarr"}, Port: 8787, Stats: "arr"},
	{Slug: "prowlarr", Name: "Prowlarr", Group: GroupDownloads, Icon: "prowlarr.png", Images: []string{"prowlarr"}, Units: []string{"prowlarr"}, Port: 9696, Stats: "arr"},
	{Slug: "bazarr", Name: "Bazarr", Group: GroupDownloads, Icon: "bazarr.png", Images: []string{"bazarr"}, Port: 6767},
	{Slug: "qbittorrent", Name: "qBittorrent", Group: GroupDownloads, Icon: "qbittorrent.png", Images: []string{"qbittorrent"}, Units: []string{"qbittorrent-nox"}, Port: 8080},
	{Slug: "sabnzbd", Name: "SABnzbd", Group: GroupDownloads, Icon: "sabnzbd.png", Images: []string{"sabnzbd"}, Units: []string{"sabnzbdplus"}, Port: 8080},
	{Slug: "transmission", Name: "Transmission", Group: GroupDownloads, Icon: "transmission.png", Images: []string{"transmission"}, Units: []string{"transmission-daemon"}, Port: 9091},
	{Slug: "nextcloud", Name: "Nextcloud", Group: GroupOffice, Icon: "nextcloud.png", Images: []string{"nextcloud"}, Stats: "nextcloud"},
	{Slug: "onlyoffice", Name: "OnlyOffice", Group: GroupOffice, Icon: "onlyoffice.png", Images: []string{"documentserver"}},
	{Slug: "paperless", Name: "Paperless-ngx", Group: GroupOffice, Icon: "paperless-ngx.png", Images: []string{"paperless-ngx"}, Port: 8000},
	{Slug: "vaultwarden", Name: "Vaultwarden", Group: GroupSecurity, Icon: "vaultwarden.png", Images: []string{"vaultwarden", "bitwarden_rs"}},
	{Slug: "vaultx", Name: "VaultX", Group: GroupSecurity, Icon: "vault.png", Images: []string{"vaultx"}, Templates: []string{"vaultx"}},
	{Slug: "authentik", Name: "Authentik", Group: GroupSecurity, Icon: "authentik.png", Images: []string{"authentik", "server"}, Templates: []string{"authentik"}, Port: 9000},
	{Slug: "crowdsec", Name: "CrowdSec", Group: GroupSecurity, Icon: "crowdsec.png", Images: []string{"crowdsec"}},
	{Slug: "npm", Name: "Nginx Proxy Manager", Group: GroupNetwork, Icon: "nginx-proxy-manager.png", Images: []string{"nginx-proxy-manager"}, Templates: []string{"nginx-proxy-manager"}, Port: 81},
	{Slug: "adguard", Name: "AdGuard Home", Group: GroupNetwork, Icon: "adguard-home.png", Images: []string{"adguardhome"}, Units: []string{"adguardhome", "AdGuardHome"}, Stats: "adguard"},
	{Slug: "pihole", Name: "Pi-hole", Group: GroupNetwork, Icon: "pi-hole.png", Images: []string{"pihole"}, Units: []string{"pihole-FTL"}},
	{Slug: "unifi", Name: "UniFi", Group: GroupNetwork, Icon: "unifi.png", Images: []string{"unifi-network-application", "unifi-controller", "unifi"}, Units: []string{"unifi"}, Port: 8443},
	{Slug: "homepage", Name: "Homepage", Group: GroupInfra, Icon: "homepage.png", Units: []string{"homepage-api"}, Templates: []string{"homepage"}},
	{Slug: "clusterforge", Name: "ClusterForge", Group: GroupInfra, Icon: "mdi-server-network", Images: []string{"clusterforge-server"}},
	{Slug: "proxmox", Name: "Proxmox VE", Group: GroupInfra, Icon: "proxmox.png", Units: []string{"pveproxy"}, Port: 8006},
	{Slug: "pbs", Name: "Proxmox Backup Server", Group: GroupInfra, Icon: "proxmox.png", Units: []string{"proxmox-backup-proxy"}, Port: 8007},
	{Slug: "portainer", Name: "Portainer", Group: GroupInfra, Icon: "portainer.png", Images: []string{"portainer", "portainer-ce", "portainer-ee"}, Port: 9443},
	{Slug: "gitea", Name: "Gitea", Group: GroupInfra, Icon: "gitea.png", Images: []string{"gitea"}, Units: []string{"gitea"}, Port: 3000},
	{Slug: "zabbix", Name: "Zabbix", Group: GroupMonitoring, Icon: "zabbix.png", Images: []string{"zabbix-web-nginx-pgsql", "zabbix-web-nginx-mysql", "zabbix-web-apache-pgsql", "zabbix-web-apache-mysql"}, Templates: []string{"zabbix"}},
	{Slug: "grafana", Name: "Grafana", Group: GroupMonitoring, Icon: "grafana.png", Images: []string{"grafana"}, Units: []string{"grafana-server"}, Port: 3000},
	{Slug: "uptime-kuma", Name: "Uptime Kuma", Group: GroupMonitoring, Icon: "uptime-kuma.png", Images: []string{"uptime-kuma"}, Port: 3001},
	{Slug: "prometheus", Name: "Prometheus", Group: GroupMonitoring, Icon: "prometheus.png", Images: []string{"prometheus"}, Units: []string{"prometheus"}, Port: 9090},
	{Slug: "home-assistant", Name: "Home Assistant", Group: GroupHome, Icon: "home-assistant.png", Images: []string{"home-assistant"}, Units: []string{"home-assistant"}, Templates: []string{"home-assistant"}, Port: 8123, Stats: "homeassistant"},
	{Slug: "zigbee2mqtt", Name: "Zigbee2MQTT", Group: GroupHome, Icon: "zigbee2mqtt.png", Images: []string{"zigbee2mqtt"}, Port: 8080},
	{Slug: "node-red", Name: "Node-RED", Group: GroupHome, Icon: "node-red.png", Images: []string{"node-red"}, Port: 1880},
}

// Lookup geeft de catalogusregel voor een slug.
func Lookup(slug string) (Entry, bool) {
	for _, e := range Catalog {
		if e.Slug == slug {
			return e, true
		}
	}
	return Entry{}, false
}

// imageName: ghcr.io/goauthentik/server:2025.10 → goauthentik/server.
func imageName(image string) (repo, last string) {
	image = strings.ToLower(strings.TrimSpace(image))
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image = image[:i]
	}
	parts := strings.Split(image, "/")
	if len(parts) > 1 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":") || parts[0] == "localhost") {
		parts = parts[1:]
	}
	repo = strings.Join(parts, "/")
	return repo, parts[len(parts)-1]
}

// ImageTag: de tag van een image, zoals 1.41.2 (leeg of latest = onbekend).
func ImageTag(image string) string {
	image = strings.TrimSpace(image)
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	i := strings.LastIndex(image, ":")
	if i < 0 || i < strings.LastIndex(image, "/") {
		return ""
	}
	t := image[i+1:]
	if t == "latest" {
		return ""
	}
	return t
}

// match zoekt de app bij een image, unit of containernaam. Een image als
// goauthentik/server telt alleen voor authentik als de repo "authentik"
// bevat: "server" alleen is te algemeen.
func match(image, unit, container string) (Entry, bool) {
	if image != "" {
		repo, last := imageName(image)
		for _, e := range Catalog {
			for _, m := range e.Images {
				if m == "server" {
					if last == "server" && strings.Contains(repo, e.Slug) {
						return e, true
					}
					continue
				}
				if last == m || strings.HasPrefix(last, m+"-") && !strings.HasSuffix(last, "-db") {
					return e, true
				}
			}
		}
	}
	if unit != "" {
		u := strings.TrimSuffix(unit, ".service")
		if i := strings.Index(u, "@"); i >= 0 {
			u = u[:i]
		}
		for _, e := range Catalog {
			for _, m := range e.Units {
				if u == m {
					return e, true
				}
			}
		}
	}
	if container != "" {
		c := strings.ToLower(container)
		for _, e := range Catalog {
			if c == e.Slug {
				return e, true
			}
		}
	}
	return Entry{}, false
}

func templateEntry(template string) (Entry, bool) {
	if template == "" {
		return Entry{}, false
	}
	for _, e := range Catalog {
		for _, t := range e.Templates {
			if t == template {
				return e, true
			}
		}
	}
	return Entry{}, false
}
