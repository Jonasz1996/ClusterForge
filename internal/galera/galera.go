// Package galera weet hoe een Galera-cluster na een volledige uitval weer
// begint. Elke node weet tot welke transactie hij de data heeft: na een
// gewone stop staat dat in grastate.dat, na een crash niet (seqno -1) en
// haalt galera_recovery of mariadbd --wsrep-recover het uit InnoDB. De node met de hoogste seqno begint
// het cluster; de andere sluiten bij hem aan en halen wat ze missen. Zo gaat
// er geen transactie verloren.
//
// De agent leest de positie op zijn node; de server kiest. Een test met
// echte MariaDB-containers (CF_TEST_GALERA_DOCKER=1) laat zien dat het zo
// werkt.
package galera

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// GrastateFile is waar Galera zijn positie bewaart, in de datamap van
// MariaDB op Debian.
const GrastateFile = "/var/lib/mysql/grastate.dat"

// SafeToBootstrap zet safe_to_bootstrap op 1 in GrastateFile. Galera weigert
// anders te beginnen op een node die niet als laatste stopte; ClusterForge
// heeft dan al vastgesteld dat deze node de nieuwste data heeft.
const SafeToBootstrap = "sed -i 's/^safe_to_bootstrap:.*/safe_to_bootstrap: 1/' " + GrastateFile

// Package is het pakket met galera_recovery, galera_new_cluster en de
// SST-scripts van MariaDB.org sinds MariaDB 12.3.2 (MariaDB-server-galera
// bij RPM). Daarvoor zaten ze in mariadb-server; een node die naar 12.3.2
// ging zonder dit pakket, mist ze.
const Package = "mariadb-server-galera"

// NoNewCluster is de fout van Bootstrap op een node zonder
// galera_new_cluster.
const NoNewCluster = "galera_new_cluster ontbreekt op deze node; sinds MariaDB 12.3.2 zit het in het pakket " + Package + ". Installeer dat en probeer opnieuw"

// NewClusterVar is de variabele in de omgeving van systemd waarmee
// galera_new_cluster mariadb.service --wsrep-new-cluster meegeeft. Het
// script wist hem na de start; wordt het afgebroken, dan blijft hij staan
// en begint elke volgende start van MariaDB een nieuw cluster.
const NewClusterVar = "_WSREP_NEW_CLUSTER"

// Bootstrap begint het cluster op deze node: MariaDB stoppen als hij nog
// probeert te starten, de node veilig verklaren en galera_new_cluster.
// systemd haalt bij het starten zelf de positie uit InnoDB als grastate.dat
// die niet heeft. Zonder galera_new_cluster verandert het niets. Daarna
// gaat NewClusterVar weg, ook als galera_new_cluster mislukte of stopte
// voor het hem zelf wiste.
const Bootstrap = "command -v galera_new_cluster >/dev/null || { echo '" + NoNewCluster + "' >&2; exit 1; }; " +
	"systemctl stop mariadb; " + SafeToBootstrap + " && galera_new_cluster; rc=$?; systemctl unset-environment " + NewClusterVar + "; exit $rc"

// NoUUID is de uuid van een node die nog nooit in een cluster zat.
const NoUUID = "00000000-0000-0000-0000-000000000000"

// Position is tot waar een node de data van het cluster heeft: de uuid van
// de geschiedenis van het cluster en het volgnummer van de laatste
// transactie. Seqno is -1 als het niet bekend is.
type Position struct {
	UUID            string `json:"uuid"`
	Seqno           int64  `json:"seqno"`
	SafeToBootstrap bool   `json:"safe_to_bootstrap"`
}

// Known is true als de positie bekend is.
func (p Position) Known() bool { return p.Seqno >= 0 && p.UUID != "" && p.UUID != NoUUID }

func (p Position) String() string {
	if !p.Known() {
		return "onbekend"
	}
	return fmt.Sprintf("%s:%d", p.UUID, p.Seqno)
}

// ParseGrastate leest grastate.dat:
//
//	# GALERA saved state
//	version: 2.1
//	uuid:    5c3a1e0e-a8f1-11f0-9a3b-7e1c2d3f4a5b
//	seqno:   -1
//	safe_to_bootstrap: 0
func ParseGrastate(s string) (Position, error) {
	p := Position{Seqno: -1}
	var seen bool
	for line := range strings.SplitSeq(s, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "uuid":
			p.UUID, seen = v, true
		case "seqno":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return p, fmt.Errorf("grastate.dat: seqno %q", v)
			}
			p.Seqno = n
		case "safe_to_bootstrap":
			p.SafeToBootstrap = v == "1"
		}
	}
	if !seen {
		return p, errors.New("grastate.dat heeft geen uuid")
	}
	return p, nil
}

// recovered vindt de positie in de uitvoer van galera_recovery of van
// mysqld --wsrep-recover. Na de seqno kan nog een GTID komen.
var recovered = regexp.MustCompile(`(?:--wsrep_start_position=|Recovered position:?\s*)([0-9a-fA-F-]{36}):(-?\d+)`)

// ParseRecovered leest de positie uit wat galera_recovery schrijft, zoals
// --wsrep_start_position=5c3a1e0e-a8f1-11f0-9a3b-7e1c2d3f4a5b:1234, of uit
// de log van mariadbd --wsrep-recover ("WSREP: Recovered position: …").
// Bij meer posities telt de laatste.
func ParseRecovered(s string) (Position, error) {
	m := recovered.FindAllStringSubmatch(s, -1)
	if len(m) == 0 {
		return Position{Seqno: -1}, errors.New("geen positie in de uitvoer")
	}
	last := m[len(m)-1]
	n, err := strconv.ParseInt(last[2], 10, 64)
	if err != nil {
		return Position{Seqno: -1}, fmt.Errorf("seqno %q", last[2])
	}
	return Position{UUID: strings.ToLower(last[1]), Seqno: n}, nil
}

// ViaNote zegt voor het logboek hoe een agent de positie uit InnoDB las,
// als dat niet met galera_recovery was; via is DBPositionResult.Via.
func ViaNote(via string) string {
	if via == "" || via == "galera_recovery" {
		return ""
	}
	return " met " + via + " (galera_recovery ontbreekt)"
}

// PositionError vult de fout van een agent die geen positie las aan met wat
// eraan te doen is: een agent van voor ViaNote kan niet zonder
// galera_recovery.
func PositionError(msg string) string {
	if strings.Contains(msg, `"galera_recovery": executable file not found`) {
		return msg + ". Op deze node ontbreekt het pakket " + Package + " (sinds MariaDB 12.3.2 apart); werk de agent bij met Agents bijwerken bij Nodes, " +
			"dan leest hij de positie met mariadbd --wsrep-recover, of installeer het pakket"
	}
	return msg
}

// Candidate is een datanode die het cluster kan beginnen.
type Candidate struct {
	Name string
	// Index is het volgnummer van de node; bij gelijke data begint de
	// laagste.
	Index    int
	Position Position
}

// Choose kiest de node die het cluster begint: die met de hoogste seqno.
// Bij gelijke seqno wint de node die als laatste stopte
// (safe_to_bootstrap), dan het laagste volgnummer. Choose weigert als een
// positie onbekend is of als de nodes verschillende geschiedenissen hebben:
// dan is niet zeker welke node de nieuwste data heeft.
func Choose(cands []Candidate) (Candidate, error) {
	if len(cands) == 0 {
		return Candidate{}, errors.New("er is geen node om het cluster mee te beginnen")
	}
	var unknown []string
	for _, c := range cands {
		if !c.Position.Known() {
			unknown = append(unknown, c.Name)
		}
	}
	if len(unknown) > 0 {
		return Candidate{}, fmt.Errorf("de positie van %s is onbekend", strings.Join(unknown, ", "))
	}
	uuid := cands[0].Position.UUID
	for _, c := range cands[1:] {
		if !strings.EqualFold(c.Position.UUID, uuid) {
			return Candidate{}, fmt.Errorf("%s en %s hebben een andere geschiedenis (%s en %s); welke data de goede is, moet je zelf beslissen",
				cands[0].Name, c.Name, uuid, c.Position.UUID)
		}
	}
	best := slices.MaxFunc(cands, func(a, b Candidate) int {
		return cmp.Or(
			cmp.Compare(a.Position.Seqno, b.Position.Seqno),
			cmp.Compare(boolInt(a.Position.SafeToBootstrap), boolInt(b.Position.SafeToBootstrap)),
			// Een lager volgnummer is beter.
			cmp.Compare(b.Index, a.Index),
		)
	})
	return best, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
