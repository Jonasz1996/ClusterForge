package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Docker zoals Portainer (fase 28): de agent praat met de Docker Engine API
// over de lokale socket (/var/run/docker.sock), nooit over TCP. Lezen gaat
// met een verzoek op cf.node.<id>.docker (DockerOpGet, DockerOpStats);
// veranderen met een run van de soort RunDockerAPI, zodat elke wijziging
// een taak met uitvoer is.
//
// De agent controleert de vorm en de allowlist hieronder (methode + pad);
// wat de GUI mag, wat een bevestiging vraagt en wie het mag, beslist de
// server (internal/dockerhost). Zo vraagt een nieuwe Docker-functie in
// ClusterForge meestal geen agentupdate.

// DockerSince is de eerste protocolversie met de Docker-API: het subject
// docker, runs van de soort RunDockerAPI en de consolesoort ConsoleDocker.
const DockerSince = 46

// SubjectDocker draagt de leesverzoeken: cf.node.<id>.docker.
const SubjectDocker = "docker"

const (
	TypeDockerRequest = "docker_request"
	TypeDockerResult  = "docker_result"
)

// RunDockerAPI is de soort run die iets aan Docker verandert: een of meer
// aanroepen van de API na elkaar.
const RunDockerAPI = "docker_api"

// Soorten leesverzoek.
const (
	// DockerOpGet is GET <Path>?<Query> op de Docker-API.
	DockerOpGet = "get"
	// DockerOpStats geeft CPU, geheugen, netwerk en schijf van elke
	// draaiende container (of alleen Containers), één meting per container.
	DockerOpStats = "stats"
)

const (
	// DockerSocket is waar de agent Docker vindt.
	DockerSocket = "/var/run/docker.sock"
	// DockerRequestTimeout is hoe lang een leesverzoek mag duren.
	DockerRequestTimeout = 30 * time.Second
	// MaxDockerResponse is het grootste antwoord; onder de grens van NATS.
	MaxDockerResponse = 900 << 10
	// MaxDockerBody is de grootste body van een aanroep.
	MaxDockerBody = 16 << 10
	// MaxDockerCalls is het meeste aantal aanroepen in één run.
	MaxDockerCalls = 50
)

type DockerRequest struct {
	ID       string            `json:"id"`
	Deadline time.Time         `json:"deadline"`
	Op       string            `json:"op"`
	Path     string            `json:"path,omitempty"`
	Query    map[string]string `json:"query,omitempty"`
	// Containers beperkt DockerOpStats tot deze ids of namen.
	Containers []string `json:"containers,omitempty"`
}

type DockerResponse struct {
	Error string `json:"error,omitempty"`
	// Status is de HTTP-status van Docker (bij DockerOpGet).
	Status int             `json:"status,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
	Stats  []DockerStat    `json:"stats,omitempty"`
}

// DockerStat is één meting van een container.
type DockerStat struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// CPU in procent van één kern (200 = twee kernen vol), zoals docker stats.
	CPU        float64 `json:"cpu"`
	MemUsage   uint64  `json:"mem_usage"`
	MemLimit   uint64  `json:"mem_limit"`
	NetRx      uint64  `json:"net_rx"`
	NetTx      uint64  `json:"net_tx"`
	BlockRead  uint64  `json:"block_read"`
	BlockWrite uint64  `json:"block_write"`
	PIDs       uint64  `json:"pids"`
}

// DockerCall is één aanroep die iets verandert.
type DockerCall struct {
	// Method is POST of DELETE.
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query,omitempty"`
	Body   json.RawMessage   `json:"body,omitempty"`
	// Title is wat de uitvoer bij deze aanroep toont.
	Title string `json:"title,omitempty"`
	// IgnoreMissing: een 404 is geen fout (al weg).
	IgnoreMissing bool `json:"ignore_missing,omitempty"`
}

// DockerAPIRun zijn de aanroepen van een run RunDockerAPI; de agent stopt
// bij de eerste fout.
type DockerAPIRun struct {
	Calls []DockerCall `json:"calls"`
}

const (
	dockerName = `[A-Za-z0-9][A-Za-z0-9_.-]{0,127}`
	// Een image: naam met registry, poort, tag of digest, of een id.
	dockerRef = `[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,254}`
)

var (
	dockerReads = []*regexp.Regexp{
		regexp.MustCompile(`^/(_ping|info|version|system/df|events)$`),
		regexp.MustCompile(`^/containers/json$`),
		regexp.MustCompile(`^/containers/` + dockerName + `/(json|top|changes)$`),
		regexp.MustCompile(`^/images/json$`),
		regexp.MustCompile(`^/images/` + dockerRef + `/(json|history)$`),
		regexp.MustCompile(`^/distribution/` + dockerRef + `/json$`),
		regexp.MustCompile(`^/volumes$`),
		regexp.MustCompile(`^/volumes/` + dockerName + `$`),
		regexp.MustCompile(`^/networks$`),
		regexp.MustCompile(`^/networks/` + dockerName + `$`),
	}
	dockerWrites = map[string][]*regexp.Regexp{
		"POST": {
			regexp.MustCompile(`^/containers/` + dockerName + `/(start|stop|restart|kill|pause|unpause|rename)$`),
			regexp.MustCompile(`^/images/create$`),
			regexp.MustCompile(`^/images/` + dockerRef + `/tag$`),
			regexp.MustCompile(`^/(containers|images|volumes|networks|build)/prune$`),
			regexp.MustCompile(`^/volumes/create$`),
			regexp.MustCompile(`^/networks/create$`),
			regexp.MustCompile(`^/networks/` + dockerName + `/(connect|disconnect)$`),
		},
		"DELETE": {
			regexp.MustCompile(`^/containers/` + dockerName + `$`),
			regexp.MustCompile(`^/images/` + dockerRef + `$`),
			regexp.MustCompile(`^/volumes/` + dockerName + `$`),
			regexp.MustCompile(`^/networks/` + dockerName + `$`),
		},
	}
	// Aanroepen met een JSON-body.
	dockerBodies   = regexp.MustCompile(`^/(volumes/create|networks/create|networks/` + dockerName + `/(connect|disconnect))$`)
	dockerQueryKey = regexp.MustCompile(`^[a-zA-Z_]{1,32}$`)
)

func matchAny(res []*regexp.Regexp, p string) bool {
	for _, re := range res {
		if re.MatchString(p) {
			return true
		}
	}
	return false
}

func validDockerPath(p string) error {
	if len(p) > 300 || strings.Contains(p, "..") || strings.Contains(p, "//") {
		return fmt.Errorf("ongeldig Docker-pad %q", p)
	}
	return nil
}

func validDockerQuery(q map[string]string) error {
	if len(q) > 20 {
		return errors.New("te veel queryvelden")
	}
	for k, v := range q {
		if !dockerQueryKey.MatchString(k) || len(v) > 2048 || strings.ContainsAny(v, "\x00\r\n") {
			return fmt.Errorf("ongeldig queryveld %q", k)
		}
	}
	return nil
}

// Validate controleert een leesverzoek.
func (r DockerRequest) Validate() error {
	switch r.Op {
	case DockerOpGet:
		if err := validDockerPath(r.Path); err != nil {
			return err
		}
		if !matchAny(dockerReads, r.Path) {
			return fmt.Errorf("Docker-pad %q mag niet gelezen worden", r.Path)
		}
		if r.Path == "/events" && r.Query["until"] == "" {
			return errors.New("gebeurtenissen lezen vraagt until, anders stopt de stroom niet")
		}
		return validDockerQuery(r.Query)
	case DockerOpStats:
		if len(r.Containers) > 500 {
			return errors.New("te veel containers")
		}
		for _, c := range r.Containers {
			if !ValidContainerName(c) {
				return fmt.Errorf("ongeldige container %q", c)
			}
		}
		return nil
	}
	return fmt.Errorf("onbekend Docker-verzoek %q", r.Op)
}

// Validate controleert een aanroep die iets verandert.
func (c DockerCall) Validate() error {
	if err := validDockerPath(c.Path); err != nil {
		return err
	}
	res, ok := dockerWrites[c.Method]
	if !ok {
		return fmt.Errorf("methode %q is geen POST of DELETE", c.Method)
	}
	if !matchAny(res, c.Path) {
		return fmt.Errorf("%s %s mag niet via ClusterForge", c.Method, c.Path)
	}
	if err := validDockerQuery(c.Query); err != nil {
		return err
	}
	if len(c.Body) > 0 {
		if c.Method != "POST" || !dockerBodies.MatchString(c.Path) {
			return fmt.Errorf("%s %s heeft geen body", c.Method, c.Path)
		}
		if len(c.Body) > MaxDockerBody || !json.Valid(c.Body) {
			return errors.New("ongeldige body")
		}
	}
	if len(c.Title) > 200 {
		return errors.New("titel te lang")
	}
	return nil
}

// Validate controleert een run.
func (d *DockerAPIRun) Validate() error {
	if d == nil || len(d.Calls) == 0 {
		return errors.New("docker_api-run zonder aanroepen")
	}
	if len(d.Calls) > MaxDockerCalls {
		return fmt.Errorf("meer dan %d aanroepen in één run", MaxDockerCalls)
	}
	for i, c := range d.Calls {
		if err := c.Validate(); err != nil {
			return fmt.Errorf("aanroep %d: %w", i+1, err)
		}
	}
	return nil
}

// DockerReadAllowed zegt of de agent dit pad laat lezen (voor de server,
// zodat die geen verzoek stuurt dat de agent toch weigert).
func DockerReadAllowed(p string) bool {
	return validDockerPath(p) == nil && matchAny(dockerReads, p)
}
