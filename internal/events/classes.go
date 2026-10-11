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
	"node.created": ClassInfo, "node.updated": ClassChange, "node.deleted": ClassInfo,
	"vip.created": ClassChange, "vip.updated": ClassChange, "vip.deleted": ClassChange,

	"node.lifecycle_changed": ClassChange,

	"job.queued": ClassChange, "job.succeeded": ClassChange, "job.failed": ClassChange, "job.canceled": ClassInfo,
	"job.cancel_requested": ClassInfo, "job.retried": ClassChange,

	"agent.enrolled": ClassChange, "agent.enroll_failed": ClassInfo, "agent.command": ClassChange,
	"agent.revoked": ClassChange, "enrollment_token.created": ClassInfo, "enrollment_token.deleted": ClassInfo,
	"node.facts_changed": ClassInfo,

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
	"backup.verify_finished": ClassInfo,

	"drift.detected": ClassChange, "drift.changed": ClassChange, "drift.resolved": ClassInfo,
	"drift.check_failed": ClassInfo, "drift.ignore_added": ClassInfo, "drift.ignore_removed": ClassInfo,
	"drift.baseline_set": ClassInfo, "drift.remediation_requested": ClassChange,

	"failover_test.created": ClassInfo, "failover_test.updated": ClassInfo, "failover_test.deleted": ClassInfo,
	"failover.fault_injected": ClassChange, "failover.fault_cleared": ClassChange, "failover.finished": ClassInfo,

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
	"incident.reopened": ClassInfo, "incident.note_updated": ClassInfo,

	"assistant.settings_updated": ClassInfo, "assistant.asked": ClassInfo, "assistant.budget_reached": ClassInfo,
	"risk.grade_changed": ClassInfo, "risk.accepted": ClassInfo, "risk.acceptance_removed": ClassInfo,
	"docs.target_connected": ClassInfo, "docs.target_updated": ClassInfo, "docs.target_disconnected": ClassInfo,
	"docs.published": ClassInfo, "docs.publish_failed": ClassInfo, "docs.publish_recovered": ClassInfo,
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
	case "node.status_changed", "cluster.status_changed":
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
