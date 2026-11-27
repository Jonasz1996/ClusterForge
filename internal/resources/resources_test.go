package resources

import (
	"strings"
	"testing"
)

func TestDependencies(t *testing.T) {
	known := map[string]bool{"192.168.10.180": true}
	env := []string{
		"GITEA__database__HOST=192.168.0.190:3306",
		"DB_HOST=192.168.0.190",
		"DB_PORT=5432",
		"DATABASE_URL=postgres://u:p@192.168.10.180:5432/x",
		"PATH=/usr/bin",
		"REDIS_HOST=",
	}
	got := Dependencies(env, known)
	if len(got) != 3 {
		t.Fatalf("verwacht 3 afhankelijkheden, kreeg %v", got)
	}
	var unknown, ok int
	for _, g := range got {
		if strings.Contains(g, "bestaat die server nog") {
			unknown++
			if !strings.Contains(g, "192.168.0.190") {
				t.Errorf("onbekend adres verkeerd: %s", g)
			}
		} else {
			ok++
		}
	}
	if unknown != 2 || ok != 1 {
		t.Errorf("onbekend=%d gekend=%d: %v", unknown, ok, got)
	}
	if !strings.Contains(strings.Join(got, "|"), "192.168.0.190:5432") {
		t.Errorf("poort uit DB_PORT ontbreekt: %v", got)
	}
}

func TestDefaultsPerEnv(t *testing.T) {
	d := Defaults
	if d["prod"].DiskPct >= d["lab"].DiskPct {
		t.Errorf("prod moet strenger zijn dan lab: %+v", d)
	}
}
