package httpapi

import "strings"

// Wat kun je doen (fase 41): bij elke regel in Aandacht nodig een korte
// uitleg in gewone taal, zodat ook wie de techniek niet kent weet wat de
// volgende stap is. De sleutel is het id of het begin ervan.
var attentionHelp = []struct{ prefix, help string }{
	{"clusters-split", "Twee servers denken allebei dat zij het gedeelde adres (VIP) hebben. Bezoekers komen dan willekeurig bij de ene of de andere. Open het cluster en kijk welke node offline of afgesloten van het netwerk is; meestal lost een herstart van keepalived op die node het op."},
	{"clusters-down", "Het cluster werkt niet meer: de dienst is voor gebruikers onbereikbaar. Open het cluster; bovenaan staat welke node of dienst faalt. Begin bij de node die offline is of rood staat."},
	{"clusters-degraded", "Het cluster werkt nog, maar met minder reserve: als nog een node uitvalt, ligt de dienst plat. Open het cluster en breng de node die rood of oranje staat terug."},
	{"agents-offline", "ClusterForge hoort niets meer van deze servers. Staan ze aan? Werkt het netwerk? Op de pagina van de node zie je sinds wanneer. Een server die je bewust uitzette, zet je in onderhoud; dan verdwijnt deze melding."},
	{"drift", "Iemand of iets veranderde de instellingen op de server, buiten ClusterForge om. Open de node bij Afwijkingen: daar zie je wat er anders is en kun je de gewenste staat terugzetten of de nieuwe aanvaarden."},
	{"incidents", "Een incident is een storing die ClusterForge vaststelde. Open het: daar staat wat er gebeurde, wat er kort voordien veranderde en wat je kunt doen."},
	{"agents-upgrade-failed", "De nieuwe versie van de agent kon niet geïnstalleerd worden. Bij Instellingen → Agents bijwerken zie je de fout; probeer opnieuw. De oude agent blijft intussen gewoon werken."},
	{"agents-outdated", "Er is een nieuwere agent. Bijwerken is veilig en duurt een paar seconden per server: Instellingen → Agents bijwerken."},
	{"jobs-failed", "Een taak is iets dat ClusterForge voor je uitvoert. Open Taken en klik op de mislukte taak: onderaan staat wat er misging en meestal kun je ze opnieuw proberen."},
	{"git-pending", "Er staat een wijziging in Git die nog niet toegepast is. Open GitOps, bekijk wat er verandert en keur goed of af."},
	{"soc", "Het beveiligingsoverzicht ziet verdachte activiteit. Open SOC: bovenaan staat waarom en welke toestellen betrokken zijn. Twijfel je, blokkeer dan eerst en onderzoek daarna."},
	{"security-network", "Een scan vond een ernstig lek in een dienst die op het netwerk bereikbaar is. Open de bevinding: daar staat welke update of instelling het oplost."},
	{"security-kev", "Er staat software met een lek dat aanvallers nu actief misbruiken. Installeer de updates voor deze nodes zo snel mogelijk (Updates)."},
	{"security-malware", "Een virusscanner vond iets verdachts. Open de bevinding en controleer het bestand. Is het echt, haal de node dan van het netwerk en herinstalleer hem."},
	{"security-ssh-waiting", "Je wijzigde de SSH-instellingen. Kun je nog inloggen, bevestig dan; anders zet de node vanzelf de oude instellingen terug zodat je niet buitengesloten raakt."},
	{"docker-problems", "Een container (een app in Docker) draait niet of is ongezond. Open Containers: bij de container zie je de logboeken; herstarten lost het vaak op."},
	{"docker-root", "Deze containers kunnen de hele server overnemen als ze gehackt worden. Haal 'privileged' of de Docker-socket weg als de app die niet echt nodig heeft."},
	{"apps-", "De applicatie op dit cluster meldt een probleem. Open het cluster bij Applicatie: daar staat wat er mis is en welke knop het oplost."},
	{"pbs-", "Iets aan de back-upserver (Proxmox Backup Server) vraagt aandacht. Zonder werkende back-ups kun je niets terugzetten na een fout. Open Backup Server voor de details."},
	{"truenas-", "Iets aan de opslag (TrueNAS) vraagt aandacht, zoals een schijf die slijt of een pool die vol raakt. Open TrueNAS voor de details."},
	{"zabbix-", "De monitoring met Zabbix ziet een probleem. Open Zabbix: daar staat welke server en wat er mis is."},
	{"opnsense-", "De firewall (OPNsense) vraagt aandacht. Open OPNsense voor de details; wijzigingen via ClusterForge maken eerst een herstelpunt."},
	{"publish-", "Iets aan je websites naar buiten (NPM, de reverse proxy, of Authentik, de login) vraagt aandacht. Open Publicatie voor de details."},
	{"totp-off", "Tweestapsverificatie beschermt je account met een code op je gsm, ook als iemand je wachtwoord kent. Zet ze aan bij Instellingen; het duurt een minuut."},
	{"disk-crit", "De schijf is (bijna) vol. Als ze helemaal vol raakt, stoppen databases en diensten met schrijven en kunnen ze vastlopen. Open de node bij Schijfruimte en klik 'Kijk wat ruimte inneemt'; daarna maak je met één knop veilig ruimte vrij."},
	{"disk-warn", "De schijf raakt vol. Nog geen probleem, maar ruim op voor het er een wordt: open de node bij Schijfruimte, laat meten wat ruimte inneemt en maak veilig ruimte vrij. Lukt dat niet genoeg, maak de schijf groter in Proxmox."},
}

func helpFor(id string) string {
	for _, h := range attentionHelp {
		if id == h.prefix || (strings.HasSuffix(h.prefix, "-") && strings.HasPrefix(id, h.prefix)) {
			return h.help
		}
	}
	return ""
}
