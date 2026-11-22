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

// kubeEOL is per minorversie van Kubernetes de dag waarop er geen patches
// meer komen, volgens de releasekalender op kubernetes.io; k3s volgt die.
// Voor 1.35 en 1.36 is het de verwachte dag: veertien maanden na de
// release, zoals bij de versies ervoor. Een nieuwe versie komt erbij in een
// nieuwe versie van ClusterForge, met een test.
var kubeEOL = map[string]string{
	"1.30": "2025-06-28", "1.31": "2025-10-28", "1.32": "2026-02-28", "1.33": "2026-06-28", "1.34": "2026-10-27",
	"1.35": "2027-02-28", "1.36": "2027-06-28",
}

// KubeEOL geeft de minorversie en het einde van haar ondersteuning bij een
// versie zoals v1.35.9+k3s1, of false als ClusterForge haar niet kent.
func KubeEOL(version string) (minor string, end time.Time, ok bool) {
	v := strings.TrimPrefix(version, "v")
	major, rest, found := strings.Cut(v, ".")
	if !found {
		return "", time.Time{}, false
	}
	m, _, _ := strings.Cut(rest, ".")
	minor = major + "." + m
	day, ok := kubeEOL[minor]
	if !ok {
		return minor, time.Time{}, false
	}
	t, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return minor, time.Time{}, false
	}
	return minor, t.Add(24*time.Hour - time.Second), true
}
