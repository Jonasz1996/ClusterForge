package docs

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/cronexpr"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// maxFoundCrons is hoeveel gevonden jobs er hoogstens in het hoofdstuk komen.
const maxFoundCrons = 80

// cronOf zijn de cronjobs van cluster id: de beheerde, en de gevonden per
// job één keer met de nodes waarop hij staat. De timers van de beheerde
// jobs zelf staan er niet nog eens bij.
func cronOf(w *world, id uuid.UUID) ClusterCron {
	var out ClusterCron
	for _, j := range w.cronJobs {
		if j.ClusterID != id {
			continue
		}
		m := CronManaged{
			ID: j.ID.String(), Name: j.Name, Schedule: j.Schedule, Describe: describeCron(j.Schedule), RunOn: j.RunOn,
			User: j.UserName, Command: j.Command, TimeoutMin: int(j.TimeoutSeconds) / 60, Retries: int(j.Retries), Enabled: j.Enabled,
		}
		if j.NodeHostname != nil {
			m.Node = *j.NodeHostname
		}
		if j.Kind == protocol.CronKindSync {
			m.Sync = &CronSync{Source: j.SyncSource, Target: j.SyncTarget, ToCluster: j.SyncToClusterName, Delete: j.SyncDelete,
				Exclude: j.SyncExclude, BwLimitMB: int(j.SyncBwlimitMb)}
		}
		out.Managed = append(out.Managed, m)
	}
	byKey := map[string]int{}
	for _, n := range w.nodeCrons {
		if n.ClusterID == nil || *n.ClusterID != id || n.Clusterforge {
			continue
		}
		i, ok := byKey[n.Key]
		if !ok {
			i = len(out.Found)
			byKey[n.Key] = i
			out.Found = append(out.Found, CronFound{Source: n.Source, File: n.File, Line: int(n.Line), User: n.UserName, Schedule: n.Schedule,
				Describe: describeCron(n.Schedule), Command: n.Command, Package: n.Package})
		}
		f := &out.Found[i]
		f.Nodes = append(f.Nodes, n.Hostname)
		if !n.Enabled {
			f.Off = append(f.Off, n.Hostname)
		}
	}
	return out
}

// describeCron is een schema van vijf velden in gewone taal; anders leeg.
func describeCron(s string) string {
	sc, err := cronexpr.Parse(s)
	if err != nil {
		return ""
	}
	return sc.Describe()
}

var cronSourceText = map[string]string{
	"crontab": "/etc/crontab", "cron.d": "cron.d", "user": "crontab van de gebruiker", "periodic": "run-parts", "timer": "systemd-timer",
}

// cronjobs is het hoofdstuk Cronjobs: de beheerde jobs en syncs, wat er
// verder op de nodes staat, en runbooks om een run na te kijken en een sync
// terug te draaien.
func cronjobs(c *Cluster, _ Opts) string {
	d := &doc{}
	header(d, c, KindCronjobs)
	cr := c.Cron
	d.p("De cronjobs van "+esc(c.Name)+": de jobs die ClusterForge beheert, als systemd-timer met `cf-agent cron-run`, en wat ClusterForge verder in de crontabs, cron.d, cron.daily en de systemd-timers van de nodes vond.",
		"Commando's staan er zoals de node ze toont, met wachtwoorden gemaskeerd (`***`).",
		"De runs met hun uitvoer staan in ClusterForge bij Cronjobs.")

	d.h2("Beheerd door ClusterForge")
	if len(cr.Managed) == 0 {
		d.p("Geen. Een job of bestandssync voeg je toe bij Cronjobs → Cronjob toevoegen, of neem je over bij een gevonden job.")
	} else {
		var rows [][]string
		var syncs []CronManaged
		for _, m := range cr.Managed {
			what := code(m.Command) + " als " + esc(m.User)
			if m.Sync != nil {
				what = "sync " + code(m.Sync.Source) + " naar " + syncTo(m.Sync) + ", in " + code(m.Sync.Target)
				syncs = append(syncs, m)
			}
			on := map[string]string{"vip": "node met het VIP", "all": "alle nodes"}[m.RunOn]
			if m.RunOn == "node" {
				on = esc(or(m.Node, "één node"))
			}
			state := "aan"
			if !m.Enabled {
				state = "uit"
			}
			rows = append(rows, []string{esc(m.Name), esc(or(m.Describe, m.Schedule)) + " " + code(m.Schedule), on, what, state})
		}
		d.table([]string{"Naam", "Wanneer", "Waar", "Wat", "Aan"}, rows)
		for _, m := range syncs {
			d.h3("Sync " + esc(m.Name))
			s := m.Sync
			items := []string{
				"Van " + code(s.Source) + " op " + map[bool]string{true: "de node met het VIP", false: esc(or(m.Node, "één node"))}[m.RunOn == "vip"] +
					" naar " + code(s.Target) + " op " + syncTo(s) + ", met rsync over SSH als root.",
				map[bool]string{true: "Wat op de bron weg is, gaat ook op het doel weg (`--delete`).", false: "Op het doel gaat niets weg."}[s.Delete],
			}
			if len(s.Exclude) > 0 {
				items = append(items, "Niet mee: "+codes(s.Exclude)+".")
			}
			if s.BwLimitMB > 0 {
				items = append(items, "Hoogstens "+strconv.Itoa(s.BwLimitMB)+" MB/s.")
			}
			items = append(items, "Een doel dat het VIP van zijn cluster heeft, weigert: de actieve node wordt nooit overschreven.",
				"Op elk doel staat de sleutel van de bron in `/root/.ssh/authorized_keys`, tussen `# clusterforge-sync begin` en `# clusterforge-sync einde`, en mag hij alleen `cf-agent sync-receive` naar deze map starten.")
			d.bullets(items)
		}
	}

	d.h2("Gevonden op de nodes")
	if len(cr.Found) == 0 {
		d.p("ClusterForge vond op de nodes van dit cluster geen andere cronjobs of timers.")
	} else {
		found := slices.Clone(cr.Found)
		// Eerst wat Jonas zelf maakte, dan wat met een pakket meekwam.
		slices.SortStableFunc(found, func(a, b CronFound) int { return cmp.Compare(len(a.Package), len(b.Package)) })
		var rows [][]string
		for i, f := range found {
			if i == maxFoundCrons {
				break
			}
			where := cronSourceText[f.Source]
			if f.File != "" && f.Source != "crontab" {
				where = code(f.File)
				if f.Line > 0 {
					where += ", regel " + strconv.Itoa(f.Line)
				}
			}
			if f.Package != "" {
				where += " (pakket " + esc(f.Package) + ")"
			}
			nodes := esc(strings.Join(f.Nodes, ", "))
			if len(f.Off) > 0 {
				nodes += "; uitgezet door ClusterForge op " + esc(strings.Join(f.Off, ", "))
			}
			rows = append(rows, []string{where, esc(or(f.Describe, f.Schedule)), esc(f.User), code(f.Command), nodes})
		}
		d.table([]string{"Waar", "Wanneer", "Gebruiker", "Commando", "Nodes"}, rows)
		if len(found) > maxFoundCrons {
			d.p(fmt.Sprintf("En nog %d; ze staan allemaal in ClusterForge bij Cronjobs.", len(found)-maxFoundCrons))
		}
	}

	d.h2("Runbooks")
	d.h3("Een run nakijken")
	d.steps([]string{
		"Open in ClusterForge Cronjobs en de job: bij elke run staan de node, de uitkomst, de exitcode, de duur en de uitvoer (de laatste 64 KiB). Een run die loopt, volg je met Volgen.",
		"Op de node zelf: `systemctl list-timers 'cf-cron-*'` toont de timers met de volgende run, `journalctl -u 'cf-cron@*'` de starts, en `/var/lib/clusterforge/cron/output/` de volledige uitvoer van de laatste tien runs per job.",
		"Een run met de hand op de node: `systemctl start cf-cron@<id>.service`; bij een job op het VIP voert alleen de node met het VIP hem uit, de andere slaan over.",
	})
	if len(cr.Managed) > 0 {
		var ids []string
		for _, m := range cr.Managed {
			ids = append(ids, esc(m.Name)+": "+code(m.ID))
		}
		d.p("De id's van de jobs hier: " + list(ids) + ".")
	}
	d.h3("Een sync terugdraaien")
	d.p("rsync heeft geen prullenbak: wat een sync overschreef of (met `--delete`) weghaalde, is op het doel weg. Een sync is dus geen back-up; hij maakt een fout op de bron ook op de doelen.")
	d.steps([]string{
		"Zet de sync meteen uit in ClusterForge (Uitzetten op de pagina van de job), zodat de volgende run niet verder kopieert. Een run die nog loopt, stop je met Stoppen.",
		"Kijk bij de laatste runs per doel hoeveel bestanden meegingen en weggingen, en in de uitvoer welke. Zo weet je welke nodes geraakt zijn.",
		"Heeft een node de goede versie nog, zoals een doel waar de sync weigerde of een node van een ander cluster, kopieer dan van daar terug. Kijk eerst wat er zou veranderen: `rsync -a --dry-run --itemize-changes root@<goede node>:/map/ /map/`, en daarna zonder `--dry-run`.",
		"Anders zet je de map terug uit een back-up van de VM, zoals het hoofdstuk " + link("Herstel", KindRecovery+".md") + " beschrijft.",
		"Zoek de oorzaak op de bron (een app die bestanden weghaalde, een verkeerde map), doe een Proefrun en zet de sync dan weer aan.",
	})
	d.h3("Een sync weghalen")
	d.p("Weghalen in ClusterForge haalt de timer van de bron en het blok `# clusterforge-sync` uit `authorized_keys` op elk doel. De gekopieerde bestanden blijven staan. De sleutel van de bron blijft in `/etc/clusterforge/sync/` voor een volgende sync.")
	d.h3("Een uitgezette cronjob weer aanzetten")
	d.p("Bij Cronjobs → Gevonden op de nodes staat bij een uitgezette job Weer aanzetten. Op de node zelf haal je `# uitgezet door ClusterForge: ` vooraan de regel weg; cron leest het bestand vanzelf opnieuw.")
	return d.String()
}

// syncTo zegt naar welke nodes een sync gaat.
func syncTo(s *CronSync) string {
	if s.ToCluster != "" {
		return "alle nodes van " + esc(s.ToCluster)
	}
	return "de andere nodes van het cluster"
}
