package events

import "sort"

// Category is de soort van een event in het logboek. Elke action hoort bij
// precies één soort.
type Category string

const (
	CategorySecurity  Category = "security"
	CategoryInventory Category = "inventory"
	CategoryLifecycle Category = "lifecycle"
	CategoryJobs      Category = "jobs"
	CategoryAgents    Category = "agents"
	CategoryProxmox   Category = "proxmox"
	CategoryBackups   Category = "backups"
	CategoryStatus    Category = "status"
)

// Categories staan in de volgorde van het filter in de webinterface.
var Categories = []struct {
	Key   Category
	Label string
}{
	{CategorySecurity, "Beveiliging"},
	{CategoryInventory, "Inventory"},
	{CategoryLifecycle, "Nodebeheer"},
	{CategoryJobs, "Taken"},
	{CategoryAgents, "Agents"},
	{CategoryProxmox, "Proxmox"},
	{CategoryBackups, "Back-ups"},
	{CategoryStatus, "Status"},
}

// Spec beschrijft een action: zijn soort en een korte Nederlandse omschrijving
// voor als het logboek geen eigen zin heeft.
type Spec struct {
	Category Category
	Label    string
}

// Known is de catalogus van alle actions die ClusterForge schrijft. Een test
// zoekt in de broncode naar elke action en faalt als hij hier ontbreekt.
var Known = map[string]Spec{
	"auth.login":              {CategorySecurity, "Ingelogd"},
	"auth.login_failed":       {CategorySecurity, "Mislukte inlogpoging"},
	"auth.logout":             {CategorySecurity, "Uitgelogd"},
	"auth.password_changed":   {CategorySecurity, "Wachtwoord gewijzigd"},
	"auth.totp_enabled":       {CategorySecurity, "Tweestapsverificatie aangezet"},
	"auth.totp_disabled":      {CategorySecurity, "Tweestapsverificatie uitgezet"},
	"auth.reauth_failed":      {CategorySecurity, "Bevestiging met wachtwoord of code mislukt"},
	"auth.totp_setup_started": {CategorySecurity, "Instellen van tweestapsverificatie gestart"},
	"auth.rate_limited":       {CategorySecurity, "Loginlimiet bereikt"},
	"user.created":            {CategorySecurity, "Gebruiker aangemaakt"},
	// Voor het gebruikersbeheer dat later komt.
	"user.disabled":         {CategorySecurity, "Gebruiker uitgeschakeld"},
	"user.enabled":          {CategorySecurity, "Gebruiker weer ingeschakeld"},
	"user.role_changed":     {CategorySecurity, "Rol van gebruiker gewijzigd"},
	"user.totp_reset":       {CategorySecurity, "Tweestapsverificatie van gebruiker gewist"},
	"user.sessions_revoked": {CategorySecurity, "Sessies van gebruiker beëindigd"},
	"audit.exported":        {CategorySecurity, "Logboek geëxporteerd"},
	"audit.throttled":       {CategorySecurity, "Meldingen gedrosseld"},

	"cluster.created":      {CategoryInventory, "Cluster aangemaakt"},
	"cluster.updated":      {CategoryInventory, "Cluster gewijzigd"},
	"cluster.deleted":      {CategoryInventory, "Cluster verwijderd"},
	"cluster.deployed":     {CategoryInventory, "Cluster uitgerold"},
	"cluster.spec_changed": {CategoryInventory, "Specificatie van cluster gewijzigd"},
	"secret.created":       {CategoryInventory, "Geheim van cluster opgeslagen"},
	"node.created":         {CategoryInventory, "Node toegevoegd"},
	"node.updated":         {CategoryInventory, "Node gewijzigd"},
	"node.deleted":         {CategoryInventory, "Node verwijderd"},
	"vip.created":          {CategoryInventory, "VIP toegevoegd"},
	"vip.updated":          {CategoryInventory, "VIP gewijzigd"},
	"vip.deleted":          {CategoryInventory, "VIP verwijderd"},

	"node.lifecycle_changed": {CategoryLifecycle, "Lifecycle van node gewijzigd"},

	"job.queued":           {CategoryJobs, "Taak aangevraagd"},
	"job.succeeded":        {CategoryJobs, "Taak gelukt"},
	"job.failed":           {CategoryJobs, "Taak mislukt"},
	"job.canceled":         {CategoryJobs, "Taak geannuleerd"},
	"job.cancel_requested": {CategoryJobs, "Annuleren van taak gevraagd"},
	"job.retried":          {CategoryJobs, "Taak opnieuw gestart"},

	"agent.enrolled":           {CategoryAgents, "Agent aangemeld"},
	"agent.enroll_failed":      {CategoryAgents, "Aanmelding van agent geweigerd"},
	"agent.command":            {CategoryAgents, "Commando naar agent gestuurd"},
	"agent.revoked":            {CategoryAgents, "Agent ingetrokken"},
	"enrollment_token.created": {CategoryAgents, "Aanmeldtoken gemaakt"},
	"enrollment_token.deleted": {CategoryAgents, "Aanmeldtoken ingetrokken"},
	"node.facts_changed":       {CategoryAgents, "Facts van node gewijzigd"},

	"proxmox.created":        {CategoryProxmox, "Proxmox gekoppeld"},
	"proxmox.updated":        {CategoryProxmox, "Proxmox-koppeling gewijzigd"},
	"proxmox.deleted":        {CategoryProxmox, "Proxmox-koppeling verwijderd"},
	"proxmox.sync_requested": {CategoryProxmox, "Proxmox-sync gevraagd"},
	"proxmox.sync_failed":    {CategoryProxmox, "Proxmox niet bereikbaar"},
	"proxmox.sync_recovered": {CategoryProxmox, "Proxmox weer bereikbaar"},
	"vm.status_changed":      {CategoryProxmox, "Status van VM gewijzigd"},
	"vm.moved":               {CategoryProxmox, "VM verhuisd"},
	"vm.missing":             {CategoryProxmox, "VM verdwenen uit Proxmox"},

	"backup.fresh":               {CategoryBackups, "Back-up weer vers"},
	"backup.stale":               {CategoryBackups, "Back-up te oud"},
	"backup.missing":             {CategoryBackups, "Back-up ontbreekt"},
	"backup.inventory_failed":    {CategoryBackups, "Back-ups niet te lezen"},
	"backup.inventory_recovered": {CategoryBackups, "Back-ups weer te lezen"},
	"backup.policy_updated":      {CategoryBackups, "Back-upbeleid gewijzigd"},
	"backup.watch_updated":       {CategoryBackups, "Lijst ook bewaken gewijzigd"},

	"node.status_changed":    {CategoryStatus, "Status van node gewijzigd"},
	"cluster.status_changed": {CategoryStatus, "Status van cluster gewijzigd"},
	"vip.owner_changed":      {CategoryStatus, "VIP verhuisd"},
}

// Lookup geeft de spec van een action. Een onbekende action (uit een nieuwere
// of oudere versie) valt onder status, met de action zelf als omschrijving.
func Lookup(action string) Spec {
	if s, ok := Known[action]; ok {
		return s
	}
	return Spec{Category: CategoryStatus, Label: action}
}

// Actions geeft de actions van een soort, gesorteerd.
func Actions(c Category) []string {
	var out []string
	for a, s := range Known {
		if s.Category == c {
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}

// ValidCategory is true voor een soort uit Categories.
func ValidCategory(c string) bool {
	for _, x := range Categories {
		if string(x.Key) == c {
			return true
		}
	}
	return false
}
