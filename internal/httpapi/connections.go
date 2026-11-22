package httpapi

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/deps"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// GetNodeConnections geeft de verbindingen van een node van de afgelopen
// 24 uur, met waar ze heen gaan. Inkomend van een adres dat geen node is,
// staat al samengeteld in de database.
func (s *Server) GetNodeConnections(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	ctx := r.Context()
	if _, err := s.q.GetNode(ctx, id); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "node niet gevonden")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := gen.NodeConnections{Out: []gen.NodeConnOut{}, In: []gen.NodeConnIn{}}
	if a, err := s.q.GetActiveAgentByNode(ctx, id); err == nil {
		out.Supported = a.ProtocolVersion >= protocol.JournalSince
	} else if !errors.Is(err, pgx.ErrNoRows) {
		s.internalError(w, r, err)
		return
	}
	rows, err := s.q.ListObservedConnections(ctx, store.ListObservedConnectionsParams{Since: s.deps.Now().Add(-deps.Window), NodeID: &id})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	g, err := s.deps.Load(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	nets, err := s.deps.Nets(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	skip := s.deps.SkipFor(ctx)
	for _, row := range rows {
		if row.Direction == "in" {
			in := gen.NodeConnIn{
				Unit: row.Unit, Port: int(row.LocalPort), PeerNodeId: nullable.NewNullNullable[uuid.UUID](),
				Count: row.Recent, FirstSeen: row.FirstSeen, LastSeen: row.LastSeen,
			}
			if peer, ok := nets.NodeAt(row.PeerAddr); ok {
				in.PeerNodeId = nullable.NewNullableWithValue(peer)
				if n, ok := g.Node(peer); ok {
					in.PeerHostname = n.Hostname
				}
			}
			out.In = append(out.In, in)
			continue
		}
		c := deps.ConnOf(row)
		d := g.Dest(nets, c, skip)
		o := gen.NodeConnOut{
			Unit: row.Unit, Addr: row.PeerAddr, Port: int(row.PeerPort), Count: row.Recent, FirstSeen: row.FirstSeen, LastSeen: row.LastSeen,
			Dest: gen.NodeConnOutDest(d.Kind), DestLabel: g.DestLabel(d),
			FromServiceId: nullable.NewNullNullable[uuid.UUID](), ServiceId: nullable.NewNullNullable[uuid.UUID](),
			NodeId: nullable.NewNullNullable[uuid.UUID](), ClusterId: nullableOf(d.ClusterID),
			DependencyId: nullable.NewNullNullable[uuid.UUID](), DependencyState: nullable.NewNullNullable[gen.ServiceState](),
		}
		if d.NodeID != uuid.Nil {
			o.NodeId = nullable.NewNullableWithValue(d.NodeID)
		}
		from, hasFrom := g.Origin(c)
		if hasFrom {
			o.FromServiceId = nullable.NewNullableWithValue(from.ID)
		}
		if d.Service != nil {
			o.ServiceId = nullable.NewNullableWithValue(d.Service.ID)
			if dep, ok := g.Dependency(from.ID, d.Service.ID); hasFrom && ok {
				o.DependencyId = nullable.NewNullableWithValue(dep.ID)
				o.DependencyState = nullable.NewNullableWithValue(gen.ServiceState(dep.State))
			}
		}
		out.Out = append(out.Out, o)
	}
	writeJSON(w, http.StatusOK, out)
}
