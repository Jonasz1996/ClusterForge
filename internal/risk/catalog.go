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

	"recovery.no_test":        "Geen failovertest",
	"recovery.never_run":      "Failovertest liep nooit",
	"recovery.failed":         "Failovertest mislukt",
	"recovery.goal_missed":    "Doel van de failovertest niet gehaald",
	"recovery.stale":          "Failovertest te lang geleden",
	"recovery.unrestored":     "Test niet hersteld",
	"recovery.dr_never_run":   "DR-repetitie liep nooit",
	"recovery.dr_failed":      "DR-repetitie mislukt",
	"recovery.dr_goal_missed": "DR-repetitie haalde haar doel niet",
	"recovery.dr_stale":       "DR-repetitie te lang geleden",

	"redundancy.same_host":           "Alle nodes op één host",
	"redundancy.vip_same_host":       "Nodes van een VIP op één host",
	"redundancy.single_node":         "Maar één node",
	"redundancy.maintenance":         "Lang in onderhoud",
	"redundancy.nonprod_dependency":  "Prod hangt af van lab of test",
	"redundancy.spof":                "Single point of failure",
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

	"kubernetes.single_server":    "Eén Kubernetes-server op prod",
	"kubernetes.snapshot_missing": "Geen etcd-snapshot",
	"kubernetes.snapshot_stale":   "etcd-snapshot te oud",
	"kubernetes.node_not_ready":   "Node NotReady",
	"kubernetes.restarts":         "Pods blijven herstarten",
	"kubernetes.single_pod":       "App op prod met één pod",
	"kubernetes.latest_image":     "Image zonder vaste versie",
	"kubernetes.memory_limit":     "Pods zonder geheugenlimiet",
	"kubernetes.version_eol":      "k3s zonder updates",

	"platform.admin_totp":          "Beheerder zonder tweestapsverificatie",
	"platform.proxmox_unreachable": "Proxmox niet te lezen",
	"platform.proxmox_certificate": "Certificaat van Proxmox verloopt",
	"platform.self_backup":         "Geen verse back-up van ClusterForge zelf",
	"platform.self_unknown":        "Niet bekend waar ClusterForge draait",
}

// CardLabels zijn de namen van de kaarten.
var CardLabels = map[CardID]string{
	Availability: "Beschikbaarheid", Backups: "Back-ups", Recovery: "Herstelbaarheid", Redundancy: "Redundantie",
	Configuration: "Configuratie", Maintenance: "Onderhoud en beveiliging", Capacity: "Capaciteit", Kubernetes: "Kubernetes",
	Platform: "Platform",
}
