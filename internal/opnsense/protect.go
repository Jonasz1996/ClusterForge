package opnsense

// Wat ClusterForge op de firewall nooit verandert (fase 40 M5): de gebruikers
// root en clusterforge (de API-sleutel van de agent hangt aan clusterforge)
// en de groep admins.

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

var (
	protectedUsers  = []string{"root", "clusterforge"}
	protectedGroups = []string{"admins"}
	accountPath     = regexp.MustCompile(`^system/(user|group)/(add|set|del|toggle)(/([0-9a-f-]{36}))?`)
)

func isProtected(kind, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	list := protectedUsers
	if kind == "group" {
		list = protectedGroups
	}
	for _, p := range list {
		if name == p {
			return true
		}
	}
	return false
}

// protect weigert aanroepen die een beschermde gebruiker of groep raken.
func (s *Service) protect(ctx context.Context, id uuid.UUID, calls []protocol.OPNCall) error {
	names := map[string]map[string]string{}
	for _, c := range calls {
		m := accountPath.FindStringSubmatch(c.Path)
		if m == nil {
			continue
		}
		kind, verb, u := m[1], m[2], m[4]
		if inner, ok := c.Body[kind].(map[string]any); ok {
			if n, _ := inner["name"].(string); isProtected(kind, n) {
				return ValidationError{fmt.Sprintf("%s %s beheert ClusterForge niet; dat doe je op de firewall zelf", kindWord(kind), n)}
			}
		}
		if verb == "add" || u == "" {
			continue
		}
		if names[kind] == nil {
			rows, err := s.searchRows(ctx, id, "system/"+kind+"/search")
			if err != nil {
				return fmt.Errorf("de %ss van de firewall lezen mislukte: %w", kind, err)
			}
			names[kind] = map[string]string{}
			for _, r := range rows {
				names[kind][r.UUID] = r.F["name"]
			}
		}
		if n := names[kind][u]; isProtected(kind, n) {
			return ValidationError{fmt.Sprintf("%s %s beheert ClusterForge niet; dat doe je op de firewall zelf", kindWord(kind), n)}
		}
	}
	return nil
}

func kindWord(kind string) string {
	if kind == "group" {
		return "groep"
	}
	return "gebruiker"
}
