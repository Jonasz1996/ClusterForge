package risk

import (
	"strings"
	"time"
)

// eol is per besturingssysteem en hoofdversie de dag waarop er geen
// beveiligingsupdates meer komen. Voor Debian is dat het einde van LTS, voor
// Ubuntu het einde van de gewone ondersteuning (zonder Ubuntu Pro), voor
// Rocky en AlmaLinux het einde van de onderhoudsfase. Een nieuwe versie komt
// erbij in een nieuwe versie van ClusterForge, met een test.
var eol = map[string]map[string]string{
	"debian": {
		"9": "2022-06-30", "10": "2024-06-30", "11": "2026-08-31", "12": "2028-06-30", "13": "2030-06-30",
	},
	"ubuntu": {
		"18.04": "2023-05-31", "20.04": "2025-05-31", "22.04": "2027-06-30", "24.04": "2029-05-31", "26.04": "2031-05-31",
	},
	"rocky":     {"8": "2029-05-31", "9": "2032-05-31"},
	"almalinux": {"8": "2029-05-31", "9": "2032-05-31"},
	"rhel":      {"8": "2029-05-31", "9": "2032-05-31"},
}

// EOL geeft het einde van de ondersteuning van een besturingssysteem uit
// os-release (ID en VERSION_ID), of false als ClusterForge het niet kent.
func EOL(id, version string) (time.Time, bool) {
	versions, ok := eol[strings.ToLower(id)]
	if !ok {
		return time.Time{}, false
	}
	v := version
	if _, ok := versions[v]; !ok {
		// Rocky 9.4 is hoofdversie 9.
		v, _, _ = strings.Cut(version, ".")
	}
	day, ok := versions[v]
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return time.Time{}, false
	}
	// Tot het einde van die dag.
	return t.Add(24*time.Hour - time.Second), true
}
