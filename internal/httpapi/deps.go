package httpapi

import (
	"cmp"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/deps"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
)

func (s *Server) GetDependencyGraph(w http.ResponseWriter, r *http.Request, params gen.GetDependencyGraphParams) {
	g, err := s.deps.Load(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	f := deps.Filter{ClusterID: params.ClusterId, Suggested: deref(params.IncludeSuggested), Ignored: deref(params.IncludeIgnored)}
	if params.Environment != nil {
		f.Environment = string(*params.Environment)
	}
	level := gen.DependencyGraphLevelService
	if params.Level != nil {
		level = gen.DependencyGraphLevel(*params.Level)
	}
	svcs, ds := g.Select(f)
	out := gen.DependencyGraph{Level: level, Groups: []gen.DepGroup{}, Services: []gen.DepService{}, Edges: []gen.DepEdge{}}

	// Het voorstellen-tellertje telt binnen het filter, ook als de
	// voorstellen niet getoond worden.
	withSuggested, _ := g.Select(deps.Filter{ClusterID: f.ClusterID, Environment: f.Environment, Suggested: true})
	suggestions := map[string]int{}
	for _, sv := range g.Services {
		if sv.State == deps.StateSuggested {
			suggestions[g.GroupOf(sv).Key]++
		}
	}
	for _, sv := range withSuggested {
		if sv.State == deps.StateSuggested {
			out.Suggestions++
		}
	}

	seen := map[string]bool{}
	addGroup := func(gr deps.Group) {
		if seen[gr.Key] {
			return
		}
		seen[gr.Key] = true
		out.Groups = append(out.Groups, depGroup(g, gr, suggestions[gr.Key]))
	}
	if f.ClusterID != nil {
		c, ok := g.Cluster(*f.ClusterID)
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden")
			return
		}
		addGroup(g.GroupOf(deps.Svc{ClusterID: &c.ID}))
	}
	for _, sv := range svcs {
		addGroup(g.GroupOf(sv))
		if level == gen.DependencyGraphLevelService {
			out.Services = append(out.Services, depService(g, sv))
		}
	}
	if level == gen.DependencyGraphLevelCluster {
		for _, e := range g.Collapse(ds) {
			out.Edges = append(out.Edges, gen.DepEdge{
				Id: e.From + "|" + e.To, From: e.From, To: e.To, Strength: gen.DependencyStrength(e.Strength),
				Source: gen.ServiceSourceManual, State: gen.Confirmed, Affected: e.Affected, Count: e.Count, Note: "",
			})
		}
	} else {
		for _, d := range ds {
			out.Edges = append(out.Edges, depEdge(g, d))
		}
	}
	slices.SortFunc(out.Groups, func(a, b gen.DepGroup) int {
		return cmp.Or(cmp.Compare(groupRank(a), groupRank(b)), cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
	})
	writeJSON(w, http.StatusOK, out)
}

// groupRank zet clusters voor losse nodes en Extern achteraan.
func groupRank(g gen.DepGroup) int {
	return map[gen.DepGroupKind]int{gen.DepGroupKindCluster: 0, gen.DepGroupKindNode: 1, gen.DepGroupKindExternal: 2}[g.Kind]
}

func depGroup(g *deps.Graph, gr deps.Group, suggestions int) gen.DepGroup {
	imp, by := g.GroupImpact(gr.Key)
	out := gen.DepGroup{
		Id: gr.Key, Kind: gen.DepGroupKind(gr.Kind), Name: gr.Name, ClusterId: nullableOf(gr.ClusterID), NodeId: nullableOf(gr.NodeID),
		Environment: nullable.NewNullNullable[gen.Environment](), Status: gen.Status(gr.Status), StatusReason: gr.Reason,
		Impact: gen.ServiceImpact(imp), ImpactedBy: by, Suggestions: suggestions,
	}
	if out.Status == "" {
		out.Status = gen.StatusUnknown
	}
	if gr.Environment != "" {
		out.Environment = nullable.NewNullableWithValue(gen.Environment(gr.Environment))
	}
	return out
}

func depService(g *deps.Graph, sv deps.Svc) gen.DepService {
	own, e := g.Own(sv.ID), g.Effect(sv.ID)
	out := gen.DepService{
		Id: sv.ID, GroupId: g.GroupOf(sv).Key, ClusterId: nullableOf(sv.ClusterID), NodeId: nullableOf(sv.NodeID),
		Name: sv.Name, Kind: gen.ServiceKind(sv.Kind), Unit: sv.Unit, Port: nullable.NewNullNullable[int](), Address: sv.Address,
		Description: sv.Description, Source: gen.ServiceSource(sv.Source), State: gen.ServiceState(sv.State),
		Status: gen.ServiceStatus(own.Status), StatusReason: own.Reason, Impact: gen.ServiceImpact(e.Impact),
		ImpactPath: []string{}, CauseId: nullable.NewNullNullable[uuid.UUID](), Instances: []gen.DepInstance{},
		LastSeenAt: nullableOf(sv.LastSeenAt), CreatedAt: sv.CreatedAt, UpdatedAt: sv.UpdatedAt,
	}
	if sv.Port > 0 {
		out.Port = nullable.NewNullableWithValue(sv.Port)
	}
	for _, i := range own.Instances {
		out.Instances = append(out.Instances, gen.DepInstance{NodeId: i.NodeID, Hostname: i.Hostname, State: i.State, Running: i.Running})
	}
	if e.Impact != deps.ImpactNone {
		out.CauseId = nullable.NewNullableWithValue(e.Cause)
		for _, id := range e.Path {
			out.ImpactPath = append(out.ImpactPath, g.LabelFrom(sv.ID, id))
		}
		out.ImpactReason = g.LabelFrom(sv.ID, e.Cause) + " is down"
		if len(e.Path) > 2 {
			out.ImpactReason += ", via " + strings.Join(out.ImpactPath[1:len(out.ImpactPath)-1], " en ")
		}
	}
	return out
}

func depEdge(g *deps.Graph, d deps.Dep) gen.DepEdge {
	return gen.DepEdge{
		Id: d.ID.String(), From: d.From.String(), To: d.To.String(), Strength: gen.DependencyStrength(d.Strength),
		Source: gen.ServiceSource(d.Source), State: gen.ServiceState(d.State), Note: d.Note, Affected: g.Affected(d), Count: 1,
	}
}

func (s *Server) GetImpact(w http.ResponseWriter, r *http.Request, params gen.GetImpactParams) {
	n := 0
	for _, p := range []*uuid.UUID{params.ServiceId, params.ClusterId, params.NodeId} {
		if p != nil {
			n++
		}
	}
	if n != 1 {
		writeError(w, http.StatusBadRequest, "bad_request", "geef precies één van service_id, cluster_id en node_id")
		return
	}
	g, err := s.deps.Load(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var out gen.Impact
	t := deps.Target{Service: params.ServiceId, Cluster: params.ClusterId, Node: params.NodeId}
	switch {
	case t.Service != nil:
		sv, ok := g.Service(*t.Service)
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "dienst niet gevonden")
			return
		}
		out.Target.Kind, out.Target.Id, out.Target.Name = gen.ImpactTargetKindService, sv.ID, g.LabelFrom(uuid.Nil, sv.ID)
	case t.Cluster != nil:
		c, ok := g.Cluster(*t.Cluster)
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "cluster niet gevonden")
			return
		}
		out.Target.Kind, out.Target.Id, out.Target.Name = gen.ImpactTargetKindCluster, c.ID, c.Name
	default:
		nd, ok := g.Node(*t.Node)
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "node niet gevonden")
			return
		}
		out.Target.Kind, out.Target.Id, out.Target.Name = gen.ImpactTargetKindNode, nd.ID, nd.Hostname
	}
	out.Items, out.Groups = []gen.ImpactItem{}, []gen.ImpactGroup{}
	groups := map[string]int{}
	for _, h := range g.Impact(t) {
		sv, _ := g.Service(h.Service)
		gr := g.GroupOf(sv)
		it := gen.ImpactItem{
			ServiceId: sv.ID, Name: sv.Name, Kind: gen.ServiceKind(sv.Kind), GroupId: gr.Key, GroupName: gr.Name,
			Environment: nullable.NewNullNullable[gen.Environment](), Impact: gen.ServiceImpact(h.Impact), Direct: h.Direct,
			Reason: h.Reason, Path: []string{}, DependencyId: nullable.NewNullNullable[uuid.UUID](),
			Source: nullable.NewNullNullable[gen.ServiceSource](),
		}
		if gr.Environment != "" {
			it.Environment = nullable.NewNullableWithValue(gen.Environment(gr.Environment))
		}
		for _, id := range h.Path {
			it.Path = append(it.Path, g.LabelFrom(sv.ID, id))
		}
		if h.Edge != nil {
			it.DependencyId = nullable.NewNullableWithValue(h.Edge.ID)
			it.Source = nullable.NewNullableWithValue(gen.ServiceSource(h.Edge.Source))
		}
		out.Items = append(out.Items, it)
		// De items staan al met prod bovenaan; de groepen volgen die volgorde.
		i, ok := groups[gr.Key]
		if !ok {
			i = len(out.Groups)
			groups[gr.Key] = i
			out.Groups = append(out.Groups, gen.ImpactGroup{GroupId: gr.Key, Name: gr.Name, Environment: it.Environment, Impact: it.Impact})
		}
		out.Groups[i].Count++
		if it.Impact == gen.ServiceImpactDown {
			out.Groups[i].Impact = gen.ServiceImpactDown
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) CreateService(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.ServiceInput
	if !decode(w, r, &req) {
		return
	}
	in := deps.ServiceInput{
		ClusterID: req.ClusterId, NodeID: req.NodeId, Name: req.Name, Kind: string(req.Kind), Port: req.Port,
		Unit: deref(req.Unit), Address: deref(req.Address), Description: deref(req.Description),
	}
	svc, err := s.deps.CreateService(r.Context(), events.User(p.User.ID), in)
	if s.depsError(w, r, err) {
		return
	}
	s.writeService(w, r, http.StatusCreated, svc.ID)
}

func (s *Server) UpdateService(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.ServicePatch
	if !decode(w, r, &req) {
		return
	}
	patch := deps.ServicePatch{Name: req.Name, Unit: req.Unit, Address: req.Address, Description: req.Description}
	if req.Kind != nil {
		k := string(*req.Kind)
		patch.Kind = &k
	}
	if req.State != nil {
		st := string(*req.State)
		patch.State = &st
	}
	if req.Port.IsSpecified() {
		if req.Port.IsNull() {
			patch.ClearPort = true
		} else {
			v := req.Port.MustGet()
			patch.Port = &v
		}
	}
	_, err := s.deps.UpdateService(r.Context(), events.User(p.User.ID), id, patch)
	if s.depsError(w, r, err) {
		return
	}
	s.writeService(w, r, http.StatusOK, id)
}

func (s *Server) DeleteService(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if s.depsError(w, r, s.deps.DeleteService(r.Context(), events.User(p.User.ID), id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeService(w http.ResponseWriter, r *http.Request, status int, id uuid.UUID) {
	g, err := s.deps.Load(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	sv, ok := g.Service(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "dienst niet gevonden")
		return
	}
	writeJSON(w, status, depService(g, sv))
}

func (s *Server) CreateDependency(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.DependencyInput
	if !decode(w, r, &req) {
		return
	}
	in := deps.DependencyInput{From: req.FromServiceId, To: req.ToServiceId, Note: deref(req.Note)}
	if req.Strength != nil {
		in.Strength = string(*req.Strength)
	}
	d, err := s.deps.CreateDependency(r.Context(), events.User(p.User.ID), in)
	if s.depsError(w, r, err) {
		return
	}
	s.writeDependency(w, r, http.StatusCreated, d.ID)
}

func (s *Server) UpdateDependency(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.DependencyPatch
	if !decode(w, r, &req) {
		return
	}
	var strength *string
	if req.Strength != nil {
		st := string(*req.Strength)
		strength = &st
	}
	_, err := s.deps.UpdateDependency(r.Context(), events.User(p.User.ID), id, strength, req.Note)
	if s.depsError(w, r, err) {
		return
	}
	s.writeDependency(w, r, http.StatusOK, id)
}

func (s *Server) DeleteDependency(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if s.depsError(w, r, s.deps.DeleteDependency(r.Context(), events.User(p.User.ID), id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeDependency(w http.ResponseWriter, r *http.Request, status int, id uuid.UUID) {
	g, err := s.deps.Load(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, d := range g.Deps {
		if d.ID == id {
			writeJSON(w, status, depEdge(g, d))
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "afhankelijkheid niet gevonden")
}

// depsError schrijft de passende foutrespons en geeft true als err niet nil
// was.
func (s *Server) depsError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var fe *deps.FieldError
	var ce *deps.ConflictError
	switch {
	case errors.As(err, &fe):
		writeJSON(w, http.StatusBadRequest, gen.Error{Code: "validation", Message: fe.Message, Field: optional(fe.Field)})
	case errors.As(err, &ce):
		writeError(w, http.StatusConflict, "conflict", ce.Message)
	case errors.Is(err, deps.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "niet gevonden")
	default:
		s.internalError(w, r, err)
	}
	return true
}
