package protocol

// SeaweedSince is de eerste protocolversie die SeaweedFS in de facts zet.
const SeaweedSince = 26

// SeaweedFacts beschrijft SeaweedFS op een node: de weed-processen met hun
// instellingen, de configuratiebestanden en wat de master en volume server
// zelf over hun toestand zeggen. Nil zonder weed-proces of configuratie.
// Geheimen (wachtwoorden en sleutels in vlaggen of toml) komen er nooit in.
type SeaweedFacts struct {
	// Version zoals de master of volume server hem meldt, zoals "30GB 3.80".
	Version   string           `json:"version,omitempty"`
	Processes []SeaweedProcess `json:"processes"`
	// Configs zijn de toml-bestanden die weed leest.
	Configs []SeaweedConfig `json:"configs,omitempty"`
	// Master is de toestand van de master op deze node, als die antwoordt.
	Master *SeaweedMaster `json:"master,omitempty"`
	// Volume is de toestand van de volume server op deze node.
	Volume *SeaweedVolume `json:"volume,omitempty"`
}

// SeaweedProcess is één weed-proces. Role is het subcommando: master,
// volume, filer, s3, server (alles in één), mount, webdav, …
type SeaweedProcess struct {
	Role string `json:"role"`
	PID  int    `json:"pid"`
	// Unit is de systemd-unit, of leeg.
	Unit string `json:"unit,omitempty"`
	// Container is het korte id van de Docker-container, of leeg.
	Container string `json:"container,omitempty"`
	// Args zijn de argumenten na weed; waarden van geheime vlaggen zijn ***.
	Args []string `json:"args"`

	IP string `json:"ip,omitempty"`
	// Ports per dienst in dit proces: master, volume, filer, s3, webdav.
	Ports map[string]int `json:"ports,omitempty"`
	// Dirs zijn de datamappen van de volume server (-dir).
	Dirs []string `json:"dirs,omitempty"`
	// MetaDir is de map van de master (-mdir).
	MetaDir string `json:"meta_dir,omitempty"`
	// Peers zijn de masters van het raft-cluster (-peers).
	Peers []string `json:"peers,omitempty"`
	// Masters zijn de masters waarmee een volume server, filer of mount
	// verbindt (-mserver, -master).
	Masters []string `json:"masters,omitempty"`
	// DefaultReplication van de master, zoals 000 of 002.
	DefaultReplication string `json:"default_replication,omitempty"`
	DataCenter         string `json:"data_center,omitempty"`
	Rack               string `json:"rack,omitempty"`
	// Disks zijn grootte en vrije ruimte van de datamappen.
	Disks []SeaweedDisk `json:"disks,omitempty"`
}

type SeaweedDisk struct {
	Dir   string `json:"dir"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
}

// SeaweedConfig is een toml-bestand van weed, met alleen de namen van de
// secties die aanstaan, nooit hun waarden.
type SeaweedConfig struct {
	Path string `json:"path"`
	// Container is het korte id van de container waarin het bestand staat.
	Container string `json:"container,omitempty"`
	// Kind is filer, master, security, notification, replication of s3.
	Kind string `json:"kind"`
	// Enabled zijn de secties met enabled = true, zoals de store van de
	// filer (leveldb2, mysql2, postgres2, …).
	Enabled []string `json:"enabled,omitempty"`
	// JWT zegt of security.toml een signeersleutel heeft.
	JWT bool `json:"jwt,omitempty"`
}

// SeaweedMaster is wat /cluster/status en /dir/status zeggen.
type SeaweedMaster struct {
	Leader      string            `json:"leader,omitempty"`
	IsLeader    bool              `json:"is_leader"`
	Peers       []string          `json:"peers,omitempty"`
	MaxVolumes  int               `json:"max_volumes"`
	FreeVolumes int               `json:"free_volumes"`
	DataNodes   []SeaweedDataNode `json:"data_nodes,omitempty"`
	Layouts     []SeaweedLayout   `json:"layouts,omitempty"`
}

type SeaweedDataNode struct {
	URL        string `json:"url"`
	DataCenter string `json:"data_center,omitempty"`
	Rack       string `json:"rack,omitempty"`
	Volumes    int    `json:"volumes"`
	Max        int    `json:"max"`
}

// SeaweedLayout is een combinatie van collectie en replicatie waarvoor de
// master volumes heeft, met hoeveel er schrijfbaar zijn.
type SeaweedLayout struct {
	Collection  string `json:"collection,omitempty"`
	Replication string `json:"replication"`
	Writable    int    `json:"writable"`
}

// SeaweedVolume is wat /status van de volume server zegt.
type SeaweedVolume struct {
	Volumes  int    `json:"volumes"`
	ReadOnly int    `json:"read_only,omitempty"`
	Bytes    uint64 `json:"bytes"`
	Files    uint64 `json:"files"`
	// Replications telt de volumes per replicatie, zoals {"000": 12}.
	Replications map[string]int `json:"replications,omitempty"`
	Collections  []string       `json:"collections,omitempty"`
}

// Stable laat weg wat bij elke meting verandert.
func (s *SeaweedFacts) Stable() *SeaweedFacts {
	if s == nil {
		return nil
	}
	out := *s
	out.Master, out.Volume = nil, nil
	out.Processes = make([]SeaweedProcess, len(s.Processes))
	for i, p := range s.Processes {
		p.PID = 0
		p.Disks = nil
		out.Processes[i] = p
	}
	return &out
}
