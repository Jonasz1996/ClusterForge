package incidents

// statusLabels zijn de statussen zoals ze in een titel staan.
var statusLabels = map[string]string{
	"degraded":    "verminderd",
	"down":        "down",
	"split_brain": "in split-brain",
	"healthy":     "gezond",
	"unknown":     "onbekend",
}

// Title is de titel van een incident dat met p begint.
func Title(p Problem) string {
	st := statusLabels[p.Status]
	if st == "" {
		st = p.Status
	}
	switch p.Type {
	case SubjectCluster:
		return "Cluster " + p.Name + " is " + st
	case SubjectNode:
		return p.Name + " is " + st
	case SubjectService:
		return "Dienst " + p.Name + " is " + st
	case SubjectApp:
		return "App " + p.Name + " is " + st
	case SubjectCron, SubjectCronJob:
		return "Cronjob " + p.Name + " mislukt"
	case SubjectTestRun:
		if p.Status == "error" {
			return p.Name + " gaf een fout"
		}
		return p.Name + " mislukt"
	}
	return p.Name + " " + st
}

// SeverityLabel is de ernst in het Nederlands.
func SeverityLabel(s string) string {
	switch s {
	case SeverityCritical:
		return "kritiek"
	case SeverityHigh:
		return "hoog"
	}
	return "laag"
}
