package zabbixmon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/events"
)

// --- onderhoud naar Zabbix (M6) ----------------------------------------------------------
//
// Staat een node in ClusterForge in onderhoud, dan zet ClusterForge in Zabbix
// een onderhoud op zijn host ("met dataverzameling": Zabbix blijft meten,
// maar meldt geen problemen). Komt de node uit onderhoud, dan haalt
// ClusterForge het weer weg. Alleen onderhouden die ClusterForge zelf maakte,
// komen ooit weg; een onderhoud verloopt ook vanzelf na MaintenanceFor, en
// zolang de node in onderhoud blijft verlengt ClusterForge het.

// MaintenanceFor: zo lang staat een onderhoud in Zabbix; ClusterForge
// verlengt als er minder dan MaintenanceRenew over is.
const (
	MaintenanceFor   = 7 * 24 * time.Hour
	MaintenanceRenew = 2 * 24 * time.Hour
)

// MaintenanceSettings zijn de instellingen met de onderhouden van nu.
type MaintenanceSettings struct {
	Sync      bool              `json:"sync"`
	UpdatedAt time.Time         `json:"updated_at"`
	UpdatedBy *string           `json:"updated_by"`
	Error     string            `json:"error"`
	Active    []MaintenanceNode `json:"active"`
}

// MaintenanceNode is een onderhoud dat ClusterForge in Zabbix zette.
type MaintenanceNode struct {
	NodeID        uuid.UUID `json:"node_id"`
	Hostname      string    `json:"hostname"`
	HostID        string    `json:"hostid"`
	MaintenanceID string    `json:"maintenanceid"`
	ActiveTill    time.Time `json:"active_till"`
	CreatedAt     time.Time `json:"created_at"`
}

// Maintenance geeft de instellingen en de onderhouden die ClusterForge zette.
func (s *Service) Maintenance(ctx context.Context) (MaintenanceSettings, error) {
	var m MaintenanceSettings
	if err := s.pool.QueryRow(ctx, `SELECT st.maintenance_sync, st.updated_at, u.username
		FROM zabbix_monitor_settings st LEFT JOIN users u ON u.id = st.updated_by`).Scan(&m.Sync, &m.UpdatedAt, &m.UpdatedBy); err != nil {
		return MaintenanceSettings{}, err
	}
	rows, err := s.pool.Query(ctx, `SELECT m.node_id, n.hostname, m.hostid, m.maintenanceid, m.active_till, m.created_at
		FROM zabbix_maintenances m JOIN nodes n ON n.id = m.node_id ORDER BY lower(n.hostname)`)
	if err != nil {
		return MaintenanceSettings{}, err
	}
	defer rows.Close()
	m.Active = []MaintenanceNode{}
	for rows.Next() {
		var r MaintenanceNode
		if err := rows.Scan(&r.NodeID, &r.Hostname, &r.HostID, &r.MaintenanceID, &r.ActiveTill, &r.CreatedAt); err != nil {
			return MaintenanceSettings{}, err
		}
		m.Active = append(m.Active, r)
	}
	s.mu.RLock()
	m.Error = s.maintErr
	s.mu.RUnlock()
	return m, rows.Err()
}

// SetMaintenanceSync zet het doorzetten aan of uit. Uit haalt bij de
// volgende ronde de onderhouden van ClusterForge weg.
func (s *Service) SetMaintenanceSync(ctx context.Context, actor events.Actor, userID *uuid.UUID, on bool) error {
	var was bool
	if err := s.pool.QueryRow(ctx, `SELECT maintenance_sync FROM zabbix_monitor_settings`).Scan(&was); err != nil {
		return err
	}
	if was == on {
		return nil
	}
	if _, err := s.pool.Exec(ctx, `UPDATE zabbix_monitor_settings SET maintenance_sync = $1, updated_at = now(), updated_by = $2`, on, userID); err != nil {
		return err
	}
	s.writeEvent(ctx, events.Event{Actor: actor, SubjectType: "zabbixmon", SubjectID: "settings", Action: "zabbix.settings_changed",
		Payload: map[string]any{"maintenance_sync": on}})
	return nil
}

type trackedMaintenance struct {
	nodeID        uuid.UUID
	hostid        string
	maintenanceid string
	activeTill    time.Time
}

// syncMaintenance brengt de onderhouden in Zabbix in lijn met de nodes in
// onderhoud. Fouten komen in het overzicht; de rest van de ronde gaat door.
func (s *Service) syncMaintenance(ctx context.Context, api API, nodes []CFNode, snap *Snapshot, now time.Time) error {
	var on bool
	if err := s.pool.QueryRow(ctx, `SELECT maintenance_sync FROM zabbix_monitor_settings`).Scan(&on); err != nil {
		return err
	}
	rows, err := s.pool.Query(ctx, `SELECT node_id, hostid, maintenanceid, active_till FROM zabbix_maintenances`)
	if err != nil {
		return err
	}
	tracked := map[uuid.UUID]trackedMaintenance{}
	wasTracked := map[uuid.UUID]bool{}
	for rows.Next() {
		var t trackedMaintenance
		if err := rows.Scan(&t.nodeID, &t.hostid, &t.maintenanceid, &t.activeTill); err != nil {
			rows.Close()
			return err
		}
		tracked[t.nodeID] = t
		wasTracked[t.nodeID] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Gewenst: elke node in onderhoud met een Zabbix-host.
	want := map[uuid.UUID]string{}
	byID := map[uuid.UUID]CFNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	if on {
		for _, h := range snap.Hosts {
			if h.Node == nil || !h.Enabled {
				continue
			}
			if n, ok := byID[h.Node.NodeID]; ok && n.Lifecycle == "maintenance" {
				if _, dup := want[n.ID]; !dup {
					want[n.ID] = h.HostID
				}
			}
		}
	}

	var errs []string
	for id, t := range tracked {
		host, keep := want[id]
		if keep && host == t.hostid && t.activeTill.Sub(now) > MaintenanceRenew {
			continue
		}
		n := byID[id]
		if keep && host == t.hostid {
			// Verlengen: hetzelfde onderhoud, nieuwe einddatum. Zo is er geen
			// ogenblik zonder onderhoud.
			till, err := s.updateMaintenance(ctx, api, t.maintenanceid, host, now)
			if err == nil {
				if _, err := s.pool.Exec(ctx, `UPDATE zabbix_maintenances SET active_till = $2 WHERE node_id = $1`, id, till); err != nil {
					errs = append(errs, err.Error())
				}
				continue
			}
			if !gone(err) {
				errs = append(errs, n.Hostname+": "+friendlyWrite(err).Error())
				continue
			}
			// Iemand haalde het in Zabbix weg: hieronder een nieuw.
		}
		if err := s.deleteMaintenance(ctx, api, t); err != nil {
			errs = append(errs, n.Hostname+": "+err.Error())
			continue
		}
		if !keep {
			s.writeEvent(ctx, events.Event{Actor: events.System(), SubjectType: "node", SubjectID: id.String(), ClusterID: n.ClusterID,
				Action: "zabbix.maintenance_ended", Payload: map[string]any{"hostid": t.hostid, "maintenanceid": t.maintenanceid, "hostname": n.Hostname}})
		}
		delete(tracked, id)
	}
	for id, host := range want {
		if _, ok := tracked[id]; ok {
			continue
		}
		n := byID[id]
		mid, till, err := s.createMaintenance(ctx, api, n, host, now)
		if err != nil {
			errs = append(errs, n.Hostname+": "+err.Error())
			continue
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO zabbix_maintenances (node_id, hostid, maintenanceid, active_till) VALUES ($1, $2, $3, $4)
			ON CONFLICT (node_id) DO UPDATE SET hostid = excluded.hostid, maintenanceid = excluded.maintenanceid, active_till = excluded.active_till`,
			id, host, mid, till); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		// Een nieuw onderhoud na een wissel van host of een weggehaald
		// onderhoud is geen nieuw begin.
		if _, was := wasTracked[id]; !was {
			s.writeEvent(ctx, events.Event{Actor: events.System(), SubjectType: "node", SubjectID: id.String(), ClusterID: n.ClusterID,
				Action: "zabbix.maintenance_started", Payload: map[string]any{"hostid": host, "maintenanceid": mid, "hostname": n.Hostname}})
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("onderhoud naar Zabbix: %s", strings.Join(errs, "; "))
	}
	return nil
}

// maintenanceName is uniek in Zabbix; het stukje node-id houdt twee nodes met
// dezelfde naam uit elkaar.
func maintenanceName(n CFNode) string {
	return fmt.Sprintf("ClusterForge: %s (%s)", n.Hostname, n.ID.String()[:8])
}

// maintenanceWindow: van nu (op de minuut) tot MaintenanceFor verder.
func maintenanceWindow(hostid string, now time.Time) (map[string]any, time.Time) {
	since := now.Truncate(time.Minute)
	till := since.Add(MaintenanceFor)
	return map[string]any{
		"active_since":     since.Unix(),
		"active_till":      till.Unix(),
		"maintenance_type": 0,
		"hosts":            []map[string]string{{"hostid": hostid}},
		"timeperiods":      []map[string]any{{"timeperiod_type": 0, "start_date": since.Unix(), "period": int(MaintenanceFor.Seconds())}},
	}, till
}

// gone: het onderhoud bestaat in Zabbix niet meer.
func gone(err error) bool {
	return err != nil && strings.Contains(err.Error(), "No permissions to referred object or it does not exist")
}

func (s *Service) createMaintenance(ctx context.Context, api API, n CFNode, hostid string, now time.Time) (string, time.Time, error) {
	params, till := maintenanceWindow(hostid, now)
	params["name"] = maintenanceName(n)
	params["description"] = "Gezet door ClusterForge: de node staat daar in onderhoud. ClusterForge haalt het weg zodra de node uit onderhoud komt."
	var out struct {
		IDs []zint `json:"maintenanceids"`
	}
	err := api.Call(ctx, "maintenance.create", params, &out)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		// Een onderhoud van ClusterForge dat hij uit het oog verloor (bv. de
		// node werd verwijderd en opnieuw gekoppeld): overnemen.
		var found []struct {
			ID zint `json:"maintenanceid"`
		}
		if gerr := api.Call(ctx, "maintenance.get", map[string]any{"output": []string{"maintenanceid"}, "filter": map[string]any{"name": params["name"]}}, &found); gerr != nil || len(found) == 0 {
			return "", time.Time{}, friendlyWrite(err)
		}
		mid := fmt.Sprint(int64(found[0].ID))
		till, uerr := s.updateMaintenance(ctx, api, mid, hostid, now)
		if uerr != nil {
			return "", time.Time{}, friendlyWrite(uerr)
		}
		return mid, till, nil
	}
	if err != nil {
		return "", time.Time{}, friendlyWrite(err)
	}
	if len(out.IDs) == 0 {
		return "", time.Time{}, errors.New("zabbix gaf geen maintenanceid terug")
	}
	return fmt.Sprint(int64(out.IDs[0])), till, nil
}

// updateMaintenance zet een nieuw venster (en de host) op een bestaand onderhoud.
func (s *Service) updateMaintenance(ctx context.Context, api API, mid, hostid string, now time.Time) (time.Time, error) {
	params, till := maintenanceWindow(hostid, now)
	params["maintenanceid"] = mid
	var out struct {
		IDs []zint `json:"maintenanceids"`
	}
	if err := api.Call(ctx, "maintenance.update", params, &out); err != nil {
		return time.Time{}, err
	}
	return till, nil
}

func (s *Service) deleteMaintenance(ctx context.Context, api API, t trackedMaintenance) error {
	var out struct {
		IDs []zint `json:"maintenanceids"`
	}
	err := api.Call(ctx, "maintenance.delete", []string{t.maintenanceid}, &out)
	// Iemand haalde het in Zabbix al weg: ook goed.
	if err != nil && !gone(err) {
		return friendlyWrite(err)
	}
	_, derr := s.pool.Exec(ctx, `DELETE FROM zabbix_maintenances WHERE node_id = $1`, t.nodeID)
	return derr
}
