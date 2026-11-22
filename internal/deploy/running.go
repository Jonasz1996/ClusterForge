package deploy

import (
	"context"
	"slices"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/templates"
)

// Running is wat er volgens de gewenste staat op een node draait: de
// services die de template start, de poorten van die services en de
// HTTP-controles van de template. De back-upcontrole vergelijkt een
// teruggezette kopie ermee.
type Running struct {
	Services []string
	Ports    []ServicePort
	HTTP     []HTTPCheck
}

// ServicePort is een poort waarop een service van de template luistert.
type ServicePort struct {
	Unit string
	Port int
}

// HTTPCheck is een HTTP-controle uit de template.
type HTTPCheck struct {
	URL    string
	Expect int
}

// Running leest wat er op een node van een cluster uit een template draait,
// met de templateversie van de spec en zonder een geheim te ontsleutelen.
// Een cluster zonder template geeft ErrNoSpec.
func (s *Service) Running(ctx context.Context, clusterID, nodeID uuid.UUID) (Running, error) {
	var out Running
	c, err := s.q.GetCluster(ctx, clusterID)
	if err != nil {
		return out, err
	}
	inv, nodes, err := s.loadNodes(ctx, clusterID)
	if err != nil {
		return out, err
	}
	spec, err := ParseSpec(c.Spec, templates.ClusterInfo{Name: c.Name, Slug: c.Slug, Environment: string(c.Environment)}, inv)
	if err != nil {
		return out, err
	}
	tpl, ok := s.Templates.Get(spec.Template.Name, spec.Template.Version)
	if !ok {
		return out, ErrNoSpec
	}
	steps, err := RenderMasked(tpl, spec, nodes, nodeID)
	if err != nil {
		return out, err
	}
	for _, st := range steps {
		if svc := st.Service; svc != nil && (svc.State == "started" || svc.State == "restarted" || svc.State == "reloaded") &&
			!slices.Contains(out.Services, svc.Name) {
			out.Services = append(out.Services, svc.Name)
		}
	}
	values, err := tpl.MaskedValues(spec.Params)
	if err != nil {
		return out, err
	}
	services, err := tpl.RenderServices(values)
	if err != nil {
		return out, err
	}
	for _, sv := range services {
		unit := sv.Unit
		if unit == "" {
			unit = sv.Name
		}
		if sv.Port > 0 && slices.Contains(out.Services, unit) {
			out.Ports = append(out.Ports, ServicePort{Unit: unit, Port: sv.Port})
		}
	}
	tc, err := spec.MaskedContext(tpl, nodes)
	if err != nil {
		return out, err
	}
	checks, err := tpl.RenderChecks(tc)
	if err != nil {
		return out, err
	}
	for _, ch := range checks {
		if ch.HTTP != nil {
			out.HTTP = append(out.HTTP, HTTPCheck{URL: ch.HTTP.URL, Expect: ch.HTTP.Expect})
		}
	}
	return out, nil
}
