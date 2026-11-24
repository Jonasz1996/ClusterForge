package upkeep

import (
	"fmt"

	"github.com/Jonasz1996/clusterforge/internal/planner"
)

// Policy is wat de agents vanzelf onderhouden en wanneer. Alles staat in
// gewone getallen en vinkjes: het script krijgt nooit vrije tekst.
type Policy struct {
	Enabled bool `json:"enabled"`
	// Window is het venster, zoals "dagelijks 01:30-03:30".
	Window string `json:"window"`

	// Schijf.
	Journal     bool `json:"journal"`
	JournalMB   int  `json:"journal_mb"`
	Apt         bool `json:"apt"`
	Logrotate   bool `json:"logrotate"`
	OldLogs     bool `json:"old_logs"`
	OldLogsDays int  `json:"old_logs_days"`
	Coredumps   bool `json:"coredumps"`
	Tmp         bool `json:"tmp"`
	TmpDays     int  `json:"tmp_days"`

	// Docker. Volumes (data) blijven altijd staan.
	DockerImages     bool `json:"docker_images"`
	DockerImageDays  int  `json:"docker_image_days"`
	DockerBuildCache bool `json:"docker_build_cache"`
	// DockerLogsMB kort containerlogs in die groter zijn; 0 is uit.
	DockerLogsMB int `json:"docker_logs_mb"`

	// Databases. Analyze: MariaDB ANALYZE per node (niet gerepliceerd),
	// PostgreSQL VACUUM ANALYZE op de primary. Optimize: MariaDB OPTIMIZE
	// van gefragmenteerde tabellen (Galera: de node even los, RSU) en
	// PostgreSQL REINDEX CONCURRENTLY van opgezwollen indexen.
	DBAnalyze      bool `json:"db_analyze"`
	DBOptimize     bool `json:"db_optimize"`
	OptimizeMinMB  int  `json:"optimize_min_mb"`
	OptimizeMinPct int  `json:"optimize_min_pct"`

	// Urgent ruimt een node die boven de ernstige schijfdrempel komt meteen
	// op, ook buiten het venster (alleen de schijfonderdelen).
	Urgent bool `json:"urgent"`
	// PauseSeconds is de pauze tussen twee nodes van een cluster.
	PauseSeconds int `json:"pause_seconds"`
}

// DefaultPolicy staat uit; aangezet doet ze het veilige: logs, caches,
// ongebruikte Docker-images van meer dan twee weken en ANALYZE/VACUUM, elke
// nacht van 01:30 tot 03:30 (vóór het updatevenster van zondag 03:00).
var DefaultPolicy = Policy{
	Window:  "dagelijks 01:30-03:30",
	Journal: true, JournalMB: 500, Apt: true, Logrotate: true, OldLogs: true, OldLogsDays: 14, Coredumps: true, TmpDays: 10,
	DockerImages: true, DockerImageDays: 14, DockerBuildCache: true,
	DBAnalyze: true, OptimizeMinMB: 100, OptimizeMinPct: 20,
	Urgent: true, PauseSeconds: 30,
}

// FieldError: ongeldige invoer, met uitleg.
type FieldError struct{ Msg string }

func (e *FieldError) Error() string { return e.Msg }

// Normalize vult lege getallen met de standaard en controleert de rest.
func (p Policy) Normalize() (Policy, error) {
	if p.Window == "" {
		p.Window = DefaultPolicy.Window
	}
	if _, err := planner.ParseWindow(p.Window); err != nil {
		return p, &FieldError{"venster: " + err.Error()}
	}
	def := func(v *int, d, lo, hi int, what string) error {
		if *v == 0 {
			*v = d
		}
		if *v < lo || *v > hi {
			return &FieldError{fmt.Sprintf("%s moet tussen %d en %d liggen", what, lo, hi)}
		}
		return nil
	}
	checks := []error{
		def(&p.JournalMB, DefaultPolicy.JournalMB, 50, 10000, "de grootte van het systeemlogboek (MB)"),
		def(&p.OldLogsDays, DefaultPolicy.OldLogsDays, 1, 365, "het aantal dagen voor oude logbestanden"),
		def(&p.TmpDays, DefaultPolicy.TmpDays, 1, 365, "het aantal dagen voor tijdelijke bestanden"),
		def(&p.DockerImageDays, DefaultPolicy.DockerImageDays, 1, 365, "het aantal dagen voor Docker-images"),
		def(&p.OptimizeMinMB, DefaultPolicy.OptimizeMinMB, 1, 100000, "de vrije ruimte voor OPTIMIZE (MB)"),
		def(&p.OptimizeMinPct, DefaultPolicy.OptimizeMinPct, 5, 90, "het percentage voor OPTIMIZE"),
	}
	for _, err := range checks {
		if err != nil {
			return p, err
		}
	}
	if p.DockerLogsMB != 0 && (p.DockerLogsMB < 50 || p.DockerLogsMB > 100000) {
		return p, &FieldError{"containerlogs inkorten vanaf: tussen 50 en 100000 MB, of 0 voor uit"}
	}
	if p.PauseSeconds < 0 || p.PauseSeconds > 3600 {
		return p, &FieldError{"de pauze tussen nodes moet tussen 0 en 3600 seconden liggen"}
	}
	if p.Enabled && !p.anything() {
		return p, &FieldError{"kies minstens één onderdeel"}
	}
	return p, nil
}

func (p Policy) anything() bool {
	return p.diskAnything() || p.DBAnalyze || p.DBOptimize
}

func (p Policy) diskAnything() bool {
	return p.Journal || p.Apt || p.Logrotate || p.OldLogs || p.Coredumps || p.Tmp ||
		p.DockerImages || p.DockerBuildCache || p.DockerLogsMB > 0
}

// DiskOnly is het beleid zonder databases, voor een spoedbeurt.
func (p Policy) DiskOnly() Policy {
	p.DBAnalyze, p.DBOptimize = false, false
	return p
}
