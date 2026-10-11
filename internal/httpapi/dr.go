package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/Jonasz1996/clusterforge/internal/backups"
	"github.com/Jonasz1996/clusterforge/internal/disaster"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

func (s *Server) SetRecoveryTargets(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	p, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req gen.RecoveryTargetsInput
	if !decode(w, r, &req) {
		return
	}
	c, err := s.backups.SetRecoveryTargets(r.Context(), events.User(p.User.ID), id, int32Of(req.RtoMinutes), int32Of(req.RpoHours))
	if s.backupError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, toAPIRecoveryTargets(disaster.TargetsOf(c)))
}

// int32Of leest een getal dat leeg mag zijn.
func int32Of(v nullable.Nullable[int]) *int32 {
	if !v.IsSpecified() || v.IsNull() {
		return nil
	}
	n := int32(v.MustGet())
	return &n
}

func toAPIRecoveryTargets(t disaster.RecoveryTargets) gen.RecoveryTargets {
	return gen.RecoveryTargets{
		RtoMinutes: t.RTOMinutes, RpoHours: t.RPOHours, DefaultRtoMinutes: t.DefaultRTOMinutes, DefaultRpoHours: t.DefaultRPOHours, Custom: t.Custom,
	}
}

// toAPIRehearsalPlan is de voorcontrole van een DR-repetitie.
func toAPIRehearsalPlan(p backups.RehearsalPlan) gen.DRPlan {
	out := gen.DRPlan{
		ClusterName: p.ClusterName, Environment: p.Environment, Bridge: p.Bridge, Connection: p.ConnectionName, Host: p.Host,
		Storage: p.Storage, Nodes: make([]gen.DRPlanNode, 0, len(p.Nodes)), Vips: make([]gen.DisasterVIP, 0, len(p.VIPs)),
		RtoTargetMinutes: p.RTOTargetMinutes, RpoTargetHours: p.RPOTargetHours, RpoSeconds: p.RPO.Seconds(), SpreadSeconds: p.Spread.Seconds(),
		Fix: orEmpty(p.Fix),
	}
	for _, n := range p.Nodes {
		out.Nodes = append(out.Nodes, gen.DRPlanNode{
			NodeId: n.NodeID, Hostname: n.Hostname, SourceVmid: n.SourceVMID, Volid: n.Volid, BackupTime: n.BackupTime,
			BackupStorage: n.BackupStorage, SizeBytes: n.Size, MemoryBytes: n.Memory, DiskBytes: n.Disk, Address: n.Address,
		})
	}
	for _, v := range p.VIPs {
		out.Vips = append(out.Vips, gen.DisasterVIP{Address: v.Address, Probe: toAPIProbe(v.Probe)})
	}
	return out
}

func toAPIChecks(cs []backups.Check) []gen.TestRunCheck {
	out := make([]gen.TestRunCheck, 0, len(cs))
	for _, c := range cs {
		out = append(out, gen.TestRunCheck{Name: c.Name, Ok: c.OK, Detail: c.Detail, Warning: c.Warning, Code: c.Code})
	}
	return out
}

// rehearsalReport zet het rapport van een DR-repetitie in out. Tijdens de
// run staat het plan er, met de metingen tot nu toe leeg.
func rehearsalReport(run store.TestRun, out *gen.TestRun) {
	var d backups.RehearsalDefinition
	_ = json.Unmarshal(run.Definition, &d)
	var m backups.RehearsalMeasurements
	_ = json.Unmarshal(run.Measurements, &m)
	rep := gen.DRReport{
		ScenarioId: d.ScenarioID, Name: d.Name, ClusterName: d.ClusterName, Environment: d.Environment, Bridge: d.Bridge,
		Connection: d.ConnectionName, Host: d.Host, Storage: d.Storage, Fix: orEmpty(d.Fix),
		RtoTargetMinutes: d.RTOTargetMinutes, RpoTargetHours: d.RPOTargetHours,
		RtoSeconds: nullableOf(m.RTOSeconds), RpoSeconds: nullableOf(m.RPOSeconds), RtoMet: nullableOf(m.RTOMet), RpoMet: nullableOf(m.RPOMet),
		RestoreSeconds: nullableOf(m.RestoreSeconds), BootSeconds: nullableOf(m.BootSeconds), CheckSeconds: nullableOf(m.CheckSeconds),
		CleanupSeconds: nullableOf(m.CleanupSeconds), TotalSeconds: nullableOf(m.TotalSeconds),
		Nodes: make([]gen.DRReportNode, 0, len(d.Nodes)), Vips: make([]gen.DRReportVIP, 0, len(d.VIPs)),
		DestroyedAt: nullableOf(m.DestroyedAt), Aborted: m.Aborted,
	}
	for _, n := range d.Nodes {
		rn := gen.DRReportNode{
			NodeId: n.NodeID, Hostname: n.Hostname, SourceVmid: n.SourceVMID, Volid: n.Volid, BackupTime: n.BackupTime,
			SandboxVmid: nullable.NewNullNullable[int](), RestoreSeconds: nullable.NewNullNullable[float64](),
			BootSeconds: nullable.NewNullNullable[float64](), Addresses: []string{},
		}
		for _, x := range m.Nodes {
			if x.NodeID != n.NodeID {
				continue
			}
			if x.SandboxVMID != 0 {
				rn.SandboxVmid = nullable.NewNullableWithValue(x.SandboxVMID)
			}
			rn.RestoreSeconds, rn.BootSeconds = nullableOf(x.RestoreSeconds), nullableOf(x.BootSeconds)
			rn.Addresses, rn.AgentVersion, rn.Destroyed = orEmpty(x.Addresses), x.AgentVersion, x.Destroyed
		}
		rep.Nodes = append(rep.Nodes, rn)
	}
	for _, v := range d.VIPs {
		rv := gen.DRReportVIP{Address: v.Address, Holders: []string{}, Probe: v.Probe.String(), ProbeOk: nullable.NewNullNullable[bool]()}
		for _, x := range m.VIPs {
			if x.Address == v.Address {
				rv.Holders, rv.Checked, rv.ProbeOk = orEmpty(x.Holders), x.Checked, nullableOf(x.ProbeOK)
			}
		}
		rep.Vips = append(rep.Vips, rv)
	}
	out.Rehearsal = nullable.NewNullableWithValue(rep)
}
