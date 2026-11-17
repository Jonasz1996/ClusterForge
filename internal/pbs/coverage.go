package pbs

// Fase 32 M5: dekking en bewaking. Welke ClusterForge-nodes (Proxmox-gasten)
// staan op een PBS, hoe oud is hun laatste back-up en is die geverifieerd.
// Een achtergrondronde houdt dat en de waarschuwingen van elke PBS bij, zodat
// "Aandacht nodig" ze kan tonen zonder de PBS bij elke paginalading te
// vragen.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Grenzen voor de leeftijd van de laatste back-up van een node.
const (
	BackupWarnAge = 36 * time.Hour
	BackupCritAge = 7 * 24 * time.Hour
)

// Coverage is de dekking van de nodes die een Proxmox-gast zijn.
type Coverage struct {
	At    time.Time      `json:"at"`
	Nodes []NodeCoverage `json:"nodes"`
	// Errors zijn de datastores die niet te lezen waren.
	Errors []string `json:"errors"`
}

// NodeCoverage is één node met zijn nieuwste back-up.
type NodeCoverage struct {
	NodeID      uuid.UUID  `json:"node_id"`
	Hostname    string     `json:"hostname"`
	VMID        int        `json:"vmid"`
	GuestType   string     `json:"guest_type,omitempty"`
	ClusterID   *uuid.UUID `json:"cluster_id,omitempty"`
	ClusterName string     `json:"cluster_name,omitempty"`
	Environment string     `json:"environment,omitempty"`
	// Leeg als er geen back-up gevonden is.
	Server     string     `json:"server,omitempty"`
	ServerID   *uuid.UUID `json:"server_id,omitempty"`
	Store      string     `json:"store,omitempty"`
	NS         string     `json:"ns,omitempty"`
	LastBackup int64      `json:"last_backup,omitempty"`
	Count      int        `json:"count,omitempty"`
	// Verify is de stand van de nieuwste snapshot: ok, failed of leeg.
	Verify string `json:"verify,omitempty"`
	// FailedVerify telt de snapshots van de groep die kapot zijn.
	FailedVerify int `json:"failed_verify,omitempty"`
	// State is ok, warn of crit.
	State string `json:"state"`
	Why   string `json:"why,omitempty"`
	// BackupType is vm of ct: de groep op PBS.
	BackupType string `json:"backup_type,omitempty"`
}

type groupInfo struct {
	server Server
	store  string
	ns     string
	typ    string
	id     string
	last   int64
	count  int
}

type pbsGroup struct {
	Type  string `json:"backup-type"`
	ID    string `json:"backup-id"`
	Last  int64  `json:"last-backup"`
	Count int    `json:"backup-count"`
}

type pbsSnap struct {
	Time         int64 `json:"backup-time"`
	Verification *struct {
		State string `json:"state"`
	} `json:"verification"`
}

// Coverage leest alle PBS'en en zoekt de back-ups van de nodes.
func (s *Service) Coverage(ctx context.Context) (Coverage, error) {
	rows, err := s.q.ListPBSCoverageNodes(ctx)
	if err != nil {
		return Coverage{}, err
	}
	servers, err := s.Servers(ctx)
	if err != nil {
		return Coverage{}, err
	}
	cov := Coverage{At: s.Now(), Nodes: []NodeCoverage{}, Errors: []string{}}
	if len(rows) == 0 {
		return cov, nil
	}
	groups := map[string]groupInfo{} // vm/101 → nieuwste groep
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	fail := func(f string, a ...any) {
		mu.Lock()
		cov.Errors = append(cov.Errors, fmt.Sprintf(f, a...))
		mu.Unlock()
	}
	for _, v := range servers {
		if v.Why != "" {
			cov.Errors = append(cov.Errors, v.Hostname+": "+v.Why)
			continue
		}
		for _, store := range v.Datastores {
			var nss []struct {
				NS string `json:"ns"`
			}
			if err := s.GetInto(ctx, v.NodeID, "/admin/datastore/"+store+"/namespace", map[string]string{"max-depth": "7"}, &nss); err != nil || len(nss) == 0 {
				nss = []struct {
					NS string `json:"ns"`
				}{{}}
			}
			for _, n := range nss {
				wg.Add(1)
				go func(v Server, store, ns string) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					var params map[string]string
					if ns != "" {
						params = map[string]string{"ns": ns}
					}
					var list []pbsGroup
					if err := s.GetInto(ctx, v.NodeID, "/admin/datastore/"+store+"/groups", params, &list); err != nil {
						fail("%s/%s: %v", v.Hostname, store, err)
						return
					}
					mu.Lock()
					defer mu.Unlock()
					for _, g := range list {
						if g.Type != "vm" && g.Type != "ct" {
							continue
						}
						key := g.Type + "/" + g.ID
						if cur, ok := groups[key]; !ok || g.Last > cur.last {
							groups[key] = groupInfo{server: v, store: store, ns: ns, typ: g.Type, id: g.ID, last: g.Last, count: g.Count}
						}
					}
				}(v, store, n.NS)
			}
		}
	}
	wg.Wait()
	now := s.Now()
	for _, r := range rows {
		n := NodeCoverage{NodeID: r.ID, Hostname: r.Hostname, VMID: int(r.Vmid), GuestType: r.GuestType, ClusterID: r.ClusterID, ClusterName: r.ClusterName, Environment: r.Environment}
		id := strconv.Itoa(int(r.Vmid))
		var g groupInfo
		var ok bool
		switch r.GuestType {
		case "qemu":
			g, ok = groups["vm/"+id]
		case "lxc":
			g, ok = groups["ct/"+id]
		default:
			a, okA := groups["vm/"+id]
			b, okB := groups["ct/"+id]
			g, ok = a, okA
			if okB && (!okA || b.last > a.last) {
				g, ok = b, true
			}
		}
		if ok {
			sid := g.server.NodeID
			n.Server, n.ServerID, n.Store, n.NS, n.LastBackup, n.Count, n.BackupType = g.server.Hostname, &sid, g.store, g.ns, g.last, g.count, g.typ
		}
		cov.Nodes = append(cov.Nodes, n)
	}
	// De verificatie van de groepen die bij een node horen.
	for i := range cov.Nodes {
		n := &cov.Nodes[i]
		if n.ServerID == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			params := map[string]string{"backup-type": n.BackupType, "backup-id": strconv.Itoa(n.VMID)}
			if n.NS != "" {
				params["ns"] = n.NS
			}
			var snaps []pbsSnap
			if err := s.GetInto(ctx, *n.ServerID, "/admin/datastore/"+n.Store+"/snapshots", params, &snaps); err != nil {
				return
			}
			var newest int64
			for _, sn := range snaps {
				st := ""
				if sn.Verification != nil {
					st = sn.Verification.State
				}
				if st == "failed" {
					n.FailedVerify++
				}
				if sn.Time >= newest {
					newest, n.Verify = sn.Time, st
				}
			}
		}()
	}
	wg.Wait()
	for i := range cov.Nodes {
		n := &cov.Nodes[i]
		judge(n, now)
		// Niets gevonden terwijl een PBS of datastore niet te lezen was:
		// misschien staat de back-up net daar. Oranje, niet rood.
		if n.LastBackup == 0 && len(cov.Errors) > 0 {
			n.State, n.Why = "warn", "geen back-up gevonden, maar niet elke PBS of datastore was te lezen"
		}
	}
	sort.SliceStable(cov.Nodes, func(i, j int) bool {
		a, b := cov.Nodes[i], cov.Nodes[j]
		if a.ClusterName != b.ClusterName {
			if a.ClusterName == "" || b.ClusterName == "" {
				return b.ClusterName == ""
			}
			return a.ClusterName < b.ClusterName
		}
		return a.Hostname < b.Hostname
	})
	sort.Strings(cov.Errors)
	return cov, nil
}

// judge zet State en Why van een node.
func judge(n *NodeCoverage, now time.Time) {
	n.State = "ok"
	switch {
	case n.LastBackup == 0:
		n.State, n.Why = "crit", "geen back-up op een PBS"
		return
	case now.Sub(time.Unix(n.LastBackup, 0)) > BackupCritAge:
		n.State, n.Why = "crit", fmt.Sprintf("laatste back-up %d dagen oud", int(now.Sub(time.Unix(n.LastBackup, 0)).Hours()/24))
	case now.Sub(time.Unix(n.LastBackup, 0)) > BackupWarnAge:
		n.State, n.Why = "warn", fmt.Sprintf("laatste back-up %d uur oud", int(now.Sub(time.Unix(n.LastBackup, 0)).Hours()))
	}
	switch {
	case n.Verify == "failed":
		n.State, n.Why = "crit", joinWhy(n.Why, "nieuwste back-up is kapot (verificatie mislukt)")
	case n.FailedVerify > 0 && n.State == "ok":
		n.State, n.Why = "warn", fmt.Sprintf("%d oudere back-ups zijn kapot", n.FailedVerify)
	case n.Verify == "" && n.State == "ok":
		n.Why = "nieuwste back-up nog niet geverifieerd"
	}
}

func joinWhy(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

// --- Achtergrondronde ------------------------------------------------------

// Summary is wat de achtergrondronde van één PBS onthoudt.
type Summary struct {
	Server   Server    `json:"server"`
	Warnings []Warning `json:"warnings"`
	At       time.Time `json:"at"`
	Error    string    `json:"error,omitempty"`
}

type watchState struct {
	mu        sync.Mutex
	summaries []Summary
	coverage  *Coverage
}

// WatchEvery is hoe vaak de achtergrondronde de PBS'en leest.
const WatchEvery = 10 * time.Minute

// Watch leest elke WatchEvery alle PBS'en; stopt met ctx.
func (s *Service) Watch(ctx context.Context) {
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.Refresh(ctx)
		t.Reset(WatchEvery)
	}
}

// Refresh leest alle PBS'en nu.
func (s *Service) Refresh(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	servers, err := s.Servers(ctx)
	if err != nil {
		s.log.Warn("PBS-ronde", "err", err)
		return
	}
	var sums []Summary
	for _, v := range servers {
		sum := Summary{Server: v, At: s.Now(), Warnings: []Warning{}}
		if v.Why != "" {
			sum.Error = v.Why
		} else if o, err := s.Overview(ctx, v.NodeID); err != nil {
			sum.Error = err.Error()
		} else {
			sum.Warnings = o.Warnings
		}
		sums = append(sums, sum)
	}
	var cov *Coverage
	if c, err := s.Coverage(ctx); err == nil {
		cov = &c
	} else {
		s.log.Warn("PBS-dekking", "err", err)
	}
	s.watch.mu.Lock()
	s.watch.summaries = sums
	if cov != nil {
		s.watch.coverage = cov
	}
	s.watch.mu.Unlock()
}

// Cached geeft wat de laatste ronde zag; nil coverage als er nog geen was.
func (s *Service) Cached() ([]Summary, *Coverage) {
	s.watch.mu.Lock()
	defer s.watch.mu.Unlock()
	return s.watch.summaries, s.watch.coverage
}

// CachedCoverage geeft de dekking van de laatste ronde als die jonger is
// dan maxAge, en leest anders opnieuw.
func (s *Service) CachedCoverage(ctx context.Context, maxAge time.Duration) (Coverage, error) {
	_, cov := s.Cached()
	if cov != nil && s.Now().Sub(cov.At) < maxAge {
		return *cov, nil
	}
	c, err := s.Coverage(ctx)
	if err != nil {
		return Coverage{}, err
	}
	s.watch.mu.Lock()
	s.watch.coverage = &c
	s.watch.mu.Unlock()
	return c, nil
}
