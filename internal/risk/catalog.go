package risk

// Checks is de catalogus van de bevindingen: per check een korte naam. Een
// test zoekt in de broncode naar elke check en faalt als hij hier ontbreekt.
var Checks = map[string]string{
	"availability.status":    "Status nu",
	"availability.uptime":    "Beschikbaarheid over 30 dagen",
	"availability.incidents": "Veel incidenten",
	"availability.vip_moves": "VIP wisselt vaak",

	"backups.missing":        "Geen back-up",
	"backups.stale":          "Back-up te oud",
	"backups.pbs_failed":     "Verificatie in Proxmox Backup Server mislukt",
	"backups.verify_failed":  "Back-upcontrole mislukt",
	"backups.verify_warning": "Back-upcontrole met waarschuwing",
	"backups.unverified":     "Back-ups niet gecontroleerd",

	"recovery.no_test":     "Geen failovertest",
	"recovery.never_run":   "Failovertest liep nooit",
	"recovery.failed":      "Failovertest mislukt",
	"recovery.goal_missed": "Doel van de failovertest niet gehaald",
	"recovery.stale":       "Failovertest te lang geleden",
	"recovery.unrestored":  "Test niet hersteld",

	"redundancy.same_host":           "Alle nodes op één host",
	"redundancy.vip_same_host":       "Nodes van een VIP op één host",
	"redundancy.single_node":         "Maar één node",
	"redundancy.maintenance":         "Lang in onderhoud",
	"redundancy.nonprod_dependency":  "Prod hangt af van lab of test",
	"configuration.no_desired_state": "Geen gewenste staat",
	"configuration.drift":            "Drift",
	"configuration.ignore_all":       "Alle drift genegeerd",
	"configuration.revision_pending": "Revisie niet toegepast",
	"configuration.git_failed":       "Git-wijziging mislukt",

	"maintenance.security_updates": "Beveiligingsupdates open",
	"maintenance.apt_lists":        "Pakketlijsten oud",
	"maintenance.reboot":           "Herstart nodig",
	"maintenance.os_eol":           "Besturingssysteem end-of-life",
	"maintenance.certificate":      "Certificaat verloopt",
	"maintenance.agent_old":        "Agent verouderd",

	"capacity.disk":           "Schijf vol",
	"capacity.disk_trend":     "Schijf binnenkort vol",
	"capacity.memory":         "Geheugen krap",
	"capacity.cpu":            "CPU krap",
	"capacity.swap":           "Veel swap",
	"capacity.network_errors": "Netwerkfouten",

	"platform.admin_totp":          "Beheerder zonder tweestapsverificatie",
	"platform.proxmox_unreachable": "Proxmox niet te lezen",
	"platform.proxmox_certificate": "Certificaat van Proxmox verloopt",
}

// CardLabels zijn de namen van de kaarten.
var CardLabels = map[CardID]string{
	Availability: "Beschikbaarheid", Backups: "Back-ups", Recovery: "Herstelbaarheid", Redundancy: "Redundantie",
	Configuration: "Configuratie", Maintenance: "Onderhoud en beveiliging", Capacity: "Capaciteit", Platform: "Platform",
}
