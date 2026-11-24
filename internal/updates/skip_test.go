package updates

import (
	"slices"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestKeepProjects(t *testing.T) {
	n := Node{Docker: &protocol.Docker{Projects: []protocol.ComposeProject{{Name: "web"}, {Name: "powershelluniversal"}, {Name: "db"}}}}
	if got := keepProjects(n, nil, []string{"powershelluniversal"}); !slices.Equal(got, []string{"web", "db"}) {
		t.Fatalf("overslaan: %v", got)
	}
	if got := keepProjects(n, []string{"web", "powershelluniversal"}, []string{"powershelluniversal"}); !slices.Equal(got, []string{"web"}) {
		t.Fatalf("met projecten: %v", got)
	}
	if got := keepProjects(Node{}, nil, []string{"x"}); got != nil {
		t.Fatalf("zonder Docker: %v", got)
	}
}
