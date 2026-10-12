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
	CategoryDrift     Category = "drift"
	CategoryFailover  Category = "failover"
	CategoryDeps      Category = "dependencies"
	CategoryGitOps    Category = "gitops"
	CategoryStatus    Category = "status"
	CategoryIncident  Category = "incidents"
	CategoryAssistant Category = "assistant"
	CategoryRisk      Category = "risk"
	CategoryDocs      Category = "docs"
	CategoryKube      Category = "kubernetes"
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
	{CategoryDrift, "Drift"},
	{CategoryFailover, "Failovertests"},
	{CategoryDeps, "Afhankelijkheden"},
	{CategoryGitOps, "GitOps"},
	{CategoryStatus, "Status"},
	{CategoryIncident, "Incidenten"},
	{CategoryAssistant, "Assistent"},
	{CategoryRisk, "Gezondheid"},
	{CategoryDocs, "Documentatie"},
	{CategoryKube, "Kubernetes"},
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

	"cluster.created":                  {CategoryInventory, "Cluster aangemaakt"},
	"cluster.updated":                  {CategoryInventory, "Cluster gewijzigd"},
	"cluster.deleted":                  {CategoryInventory, "Cluster verwijderd"},
	"cluster.deployed":                 {CategoryInventory, "Cluster uitgerold"},
	"cluster.spec_changed":             {CategoryInventory, "Specificatie van cluster gewijzigd"},
	"cluster.recovery_targets_updated": {CategoryBackups, "RTO- en RPO-doelen van cluster gewijzigd"},
	"secret.created":                   {CategoryInventory, "Geheim van cluster opgeslagen"},
	"cluster.database_added":           {CategoryInventory, "Database toegevoegd aan cluster"},
	"cluster.database_applied":         {CategoryInventory, "Database van cluster uitgerold"},
	"cluster.database_removed":         {CategoryInventory, "Database weggehaald uit ClusterForge"},
	"cluster.database_recovering":      {CategoryInventory, "Database van cluster opnieuw gestart"},
	"cluster.database_recovered":       {CategoryInventory, "Database van cluster draait weer na opnieuw starten"},
	"cluster.database_auto_recover":    {CategoryInventory, "Vanzelf opnieuw starten van database gewijzigd"},
	"cluster.database_created":         {CategoryInventory, "Database met gebruiker aangemaakt"},
	"cluster.database_dropped":         {CategoryInventory, "Database weggehaald"},
	"cluster.database_password":        {CategoryInventory, "Nieuw wachtwoord voor databasegebruiker"},
	"cluster.database_user_dropped":    {CategoryInventory, "Databasegebruiker weggehaald"},
	"node.created":                     {CategoryInventory, "Node toegevoegd"},
	"node.updated":                     {CategoryInventory, "Node gewijzigd"},
	"node.deleted":                     {CategoryInventory, "Node verwijderd"},
	"vip.created":                      {CategoryInventory, "VIP toegevoegd"},
	"vip.updated":                      {CategoryInventory, "VIP gewijzigd"},
	"vip.deleted":                      {CategoryInventory, "VIP verwijderd"},

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

	"proxmox.created":               {CategoryProxmox, "Proxmox gekoppeld"},
	"proxmox.updated":               {CategoryProxmox, "Proxmox-koppeling gewijzigd"},
	"proxmox.deleted":               {CategoryProxmox, "Proxmox-koppeling verwijderd"},
	"proxmox.sync_requested":        {CategoryProxmox, "Proxmox-sync gevraagd"},
	"proxmox.sync_failed":           {CategoryProxmox, "Proxmox niet bereikbaar"},
	"proxmox.sync_recovered":        {CategoryProxmox, "Proxmox weer bereikbaar"},
	"proxmox.host_offline":          {CategoryProxmox, "Proxmox-host offline"},
	"proxmox.host_online":           {CategoryProxmox, "Proxmox-host weer online"},
	"proxmox.self_location_set":     {CategoryProxmox, "VM van ClusterForge ingevuld"},
	"proxmox.self_location_cleared": {CategoryProxmox, "Ingevulde VM van ClusterForge weggehaald"},
	"vm.status_changed":             {CategoryProxmox, "Status van VM gewijzigd"},
	"vm.moved":                      {CategoryProxmox, "VM verhuisd"},
	"vm.missing":                    {CategoryProxmox, "VM verdwenen uit Proxmox"},
	"node.link_proposed":            {CategoryProxmox, "Koppeling van node aan VM voorgesteld"},
	"node.link_confirmed":           {CategoryProxmox, "Voorgestelde koppeling bevestigd"},
	"node.link_ignored":             {CategoryProxmox, "Voorgestelde koppeling genegeerd"},

	"backup.fresh":                      {CategoryBackups, "Back-up weer vers"},
	"backup.stale":                      {CategoryBackups, "Back-up te oud"},
	"backup.missing":                    {CategoryBackups, "Back-up ontbreekt"},
	"backup.inventory_failed":           {CategoryBackups, "Back-ups niet te lezen"},
	"backup.inventory_recovered":        {CategoryBackups, "Back-ups weer te lezen"},
	"backup.policy_updated":             {CategoryBackups, "Back-upbeleid gewijzigd"},
	"backup.watch_updated":              {CategoryBackups, "Lijst ook bewaken gewijzigd"},
	"backup.sandbox_created":            {CategoryBackups, "Back-up teruggezet in een sandbox"},
	"backup.sandbox_destroyed":          {CategoryBackups, "Sandbox verwijderd"},
	"backup.sandbox_destroy_failed":     {CategoryBackups, "Sandbox niet te verwijderen"},
	"backup.sandbox_connection_refused": {CategoryBackups, "Tweede agentverbinding geweigerd tijdens een back-upcontrole"},
	"backup.verify_finished":            {CategoryBackups, "Back-upcontrole klaar"},
	"backup.db_dumped":                  {CategoryBackups, "Dump van database gemaakt"},
	"backup.db_dump_suspect":            {CategoryBackups, "Dump van database verdacht"},
	"backup.db_dump_failed":             {CategoryBackups, "Dump van database mislukt"},
	"backup.db_restored":                {CategoryBackups, "Dump teruggezet in een nieuwe database"},
	"backup.db_restore_failed":          {CategoryBackups, "Dump terugzetten mislukt"},
	"backup.db_settings":                {CategoryBackups, "Nachtelijke dump van databases ingesteld"},

	"drift.detected":              {CategoryDrift, "Drift gevonden"},
	"drift.changed":               {CategoryDrift, "Drift veranderd"},
	"drift.resolved":              {CategoryDrift, "Drift verdwenen"},
	"drift.check_failed":          {CategoryDrift, "Driftcontrole mislukt"},
	"drift.ignore_added":          {CategoryDrift, "Drift genegeerd"},
	"drift.ignore_removed":        {CategoryDrift, "Negeerregel opgeheven"},
	"drift.baseline_set":          {CategoryDrift, "Baseline vastgelegd"},
	"drift.remediation_requested": {CategoryDrift, "Herstel van drift aangevraagd"},

	"failover_test.created":   {CategoryFailover, "Failovertest aangemaakt"},
	"failover_test.updated":   {CategoryFailover, "Failovertest gewijzigd"},
	"failover_test.deleted":   {CategoryFailover, "Failovertest verwijderd"},
	"failover.fault_injected": {CategoryFailover, "Storing van een failovertest veroorzaakt"},
	"failover.fault_cleared":  {CategoryFailover, "Storing van een failovertest opgeheven"},
	"failover.finished":       {CategoryFailover, "Failovertest klaar"},

	"disaster_scenario.created": {CategoryFailover, "Rampoefening aangemaakt"},
	"disaster_scenario.updated": {CategoryFailover, "Rampoefening gewijzigd"},
	"disaster_scenario.deleted": {CategoryFailover, "Rampoefening verwijderd"},
	"disaster.started":          {CategoryFailover, "Rampoefening gestart"},
	"disaster.fault_injected":   {CategoryFailover, "Storing van een rampoefening veroorzaakt"},
	"disaster.fault_cleared":    {CategoryFailover, "Storing van een rampoefening opgeheven"},
	"disaster.finished":         {CategoryFailover, "Rampoefening klaar"},
	"dr.started":                {CategoryBackups, "DR-repetitie gestart"},
	"dr.aborted":                {CategoryBackups, "DR-repetitie afgebroken door de bewaking"},
	"dr.finished":               {CategoryBackups, "DR-repetitie klaar"},

	"service.created":        {CategoryDeps, "Dienst toegevoegd"},
	"service.updated":        {CategoryDeps, "Dienst gewijzigd"},
	"service.deleted":        {CategoryDeps, "Dienst verwijderd"},
	"dependency.created":     {CategoryDeps, "Afhankelijkheid toegevoegd"},
	"dependency.updated":     {CategoryDeps, "Afhankelijkheid gewijzigd"},
	"dependency.deleted":     {CategoryDeps, "Afhankelijkheid verwijderd"},
	"service.status_changed": {CategoryDeps, "Status van dienst gewijzigd"},

	"gitops.repo_connected":    {CategoryGitOps, "Git-repository gekoppeld"},
	"gitops.repo_updated":      {CategoryGitOps, "Koppeling met Git gewijzigd"},
	"gitops.repo_disconnected": {CategoryGitOps, "Git-repository ontkoppeld"},
	"gitops.commit_seen":       {CategoryGitOps, "Nieuwe commit gezien"},
	"gitops.sync_failed":       {CategoryGitOps, "Lezen uit Git mislukt"},
	"gitops.sync_recovered":    {CategoryGitOps, "Lezen uit Git lukt weer"},
	"gitops.file_invalid":      {CategoryGitOps, "Clusterbestand ongeldig"},
	"gitops.file_missing":      {CategoryGitOps, "Clusterbestand ontbreekt"},
	"gitops.change_planned":    {CategoryGitOps, "Wijziging uit Git gepland"},
	"gitops.change_superseded": {CategoryGitOps, "Wijziging uit Git vervangen"},
	"gitops.change_approved":   {CategoryGitOps, "Wijziging uit Git goedgekeurd"},
	"gitops.change_rejected":   {CategoryGitOps, "Wijziging uit Git afgewezen"},
	"gitops.change_applied":    {CategoryGitOps, "Wijziging uit Git toegepast"},
	"gitops.change_failed":     {CategoryGitOps, "Wijziging uit Git mislukt"},
	"gitops.reapply_requested": {CategoryGitOps, "Revisie opnieuw toepassen"},
	"cluster.git_linked":       {CategoryGitOps, "Cluster aan Git gekoppeld"},
	"cluster.git_unlinked":     {CategoryGitOps, "Cluster van Git ontkoppeld"},

	"node.status_changed":    {CategoryStatus, "Status van node gewijzigd"},
	"cluster.status_changed": {CategoryStatus, "Status van cluster gewijzigd"},
	"vip.owner_changed":      {CategoryStatus, "VIP verhuisd"},

	"cluster.database_down":      {CategoryStatus, "Database van cluster weg"},
	"cluster.database_up":        {CategoryStatus, "Database van cluster draait weer"},
	"cluster.database_node_out":  {CategoryStatus, "Databasenode doet niet mee"},
	"cluster.database_node_back": {CategoryStatus, "Databasenode doet weer mee"},

	"incident.opened":       {CategoryIncident, "Incident geopend"},
	"incident.escalated":    {CategoryIncident, "Incident erger geworden"},
	"incident.resolved":     {CategoryIncident, "Incident opgelost"},
	"incident.reopened":     {CategoryIncident, "Incident opnieuw geopend"},
	"incident.note_updated": {CategoryIncident, "Notitie bij incident bijgewerkt"},
	"incident.analyzed":     {CategoryIncident, "Incident geanalyseerd door de assistent"},

	"assistant.settings_updated": {CategoryAssistant, "Instellingen van de assistent gewijzigd"},
	"assistant.asked":            {CategoryAssistant, "Vraag aan de assistent"},
	"assistant.budget_reached":   {CategoryAssistant, "Maandbudget van de assistent op"},

	"risk.grade_changed":      {CategoryRisk, "Cijfer van de health score gewijzigd"},
	"risk.accepted":           {CategoryRisk, "Bevinding geaccepteerd"},
	"risk.acceptance_removed": {CategoryRisk, "Acceptatie ingetrokken"},

	"docs.target_connected":    {CategoryDocs, "GitHub-repository voor de documentatie gekoppeld"},
	"docs.target_updated":      {CategoryDocs, "Koppeling van de documentatie met GitHub gewijzigd"},
	"docs.target_disconnected": {CategoryDocs, "GitHub-repository voor de documentatie ontkoppeld"},
	"docs.published":           {CategoryDocs, "Documentatie naar GitHub geschreven"},
	"docs.publish_failed":      {CategoryDocs, "Documentatie naar GitHub schrijven mislukt"},
	"docs.publish_recovered":   {CategoryDocs, "Documentatie naar GitHub schrijven lukt weer"},
	"docs.summary_updated":     {CategoryDocs, "Samenvatting van de documentatie geschreven door de assistent"},
	"docs.summary_removed":     {CategoryDocs, "Samenvatting van de documentatie weggehaald"},

	"kube.scaled":             {CategoryKube, "App geschaald in Kubernetes"},
	"kube.restarted":          {CategoryKube, "App herstart in Kubernetes"},
	"kube.rolled_back":        {CategoryKube, "App teruggedraaid in Kubernetes"},
	"kube.deleted":            {CategoryKube, "Object weggehaald in Kubernetes"},
	"kube.cordoned":           {CategoryKube, "Node op cordon gezet in Kubernetes"},
	"kube.uncordoned":         {CategoryKube, "Node krijgt weer pods in Kubernetes"},
	"kube.drained":            {CategoryKube, "Node leeggemaakt in Kubernetes"},
	"kube.applied":            {CategoryKube, "YAML toegepast in Kubernetes"},
	"kube.node_removed":       {CategoryKube, "Worker weggehaald uit Kubernetes"},
	"kube.rolled_out":         {CategoryKube, "Nieuwe image uitgerold in Kubernetes"},
	"kube.app_status_changed": {CategoryKube, "Status van app in Kubernetes gewijzigd"},
	"kube.access_issued":      {CategoryKube, "Kubeconfig voor kubectl uitgegeven"},
	"kube.access_revoked":     {CategoryKube, "Kubeconfig voor kubectl ingetrokken"},

	"kube.snapshot_saved":           {CategoryKube, "etcd-snapshot gemaakt in Kubernetes"},
	"kube.snapshot_restored":        {CategoryKube, "etcd-snapshot teruggezet in Kubernetes"},
	"kube.snapshot_restore_started": {CategoryKube, "Terugzetten van etcd-snapshot gestart in Kubernetes"},
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
