package templates

// Syncs van een template (fase 11): bestandssyncs die ClusterForge na de
// uitrol zelf als beheerde cronjob van de soort sync zet, van de node met
// het VIP naar de andere nodes. Zo hoeft een app met lokale toestand (zoals
// Nginx Proxy Manager met SQLite) geen gedeelde opslag: de VIP-node maakt
// een consistente kopie (Before) en de sync zet die op de andere nodes.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// SyncInfo is een sync van een template. Tekst mag {{ .params.x }} bevatten.
type SyncInfo struct {
	// Name is de naam van de cronjob in het cluster; een volgende uitrol of
	// wijziging past de job met die naam aan.
	Name string `yaml:"name"`
	// Schedule is een cronregel, zoals */5 * * * *.
	Schedule string `yaml:"schedule"`
	// Source is de map op de VIP-node, Target die op de andere nodes; met
	// een / op het einde. Target leeg is dezelfde map.
	Source string `yaml:"source"`
	Target string `yaml:"target,omitempty"`
	// Before draait de VIP-node vóór rsync; faalt het, dan kopieert de
	// sync niets.
	Before  string   `yaml:"before,omitempty"`
	Delete  bool     `yaml:"delete,omitempty"`
	Exclude []string `yaml:"exclude,omitempty"`
	// Alert: een mislukte run opent een incident.
	Alert bool `yaml:"alert,omitempty"`
	// TimeoutSeconds is de tijdslimiet van een run; 0 is de standaard.
	TimeoutSeconds int `yaml:"timeout_seconds,omitempty"`
}

// checkSyncs controleert de syncs bij het laden.
func (t *Template) checkSyncs() error {
	if len(t.Syncs) > 0 && t.ClusterType == "kubernetes" {
		return errors.New("syncs: horen niet bij cluster_type kubernetes")
	}
	seen := map[string]bool{}
	for i, s := range t.Syncs {
		switch {
		case strings.TrimSpace(s.Name) == "" || len(s.Name) > 80:
			return fmt.Errorf("sync %d: een naam van hoogstens 80 tekens", i+1)
		case seen[s.Name]:
			return fmt.Errorf("sync %s staat er twee keer in", s.Name)
		case s.Source == "" || s.Schedule == "":
			return fmt.Errorf("sync %s: source en schedule zijn verplicht", s.Name)
		case len(s.Before) > protocol.MaxSyncBefore:
			return fmt.Errorf("sync %s: before is te lang", s.Name)
		case s.TimeoutSeconds < 0 || s.TimeoutSeconds > 24*3600:
			return fmt.Errorf("sync %s: timeout_seconds van 0 tot 86400", s.Name)
		}
		seen[s.Name] = true
	}
	return nil
}

// RenderSyncs vult de parameters in de syncs in en controleert de mappen.
func (t *Template) RenderSyncs(c Context) ([]SyncInfo, error) {
	data := c.data()
	out := make([]SyncInfo, 0, len(t.Syncs))
	for _, s := range t.Syncs {
		for _, f := range []*string{&s.Schedule, &s.Source, &s.Target, &s.Before} {
			v, err := expandString(*f, data)
			if err != nil && *f != "" {
				return nil, fmt.Errorf("sync %s: %w", s.Name, err)
			}
			*f = strings.TrimSpace(v)
		}
		if s.Target == "" {
			s.Target = s.Source
		}
		if !protocol.ValidSyncPath(s.Source) || !protocol.ValidSyncPath(s.Target) {
			return nil, fmt.Errorf("sync %s: ongeldige map %s of %s", s.Name, s.Source, s.Target)
		}
		out = append(out, s)
	}
	return out, nil
}
