package agent

import (
	"context"
	"errors"
	"time"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// inspectTimeout is hoe lang state.inspect mag duren.
const inspectTimeout = 30 * time.Second

// inspect bekijkt hoe de stappen erbij staan, met dezelfde leesfuncties als
// apply. Het voert nooit apt-get, een ander systemctl-werkwoord dan show of
// sh uit, en schrijft niets.
func (a *Agent) inspect(ctx context.Context, steps []protocol.InspectStep) protocol.Result {
	if len(steps) == 0 {
		return failed(nil, "geen stappen")
	}
	res := protocol.Result{OK: true, Observations: make([]protocol.Observation, 0, len(steps))}
	for _, s := range steps {
		res.Observations = append(res.Observations, a.inspectStep(ctx, s))
	}
	return res
}

func (a *Agent) inspectStep(ctx context.Context, s protocol.InspectStep) protocol.Observation {
	var o protocol.Observation
	var err error
	switch s.Kind() {
	case "package":
		var states map[string]protocol.PackageState
		states, err = a.packageStates(ctx, s.Packages)
		if errors.Is(err, errNoDpkg) {
			return protocol.Observation{Skipped: err.Error()}
		}
		for _, n := range s.Packages {
			o.Packages = append(o.Packages, states[n])
		}
	case "file", "directory", "command":
		path, hash := s.File, true
		switch {
		case s.Directory != "":
			path, hash = s.Directory, false
		case s.Creates != "":
			path, hash = s.Creates, false
		}
		var st protocol.PathState
		st, err = a.pathState(path, hash)
		o.Path = &st
	case "service":
		if !unitName.MatchString(s.Service) {
			return protocol.Observation{Error: "ongeldige servicenaam " + s.Service}
		}
		var u protocol.UnitState
		u.Loaded, u.Enabled, u.Active, err = a.unit(ctx, s.Service)
		o.Service = &u
	case "user":
		var ok bool
		ok, err = a.userExists(ctx, s.User)
		o.UserExists = &ok
	default:
		return protocol.Observation{Error: "onbekende of lege stap"}
	}
	if err != nil {
		return protocol.Observation{Error: err.Error()}
	}
	return o
}
