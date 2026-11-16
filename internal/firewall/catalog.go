// Package firewall beheert ufw op de nodes (fase 16): de catalogus met
// software en poorten, het plan per node, de taak firewall.apply, de poller
// en de monitoring van verkeer naar dichte poorten.
package firewall

import (
	"slices"
	"strings"
)

// Bronklassen: voor wie een poort open gaat.
const (
	ClassAny        = "any"
	ClassLAN        = "lan"
	ClassCluster    = "cluster"
	ClassAdmin      = "admin"
	ClassContainers = "containers"
	// ClassSSH volgt de instelling "SSH vanaf".
	ClassSSH = "ssh"
	// ClassIgnore: niet openzetten en niet als onbekend tonen (ufw laat
	// het zelf toe, zoals DHCP en mDNS, of het is uitgaand).
	ClassIgnore = "ignore"
)

// Classes zijn de klassen die een eigen regel mag gebruiken.
var Classes = []string{ClassAny, ClassLAN, ClassCluster, ClassAdmin, ClassContainers}

// ContainerNets zijn de netwerken van Docker (bridge en compose).
var ContainerNets = []string{"172.16.0.0/12"}

type portSpec struct {
	Proto string `json:"proto"`
	Port  string `json:"port"`
	Class string `json:"class"`
	Label string `json:"label,omitempty"`
}

// Software is een regel van de catalogus.
type Software struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	// Procs zijn procesnamen (comm, hoogstens 15 tekens; begin volstaat),
	// Units systemd-units (naam@ volstaat voor postgresql@16-main), Images
	// stukken van een Docker-image.
	Procs  []string `json:"procs,omitempty"`
	Units  []string `json:"units,omitempty"`
	Images []string `json:"images,omitempty"`
	// Ports zijn de bekende poorten met hun klasse.
	Ports []portSpec `json:"ports"`
	// Fixed: de bekende poorten gaan open zodra de unit enabled of actief
	// is, ook als er (nog) niets luistert.
	Fixed bool `json:"fixed,omitempty"`
	// Default is de klasse voor andere poorten van dit proces; leeg volgt
	// de instelling voor onbekende poorten.
	Default string `json:"default,omitempty"`
	// VRRP: keepalived; VRRP gaat open voor de clusterleden.
	VRRP bool `json:"vrrp,omitempty"`
}

func ports(class string, proto string, ps ...string) []portSpec {
	out := make([]portSpec, 0, len(ps))
	for _, p := range ps {
		out = append(out, portSpec{Proto: proto, Port: p, Class: class})
	}
	return out
}

func tcp(class string, ps ...string) []portSpec { return ports(class, "tcp", ps...) }
func udp(class string, ps ...string) []portSpec { return ports(class, "udp", ps...) }

func lbl(label string, ps []portSpec) []portSpec {
	for i := range ps {
		ps[i].Label = label
	}
	return ps
}

// Catalog is de software die ClusterForge kent.
var Catalog = []Software{
	{Key: "ssh", Name: "SSH", Procs: []string{"sshd"}, Units: []string{"ssh", "sshd"}, Ports: tcp(ClassSSH, "22"), Default: ClassSSH},
	{Key: "keepalived", Name: "Keepalived (VRRP)", Procs: []string{"keepalived"}, Units: []string{"keepalived"}, VRRP: true, Default: ClassCluster},
	{Key: "nginx", Name: "nginx", Procs: []string{"nginx"}, Units: []string{"nginx"}, Ports: tcp(ClassAny, "80", "443"), Default: ClassLAN},
	{Key: "apache", Name: "Apache", Procs: []string{"apache2", "httpd"}, Units: []string{"apache2"}, Ports: tcp(ClassAny, "80", "443"), Default: ClassLAN},
	{Key: "caddy", Name: "Caddy", Procs: []string{"caddy"}, Units: []string{"caddy"}, Images: []string{"caddy"}, Ports: append(tcp(ClassAny, "80", "443"), udp(ClassAny, "443")...), Default: ClassAdmin},
	{Key: "traefik", Name: "Traefik", Procs: []string{"traefik"}, Units: []string{"traefik"}, Images: []string{"traefik"}, Ports: tcp(ClassAny, "80", "443"), Default: ClassAdmin},
	{Key: "haproxy", Name: "HAProxy", Procs: []string{"haproxy"}, Units: []string{"haproxy"},
		Ports: slices.Concat(tcp(ClassAny, "80", "443"), lbl("statistieken", tcp(ClassAdmin, "7000", "8404"))), Default: ClassLAN},
	{Key: "npm", Name: "Nginx Proxy Manager", Images: []string{"jc21/nginx-proxy-manager"}, Units: []string{"cf-npm"},
		Ports: slices.Concat(tcp(ClassAny, "80", "443"), lbl("beheer", tcp(ClassAdmin, "81"))), Fixed: true, Default: ClassLAN},
	{Key: "mariadb", Name: "MariaDB / Galera", Procs: []string{"mariadbd", "mysqld"}, Units: []string{"mariadb", "mysql"},
		Ports: slices.Concat(tcp(ClassLAN, "3306", "3307"), lbl("Galera", tcp(ClassCluster, "4567", "4568", "4444")), lbl("Galera", udp(ClassCluster, "4567"))),
		Fixed: true, Default: ClassLAN},
	{Key: "garb", Name: "Galera-scheidsrechter (garb)", Procs: []string{"garbd"}, Units: []string{"garb"}, Ports: tcp(ClassCluster, "4567"), Fixed: true, Default: ClassCluster},
	{Key: "postgresql", Name: "PostgreSQL", Procs: []string{"postgres"}, Units: []string{"postgresql@", "postgresql"}, Ports: tcp(ClassLAN, "5432", "5433"), Default: ClassLAN},
	{Key: "patroni", Name: "Patroni", Procs: []string{"patroni"}, Units: []string{"patroni"},
		Ports: slices.Concat(lbl("REST", tcp(ClassCluster, "8008")), tcp(ClassLAN, "5432")), Fixed: true, Default: ClassCluster},
	{Key: "etcd", Name: "etcd", Procs: []string{"etcd"}, Units: []string{"etcd"}, Ports: tcp(ClassCluster, "2379", "2380"), Fixed: true, Default: ClassCluster},
	{Key: "redis", Name: "Redis", Procs: []string{"redis-server", "redis-sentinel", "keydb-server"}, Units: []string{"redis-server", "redis-sentinel"}, Images: []string{"redis", "valkey"},
		Ports: tcp(ClassCluster, "6379", "26379"), Default: ClassCluster},
	{Key: "authentik", Name: "Authentik", Images: []string{"goauthentik/server"}, Units: []string{"cf-authentik"},
		Ports: slices.Concat(tcp(ClassLAN, "9000", "9443"), lbl("metrics", tcp(ClassAdmin, "9300"))), Fixed: true, Default: ClassLAN},
	{Key: "vaultx", Name: "VaultX", Units: []string{"cf-vaultx", "vaultx"}, Ports: tcp(ClassLAN, "80", "443"), Fixed: true, Default: ClassLAN},
	{Key: "home-assistant", Name: "Home Assistant", Images: []string{"home-assistant"}, Procs: []string{"hass"}, Units: []string{"cf-homeassistant", "home-assistant"},
		Ports: tcp(ClassLAN, "8123"), Fixed: true, Default: ClassLAN},
	{Key: "zabbix-server", Name: "Zabbix-server", Procs: []string{"zabbix_server", "zabbix_proxy"}, Units: []string{"zabbix-server", "cf-zabbix-server", "zabbix-proxy", "cf-zabbix-proxy"},
		Ports: tcp(ClassLAN, "10051"), Fixed: true, Default: ClassLAN},
	{Key: "zabbix-agent", Name: "Zabbix-agent", Procs: []string{"zabbix_agentd", "zabbix_agent2"}, Units: []string{"zabbix-agent", "zabbix-agent2"},
		Ports: tcp(ClassLAN, "10050"), Fixed: true, Default: ClassLAN},
	{Key: "grafana", Name: "Grafana", Procs: []string{"grafana", "grafana-server"}, Units: []string{"grafana-server"}, Images: []string{"grafana/grafana"}, Ports: tcp(ClassLAN, "3000"), Default: ClassLAN},
	{Key: "prometheus", Name: "Prometheus / exporters", Procs: []string{"prometheus", "node_exporter", "prometheus-node"}, Ports: tcp(ClassLAN, "9090", "9100"), Default: ClassLAN},
	{Key: "victoriametrics", Name: "VictoriaMetrics", Procs: []string{"victoria-metrics"}, Images: []string{"victoria-metrics"}, Ports: tcp(ClassLAN, "8428"), Default: ClassLAN},
	{Key: "plex", Name: "Plex", Procs: []string{"Plex Media Serv", "Plex DLNA Serve"}, Units: []string{"plexmediaserver"}, Images: []string{"plex"},
		Ports: slices.Concat(tcp(ClassAny, "32400"), udp(ClassLAN, "1900", "5353", "32410", "32412", "32413", "32414"), tcp(ClassLAN, "32469")), Fixed: true, Default: ClassLAN},
	{Key: "proxmox", Name: "Proxmox VE", Procs: []string{"pveproxy", "spiceproxy", "pvedaemon"}, Units: []string{"pveproxy"},
		Ports: tcp(ClassAdmin, "8006", "3128"), Fixed: true, Default: ClassAdmin},
	{Key: "clusterforge", Name: "ClusterForge-server", Procs: []string{"clusterforge-se"}, Images: []string{"clusterforge-server"},
		Ports: slices.Concat(lbl("agents (NATS)", tcp(ClassLAN, "4222")), lbl("web", tcp(ClassLAN, "8080"))), Default: ClassLAN},
	{Key: "crowdsec", Name: "CrowdSec", Procs: []string{"crowdsec", "crowdsec-firew"}, Units: []string{"crowdsec"}, Default: ClassAdmin},
	{Key: "dns", Name: "DNS", Procs: []string{"named", "unbound", "pihole-FTL", "dnsmasq", "coredns"}, Ports: append(tcp(ClassLAN, "53"), udp(ClassLAN, "53")...), Default: ClassLAN},
	{Key: "nfs", Name: "NFS", Procs: []string{"rpcbind", "rpc.mountd", "rpc.statd"}, Units: []string{"nfs-server"}, Ports: append(tcp(ClassLAN, "111", "2049"), udp(ClassLAN, "111")...), Default: ClassLAN},
	{Key: "samba", Name: "Samba", Procs: []string{"smbd"}, Units: []string{"smbd"}, Ports: tcp(ClassLAN, "139", "445"), Default: ClassLAN},
	{Key: "rsync", Name: "rsync-daemon", Procs: []string{"rsync"}, Units: []string{"rsync"}, Ports: tcp(ClassCluster, "873"), Default: ClassCluster},
	{Key: "k3s", Name: "k3s", Procs: []string{"k3s-server", "k3s-agent", "k3s"}, Units: []string{"k3s", "k3s-agent"},
		Ports: slices.Concat(lbl("API", tcp(ClassLAN, "6443")), tcp(ClassCluster, "10250", "2379", "2380"), lbl("flannel", udp(ClassCluster, "8472", "51820"))), Fixed: true, Default: ClassCluster},
	{Key: "seaweedfs", Name: "SeaweedFS", Procs: []string{"weed"}, Units: []string{"cf-seaweedfs"},
		Ports: tcp(ClassCluster, "9333", "19333", "8080", "18080", "8888", "18888", "8081"), Default: ClassCluster},
	{Key: "mosquitto", Name: "Mosquitto (MQTT)", Procs: []string{"mosquitto"}, Units: []string{"mosquitto"}, Images: []string{"eclipse-mosquitto"}, Ports: tcp(ClassLAN, "1883", "8883"), Default: ClassLAN},
	// Wat ufw zelf toelaat of uitgaand is.
	{Key: "system", Name: "Systeem (DHCP, mDNS, tijd)", Procs: []string{"dhclient", "systemd-network", "avahi-daemon", "nmbd", "cf-agent", "systemd-resolve", "chronyd", "systemd-timesyn"}, Default: ClassIgnore},
}

// Templates zijn de poorten die een clustertemplate nodig heeft, ook als
// de dienst nog niet draait (etcd moet open staan vóór het een cluster
// vormt).
var Templates = map[string][]portSpec{
	"authentik": slices.Concat(lbl("etcd", tcp(ClassCluster, "2379", "2380")), lbl("Patroni", tcp(ClassCluster, "8008")),
		lbl("PostgreSQL", tcp(ClassLAN, "5432")), lbl("PostgreSQL vanuit de containers", tcp(ClassContainers, "5432")),
		lbl("Redis", tcp(ClassCluster, "6379")), lbl("Authentik", tcp(ClassLAN, "9000", "9443"))),
	"keepalived-nginx":    lbl("web", tcp(ClassAny, "80", "443")),
	"nginx-proxy-manager": slices.Concat(lbl("web", tcp(ClassAny, "80", "443")), lbl("NPM-beheer", tcp(ClassAdmin, "81"))),
	"home-assistant":      lbl("Home Assistant", tcp(ClassLAN, "8123")),
	"vaultx": slices.Concat(lbl("etcd", tcp(ClassCluster, "2379", "2380")), lbl("Patroni", tcp(ClassCluster, "8008")),
		lbl("PostgreSQL", tcp(ClassCluster, "5432")), lbl("VaultX", tcp(ClassLAN, "80", "443"))),
	"zabbix": slices.Concat(lbl("Zabbix-server", tcp(ClassLAN, "10051")), lbl("Zabbix-web", tcp(ClassLAN, "80", "443"))),
	"media-platform": slices.Concat(lbl("web", tcp(ClassLAN, "80", "443")),
		lbl("SeaweedFS", tcp(ClassCluster, "9333", "19333", "8080", "18080", "8888", "18888", "8081"))),
	"kubernetes": slices.Concat(lbl("k3s API", tcp(ClassLAN, "6443")), lbl("k3s", tcp(ClassCluster, "10250", "2379", "2380")),
		lbl("flannel", udp(ClassCluster, "8472", "51820"))),
}

// SoftwareByKey zoekt in de catalogus.
func SoftwareByKey(k string) (Software, bool) {
	i := slices.IndexFunc(Catalog, func(s Software) bool { return s.Key == k })
	if i < 0 {
		return Software{}, false
	}
	return Catalog[i], true
}

func (s Software) matchProc(p string) bool {
	return p != "" && slices.ContainsFunc(s.Procs, func(x string) bool { return strings.HasPrefix(p, x) })
}

func (s Software) matchUnit(u string) bool {
	return u != "" && slices.ContainsFunc(s.Units, func(x string) bool {
		if strings.HasSuffix(x, "@") {
			return strings.HasPrefix(u, x)
		}
		return u == x
	})
}

func (s Software) matchImage(img string) bool {
	if img == "" {
		return false
	}
	img = strings.ToLower(img)
	return slices.ContainsFunc(s.Images, func(x string) bool {
		// Het deel van het image na de registry en vóór de tag.
		name := img
		if i := strings.LastIndexByte(name, '@'); i >= 0 {
			name = name[:i]
		}
		if i := strings.LastIndexByte(name, ':'); i > strings.LastIndexByte(name, '/') {
			name = name[:i]
		}
		return name == x || strings.HasSuffix(name, "/"+x) || strings.Contains(x, "/") && strings.HasSuffix(name, x)
	})
}

// identify zoekt de software achter een proces, unit of image.
func identify(proc, unit, image string) (Software, bool) {
	for _, s := range Catalog {
		if s.matchImage(image) {
			return s, true
		}
	}
	for _, s := range Catalog {
		if s.matchProc(proc) || s.matchUnit(unit) {
			return s, true
		}
	}
	return Software{}, false
}
