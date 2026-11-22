package inventory

import (
	"testing"

	"github.com/google/uuid"
)

func TestNormalizeTags(t *testing.T) {
	got, err := normalizeTags([]string{" Web ", "web", "", "dc1", "role=lb"})
	if err != nil || len(got) != 3 || got[0] != "web" || got[1] != "dc1" || got[2] != "role=lb" {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := normalizeTags([]string{"met spatie"}); err == nil {
		t.Fatal("tag met spatie geaccepteerd")
	}
}

func TestDiff(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	before := ClusterFields{Name: "x", Tags: []string{"a"}, OwnerIDs: []uuid.UUID{a, b}}
	after := ClusterFields{Name: "y", Tags: []string{"a"}, OwnerIDs: []uuid.UUID{b, a}}
	d := diff(before, after)
	if len(d) != 1 || d["name"] == nil {
		t.Fatalf("diff: %v", d)
	}
}
