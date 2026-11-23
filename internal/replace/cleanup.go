package replace

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/noderun"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

type cleanupParams struct {
	ID uuid.UUID `json:"id"`
	// Storage voor de vzdump; leeg is de standaard van de host.
	Storage string `json:"storage,omitempty"`
}

// StartCleanup ruimt de oude installatie op: eerst een kopie, dan uitzetten.
// Je tikt de naam of het adres van de oude installatie over.
func (s *Service) StartCleanup(ctx context.Context, actor events.Actor, id uuid.UUID, confirm, storage string) (store.Job, error) {
	r, err := s.Get(ctx, id)
	if err != nil {
		return store.Job{}, err
	}
	if r.State != StateActive && r.State != StateKept {
		return store.Job{}, ValidationError{"deze oude installatie is al opgeruimd of wordt opgeruimd"}
	}
	c := strings.TrimSpace(confirm)
	if c == "" || (c != r.OldAddress && (r.OldHostname == "" || c != r.OldHostname)) {
		return store.Job{}, ValidationError{fmt.Sprintf("tik %s over om op te ruimen", or(r.OldHostname, r.OldAddress))}
	}
	if r.OldNodeID == nil {
		return store.Job{}, ValidationError{"ClusterForge kent de oude installatie alleen als adres (geen node met een agent); ruim ze zelf op en zet ze hier op gehouden"}
	}
	if !r.Guest && !r.OldOnline {
		return store.Job{}, ValidationError{"de oude node is geen Proxmox-gast en zijn agent is offline: ClusterForge kan geen kopie maken"}
	}
	if storage != "" && !validStorage(storage) {
		return store.Job{}, ValidationError{"ongeldige storage"}
	}
	cid := r.ClusterID
	j, err := s.jobs.Enqueue(ctx, jobs.Spec{Kind: KindCleanup, Title: "Oude installatie opruimen: " + or(r.OldHostname, r.OldAddress), Actor: actor,
		ClusterID: &cid, NodeID: r.OldNodeID, Params: cleanupParams{ID: id, Storage: storage}})
	if err != nil {
		return store.Job{}, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE replacements SET state = 'opruimen', cleanup_job = $2, updated_at = now() WHERE id = $1`, id, j.ID); err != nil {
		return j, err
	}
	s.event(ctx, actor, "replace.cleanup_requested", map[string]any{"id": id.String(), "old_address": r.OldAddress, "job_id": j.ID.String()})
	return j, nil
}

func validStorage(s string) bool {
	if len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// TarScript maakt op een losse machine een kopie van de instellingen en de
// data van de diensten.
const TarScript = `set -e
f=/var/backups/clusterforge-oud-$(date +%Y%m%d-%H%M%S).tar.gz
mkdir -p /var/backups
dirs=
for d in /etc /opt /srv /root /var/lib/docker/volumes /var/lib/cf-*; do [ -e "$d" ] && dirs="$dirs $d"; done
echo "Kopie van:$dirs"
tar --one-file-system --exclude=/var/backups -czf "$f" $dirs 2>/dev/null || [ $? -eq 1 ]
[ -s "$f" ] || { echo "kopie mislukt"; exit 1; }
echo "Kopie: $f ($(du -h "$f" | cut -f1))"
`

func (s *Service) cleanup(ctx context.Context, j *jobs.Job) (err error) {
	var p cleanupParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	defer func() {
		state := StateDone
		if err != nil {
			state = StateActive
		}
		note := ""
		if err != nil {
			note = "opruimen mislukt: " + err.Error()
		}
		_, _ = s.pool.Exec(context.WithoutCancel(ctx), `UPDATE replacements SET state = $2, note = $3, updated_at = now() WHERE id = $1`, p.ID, state, note)
	}()
	r, err := s.Get(ctx, p.ID)
	if err != nil {
		return err
	}
	var conn *uuid.UUID
	var vmid *int32
	if err := s.pool.QueryRow(ctx, `SELECT proxmox_id, pve_vmid FROM nodes WHERE id = $1`, r.OldNodeID).Scan(&conn, &vmid); err != nil {
		return err
	}
	var host uuid.UUID
	var pveNode string
	var guest protocol.PVEGuest
	if conn != nil && vmid != nil {
		if s.PVE == nil {
			return errors.New("geen Proxmox-hosts met een agent")
		}
		if host, pveNode, guest, err = s.PVE.FindGuest(ctx, *conn, int(*vmid)); err != nil {
			return fmt.Errorf("de oude gast %d zoeken: %w", *vmid, err)
		}
	}
	if err := j.Step(ctx, "Kopie vooraf", func(ctx context.Context, st *jobs.Step) error {
		if guest.VMID != 0 {
			params := map[string]string{"vmid": strconv.Itoa(guest.VMID), "mode": "snapshot", "compress": "zstd"}
			if p.Storage != "" {
				params["storage"] = p.Storage
			}
			st.Logf("vzdump van %s %d (%s) op %s", guest.Type, guest.VMID, guest.Name, pveNode)
			res, err := s.PVE.Do(ctx, st, st, host, protocol.PVERun{Action: protocol.PVEAPI,
				API: &protocol.PVEAPICall{Method: "create", Path: "/nodes/" + pveNode + "/vzdump", Params: params}}, "vzdump")
			if err != nil {
				return err
			}
			if res.State != protocol.RunOK {
				return errors.New("vzdump: " + noderun.Describe(res))
			}
			return nil
		}
		res, err := s.Run.Do(ctx, st, *r.OldNodeID, protocol.RunSpec{ID: "cf-oud-kopie-" + j.ID.String(), Kind: protocol.RunScript, Title: "kopie",
			Script: TarScript, TimeoutSeconds: int(time.Hour.Seconds())})
		if err != nil {
			return err
		}
		if res.State != protocol.RunOK {
			return errors.New(noderun.Describe(res))
		}
		return nil
	}); err != nil {
		return err
	}
	if err := j.Step(ctx, "Oude installatie uitzetten", func(ctx context.Context, st *jobs.Step) error {
		if guest.VMID == 0 {
			st.Logf("%s is geen Proxmox-gast: ClusterForge zet een losse machine niet zelf uit. De kopie staat in /var/backups; zet de machine uit wanneer het past.", or(r.OldHostname, r.OldAddress))
			return nil
		}
		if guest.Status != "running" {
			st.Logf("%s %d staat al uit", guest.Type, guest.VMID)
		} else {
			res, err := s.PVE.Do(ctx, st, st, host, protocol.PVERun{Action: protocol.PVEShutdown, Type: guest.Type, VMID: guest.VMID}, "afsluiten")
			if err != nil {
				return err
			}
			if res.State != protocol.RunOK {
				return errors.New("afsluiten: " + noderun.Describe(res))
			}
		}
		res, err := s.PVE.Do(ctx, st, st, host, protocol.PVERun{Action: protocol.PVESet, Type: guest.Type, VMID: guest.VMID,
			Set: &protocol.PVESetting{OnBoot: ptr(false)}}, "niet meer automatisch starten")
		if err == nil && res.State == protocol.RunOK {
			st.Logf("Start niet meer vanzelf bij het opstarten van de host.")
		}
		st.Logf("De gast blijft staan (uit); weggooien doe je zelf bij Proxmox → Gasten, wanneer je wil.")
		return nil
	}); err != nil {
		return err
	}
	return j.Step(ctx, "Node uit dienst", func(ctx context.Context, st *jobs.Step) error {
		if _, err := s.pool.Exec(ctx, `UPDATE nodes SET lifecycle = 'decommissioned' WHERE id = $1 AND cluster_id IS NULL`, r.OldNodeID); err != nil {
			return err
		}
		st.Logf("%s staat uit dienst bij Nodes.", or(r.OldHostname, r.OldAddress))
		s.event(ctx, actorOf(j), "replace.cleaned", map[string]any{"id": r.ID.String(), "old_address": r.OldAddress})
		return nil
	})
}

func ptr[T any](v T) *T { return &v }
