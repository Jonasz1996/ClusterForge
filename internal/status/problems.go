package status

import "strings"

// Een dienst die op een node faalt, zoals Node hem in de reden zet (fase 47).
// Aandacht groepeert hiermee per oorzaak: "ufw draait niet op 25 nodes" in
// plaats van 25 nodes met elk dezelfde reden.

// ServiceState is wat er met een dienst mis is.
type ServiceState string

const (
	ServiceFailed   ServiceState = "failed"
	ServiceCrashing ServiceState = "crashing"
	ServiceStopped  ServiceState = "stopped"
)

// ServiceProblem is één dienst uit de reden van een node.
type ServiceProblem struct {
	Unit  string
	State ServiceState
}

// Text is de oorzaak in gewone taal, zonder node.
func (p ServiceProblem) Text() string {
	switch p.State {
	case ServiceFailed:
		return p.Unit + " is gefaald"
	case ServiceCrashing:
		return p.Unit + " crasht en start steeds opnieuw"
	}
	return p.Unit + " draait niet"
}

// ParseReason splitst de reden van een node (uit Node) in diensten die
// falen en de andere problemen, zoals een volle schijf.
func ParseReason(reason string) (services []ServiceProblem, other []string) {
	if strings.TrimSpace(reason) == "" {
		return nil, nil
	}
	for _, part := range strings.Split(reason, "; ") {
		part = strings.TrimSpace(part)
		rest, ok := strings.CutPrefix(part, "service ")
		if !ok {
			if part != "" {
				other = append(other, part)
			}
			continue
		}
		unit, tail, _ := strings.Cut(rest, " ")
		switch tail {
		case "is gefaald":
			services = append(services, ServiceProblem{unit, ServiceFailed})
		case "crasht en start steeds opnieuw":
			services = append(services, ServiceProblem{unit, ServiceCrashing})
		case "draait niet":
			services = append(services, ServiceProblem{unit, ServiceStopped})
		default:
			other = append(other, part)
		}
	}
	return services, other
}
