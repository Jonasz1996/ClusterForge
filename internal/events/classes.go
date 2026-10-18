package events

// Class zegt wat een regel in de tijdlijn van What Broke betekent: een
// verandering die oorzaak kan zijn, een symptoom van een storing, of alleen
// informatie.
type Class string

const (
	ClassChange  Class = "change"
	ClassSymptom Class = "symptom"
	ClassInfo    Class = "info"
)

// classes geeft elke action in de catalogus een klasse. Een test faalt als
// er een ontbreekt. Voor een paar actions hangt het af van de payload; zie
// ClassOf.
var classes = map[string]Class{
	"auth.login": ClassInfo, "auth.login_failed": ClassInfo, "auth.logout": ClassInfo,
	"auth.password_changed": ClassInfo, "auth.totp_enabled": ClassInfo, "auth.totp_disabled": ClassInfo,
	"auth.reauth_failed": ClassInfo, "auth.totp_setup_started": ClassInfo, "auth.rate_limited": ClassInfo,
	"user.created": ClassInfo, "user.disabled": ClassInfo, "user.enabled": ClassInfo, "user.role_changed": ClassInfo,
	"user.totp_reset": ClassInfo, "user.sessions_revoked": ClassInfo, "audit.exported": ClassInfo,
	"audit.throttled": ClassInfo,

	"cluster.created": ClassInfo, "cluster.updated": ClassChange, "cluster.deleted": ClassInfo,
	"cluster.deployed": ClassChange, "cluster.spec_changed": ClassChange, "secret.created": ClassChange,
	"cluster.database_added": ClassInfo, "cluster.database_applied": ClassChange, "cluster.database_removed": ClassInfo,
	"cluster.database_recovering": ClassChange, "cluster.database_recovered": ClassChange, "cluster.database_auto_recover": ClassChange,
	"cluster.database_resynced": ClassChange, "cluster.database_switchover": ClassChange, "cluster.database_reinitialized": ClassChange,
	"cluster.database_settings": ClassChange, "cluster.database_updated": ClassChange, "cluster.database_node_added": ClassChange,
	"cluster.database_down": ClassSymptom, "cluster.database_up": ClassInfo, "cluster.database_node_out": ClassSymptom,
	"cluster.database_node_back": ClassInfo, "cluster.database_restarted": ClassChange, "cluster.database_failover": ClassSymptom,
	"cluster.database_state_transfer": ClassInfo,
	"cluster.database_created":        ClassChange, "cluster.database_dropped": ClassChange, "cluster.database_password": ClassChange,
	"cluster.database_user_dropped": ClassChange, "cluster.database_trial_ok": ClassInfo, "cluster.database_trial_failed": ClassInfo,
	"cluster.database_not_added": ClassInfo, "cluster.database_retired": ClassChange,
	"cluster.database_move_copied": ClassChange, "cluster.database_move_failed": ClassInfo, "cluster.database_moved": ClassChange,
	"cluster.database_move_cancelled": ClassChange, "cluster.database_locked": ClassChange, "cluster.database_unlocked": ClassChange,
	"cluster.heal_started": ClassChange, "cluster.heal_done": ClassChange, "cluster.heal_failed": ClassInfo,
	"cluster.heal_auto_changed": ClassChange, "cluster.heal_auto_paused": ClassInfo,
	"node.heal_started": ClassChange, "node.heal_done": ClassChange, "node.heal_failed": ClassInfo,
	"node.created": ClassInfo, "node.updated": ClassChange, "node.deleted": ClassInfo,
	"vip.created": ClassChange, "vip.updated": ClassChange, "vip.deleted": ClassChange,

	"node.lifecycle_changed": ClassChange,

	"job.queued": ClassChange, "job.succeeded": ClassChange, "job.failed": ClassChange, "job.canceled": ClassInfo,
	"job.cancel_requested": ClassInfo, "job.retried": ClassChange,

	"agent.enrolled": ClassChange, "agent.enroll_failed": ClassInfo, "agent.command": ClassChange,
	"agent.revoked": ClassChange, "enrollment_token.created": ClassInfo, "enrollment_token.deleted": ClassInfo,
	"node.facts_changed": ClassInfo,
	"node.cron_added":    ClassChange, "node.cron_removed": ClassChange, "node.cron_changed": ClassChange,
	"node.cron_failed": ClassSymptom, "node.cron_recovered": ClassInfo, "node.cron_logging": ClassChange,
	"node.cron_disabled": ClassChange, "node.cron_enabled": ClassChange,
	"cron_job.created": ClassChange, "cron_job.updated": ClassChange, "cron_job.enabled": ClassChange, "cron_job.disabled": ClassChange,
	"cron_job.deleted": ClassChange, "cron_job.run_requested": ClassChange, "cron_job.run_failed": ClassSymptom, "cron_job.run_stopped": ClassChange,
	"cron_job.dry_run": ClassInfo,

	"proxmox.created": ClassInfo, "proxmox.updated": ClassInfo, "proxmox.deleted": ClassInfo,
	"proxmox.sync_requested": ClassInfo, "proxmox.sync_failed": ClassSymptom, "proxmox.sync_recovered": ClassInfo,
	"proxmox.host_offline": ClassChange, "proxmox.host_online": ClassInfo,
	"proxmox.self_location_set": ClassInfo, "proxmox.self_location_cleared": ClassInfo,
	"node.link_proposed": ClassInfo, "node.link_confirmed": ClassChange, "node.link_ignored": ClassInfo,
	"vm.status_changed": ClassChange, "vm.moved": ClassChange, "vm.missing": ClassChange,

	"backup.fresh": ClassInfo, "backup.stale": ClassInfo, "backup.missing": ClassInfo,
	"backup.inventory_failed": ClassInfo, "backup.inventory_recovered": ClassInfo, "backup.policy_updated": ClassInfo,
	"backup.watch_updated": ClassInfo, "backup.sandbox_created": ClassInfo, "backup.sandbox_destroyed": ClassInfo,
	"backup.sandbox_destroy_failed": ClassInfo, "backup.sandbox_connection_refused": ClassInfo,
	"backup.verify_finished": ClassInfo, "backup.db_dumped": ClassInfo, "backup.db_dump_suspect": ClassInfo,
	"backup.db_dump_failed": ClassInfo, "backup.db_restored": ClassChange, "backup.db_restore_failed": ClassInfo,
	"backup.db_settings": ClassInfo,

	"drift.detected": ClassChange, "drift.changed": ClassChange, "drift.resolved": ClassInfo,
	"drift.check_failed": ClassInfo, "drift.ignore_added": ClassInfo, "drift.ignore_removed": ClassInfo,
	"drift.baseline_set": ClassInfo, "drift.remediation_requested": ClassChange,

	"failover_test.created": ClassInfo, "failover_test.updated": ClassInfo, "failover_test.deleted": ClassInfo,
	"failover.fault_injected": ClassChange, "failover.fault_cleared": ClassChange, "failover.finished": ClassInfo,
	"disaster_scenario.created": ClassInfo, "disaster_scenario.updated": ClassInfo, "disaster_scenario.deleted": ClassInfo,
	"disaster.started": ClassInfo, "disaster.fault_injected": ClassChange, "disaster.fault_cleared": ClassChange,
	"disaster.finished": ClassInfo,
	"dr.started":        ClassInfo, "dr.aborted": ClassInfo, "dr.finished": ClassInfo, "cluster.recovery_targets_updated": ClassInfo,

	"service.created": ClassInfo, "service.updated": ClassInfo, "service.deleted": ClassInfo,
	"dependency.created": ClassInfo, "dependency.updated": ClassInfo, "dependency.deleted": ClassInfo,
	"service.status_changed": ClassSymptom,

	"gitops.repo_connected": ClassInfo, "gitops.repo_updated": ClassInfo, "gitops.repo_disconnected": ClassInfo,
	"gitops.commit_seen": ClassInfo, "gitops.sync_failed": ClassInfo, "gitops.sync_recovered": ClassInfo,
	"gitops.file_invalid": ClassInfo, "gitops.file_missing": ClassInfo, "gitops.change_planned": ClassInfo,
	"gitops.change_superseded": ClassInfo, "gitops.change_approved": ClassInfo, "gitops.change_rejected": ClassInfo,
	"gitops.change_applied": ClassChange, "gitops.change_failed": ClassChange, "gitops.reapply_requested": ClassChange,
	"cluster.git_linked": ClassInfo, "cluster.git_unlinked": ClassInfo,

	"node.status_changed": ClassSymptom, "cluster.status_changed": ClassSymptom, "vip.owner_changed": ClassSymptom,

	"incident.opened": ClassInfo, "incident.escalated": ClassInfo, "incident.resolved": ClassInfo,
	"incident.reopened": ClassInfo, "incident.note_updated": ClassInfo, "incident.analyzed": ClassInfo,

	"assistant.settings_updated": ClassInfo, "assistant.asked": ClassInfo, "assistant.budget_reached": ClassInfo,
	"risk.grade_changed": ClassInfo, "risk.accepted": ClassInfo, "risk.acceptance_removed": ClassInfo,
	"docs.target_connected": ClassInfo, "docs.target_updated": ClassInfo, "docs.target_disconnected": ClassInfo,
	"docs.published": ClassInfo, "docs.publish_failed": ClassInfo, "docs.publish_recovered": ClassInfo,
	"docs.summary_updated": ClassInfo, "docs.summary_removed": ClassInfo,

	"kube.scaled": ClassChange, "kube.restarted": ClassChange, "kube.rolled_back": ClassChange, "kube.deleted": ClassChange,
	"kube.cordoned": ClassChange, "kube.uncordoned": ClassChange, "kube.drained": ClassChange, "kube.applied": ClassChange,
	"kube.node_removed": ClassChange, "kube.rolled_out": ClassChange, "kube.app_status_changed": ClassSymptom,
	"kube.access_issued": ClassInfo, "kube.access_revoked": ClassInfo, "kube.snapshot_saved": ClassInfo,
	"kube.snapshot_restored": ClassChange, "kube.snapshot_restore_started": ClassChange,
}

// HasClass is true als de action een klasse heeft.
func HasClass(action string) bool {
	_, ok := classes[action]
	return ok
}

// ClassOf geeft de klasse van een event. Een statusovergang is alleen een
// symptoom als het erger wordt; terug naar gezond is informatie. Een VIP dat
// verhuist is informatie, een VIP zonder eigenaar een symptoom.
func ClassOf(action string, payload map[string]any) Class {
	c, ok := classes[action]
	if !ok {
		return ClassInfo
	}
	to, _ := payload["to"].(string)
	switch action {
	case "node.status_changed", "cluster.status_changed", "kube.app_status_changed":
		if !Bad(to) {
			return ClassInfo
		}
	case "service.status_changed":
		impact, _ := payload["impact"].(string)
		if !Bad(to) && (impact == "" || impact == "none") {
			return ClassInfo
		}
	case "vip.owner_changed":
		if payload["to"] != nil {
			return ClassInfo
		}
	}
	return c
}

// Bad is true voor een status die niet gezond is.
func Bad(status string) bool {
	return status == "degraded" || status == "down" || status == "split_brain"
}
