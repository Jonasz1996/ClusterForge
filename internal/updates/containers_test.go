package updates

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWaitContainers(t *testing.T) {
	calls := 0
	s := &Service{ReadyTimeout: time.Minute, ContainerPoll: time.Millisecond, Now: time.Now}
	s.Containers = func(context.Context, uuid.UUID) (map[string]string, error) {
		calls++
		switch calls {
		case 1:
			return nil, errors.New("docker herstart")
		case 2:
			return map[string]string{"server": "starting", "worker": "running"}, nil
		}
		return map[string]string{"server": "healthy", "worker": "running", "nieuw": "exited"}, nil
	}
	var log []string
	err := s.waitContainers(context.Background(), jobNode{Hostname: "ak1"}, []string{"server", "worker"}, func(m string) { log = append(log, m) })
	if err != nil || calls != 3 {
		t.Fatalf("err %v, calls %d, log %v", err, calls, log)
	}
	if !strings.Contains(strings.Join(log, "\n"), "server (starting)") {
		t.Fatalf("log %v", log)
	}

	s.ReadyTimeout = 0
	s.Containers = func(context.Context, uuid.UUID) (map[string]string, error) {
		return map[string]string{"server": "unhealthy"}, nil
	}
	if err := s.waitContainers(context.Background(), jobNode{Hostname: "ak1"}, []string{"server"}, func(string) {}); err == nil || !strings.Contains(err.Error(), "server (unhealthy)") {
		t.Fatalf("err %v", err)
	}
}
