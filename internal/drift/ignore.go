package drift

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Ignore is een negeerregel. Hij geldt voor een hele stap, want herstel past
// hele stappen toe: een sleutel als file:/etc/nginx/nginx.conf, een
// voorvoegsel dat op * eindigt, of * alleen voor alles.
type Ignore struct {
	ID     uuid.UUID
	NodeID *uuid.UUID
	Key    string
}

// Matches zegt of de regel deze afwijking dekt.
func (i Ignore) Matches(f Finding) bool {
	if prefix, ok := strings.CutSuffix(i.Key, "*"); ok {
		return strings.HasPrefix(f.Step, prefix)
	}
	return f.Step == i.Key
}

// stepKinds zijn de soorten stappen die een sleutel kan noemen.
var stepKinds = []string{"package:", "service:", "file:", "directory:", "user:", "command:"}

// ValidateIgnore controleert een nieuwe regel.
func ValidateIgnore(key, reason string, expires *time.Time, now time.Time) error {
	switch {
	case key == "":
		return &FieldError{Field: "key", Message: "kies welke stap je negeert"}
	case len(key) > 500:
		return &FieldError{Field: "key", Message: "de sleutel is te lang"}
	case key != "*" && !startsWithKind(key):
		return &FieldError{Field: "key", Message: fmt.Sprintf("%q is geen stap; begin met bijvoorbeeld file:/pad of service:naam", key)}
	case strings.Count(key, "*") > 1 || (strings.Contains(key, "*") && !strings.HasSuffix(key, "*")):
		return &FieldError{Field: "key", Message: "een * mag alleen aan het eind"}
	case key == "*" && expires == nil:
		return &FieldError{Field: "expires_at", Message: "alles negeren kan alleen tijdelijk; kies een einddatum"}
	case len([]rune(strings.TrimSpace(reason))) < 3 || len([]rune(reason)) > 500:
		return &FieldError{Field: "reason", Message: "geef een reden van 3 tot 500 tekens"}
	case expires != nil && !expires.After(now):
		return &FieldError{Field: "expires_at", Message: "de einddatum ligt in het verleden"}
	}
	return nil
}

func startsWithKind(key string) bool {
	for _, k := range stepKinds {
		if strings.HasPrefix(key, k) {
			return true
		}
	}
	return false
}

// markIgnored zet Ignored op elke afwijking die een regel dekt en geeft de
// sleutels die tellen.
func markIgnored(findings []Finding, rules []Ignore) []string {
	keys := []string{}
	for i := range findings {
		findings[i].Ignored, findings[i].IgnoreID = false, nil
		for _, r := range rules {
			if r.Matches(findings[i]) {
				id := r.ID
				findings[i].Ignored, findings[i].IgnoreID = true, &id
				break
			}
		}
		if !findings[i].Ignored {
			keys = append(keys, findings[i].Key)
		}
	}
	return keys
}

// activeKeys zijn de sleutels van de afwijkingen die bij het opslaan
// telden.
func activeKeys(findings []Finding) []string {
	keys := []string{}
	for _, f := range findings {
		if !f.Ignored {
			keys = append(keys, f.Key)
		}
	}
	return keys
}
