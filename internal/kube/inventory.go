package kube

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/store"
)

// Inventory is wat de documentatie van een cluster toont en niet elke 30
// seconden hoeft: de namespaces, de webadressen en de opslag. De sync leest
// het elke InventoryInterval en bewaart het in kube_clusters.
type Inventory struct {
	Namespaces []string           `json:"namespaces"`
	Addresses  []InventoryAddress `json:"addresses"`
	Volumes    []InventoryVolume  `json:"volumes"`
	// DefaultClass is de standaard-StorageClass; leeg zonder.
	DefaultClass string `json:"default_class"`
}

// InventoryAddress is een weg naar binnen: een regel van een Ingress, of
// een Service met een adres buiten het cluster.
type InventoryAddress struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	// URL is het adres om te openen; leeg als er geen hostnaam of adres is.
	URL string `json:"url"`
	// Host en Path bij een Ingress, en de Service met poort erachter.
	Host    string `json:"host,omitempty"`
	Path    string `json:"path,omitempty"`
	Service string `json:"service,omitempty"`
	Port    string `json:"port,omitempty"`
	// Text beschrijft een Service, zoals 'LoadBalancer op 10.0.40.11,
	// poorten 80, 443'.
	Text string `json:"text,omitempty"`
}

// InventoryVolume is een PersistentVolumeClaim met het volume erachter.
type InventoryVolume struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Class     string `json:"class"`
	Size      string `json:"size"`
	Phase     string `json:"phase"`
	// Node en Path als het volume op de schijf van één node staat, zoals
	// bij local-path.
	Node   string   `json:"node,omitempty"`
	Path   string   `json:"path,omitempty"`
	UsedBy []string `json:"used_by"`
}

// ReadInventory leest de namespaces, de webadressen en de opslag.
func (s *Service) ReadInventory(ctx context.Context, clusterID uuid.UUID) (Inventory, error) {
	var (
		out       = Inventory{Namespaces: []string{}, Addresses: []InventoryAddress{}, Volumes: []InventoryVolume{}}
		services  []KService
		ingresses []Ingress
		st        Storage
		errs      [3]error
		wg        sync.WaitGroup
	)
	wg.Go(func() { out.Namespaces, errs[0] = s.Namespaces(ctx, clusterID) })
	wg.Go(func() { services, ingresses, errs[1] = s.Network(ctx, clusterID, "") })
	wg.Go(func() { st, errs[2] = s.Storage(ctx, clusterID, "") })
	wg.Wait()
	if err := errors.Join(errs[:]...); err != nil {
		return Inventory{}, err
	}
	slices.Sort(out.Namespaces)
	for _, in := range ingresses {
		for _, r := range in.Rules() {
			out.Addresses = append(out.Addresses, InventoryAddress{
				Namespace: in.Metadata.Namespace, Kind: "Ingress", Name: in.Metadata.Name, URL: r.URL, Host: r.Host, Path: r.Path,
				Service: r.Service, Port: r.Port,
			})
		}
	}
	for _, sv := range services {
		if len(sv.External()) == 0 {
			continue
		}
		out.Addresses = append(out.Addresses, InventoryAddress{
			Namespace: sv.Metadata.Namespace, Kind: "Service", Name: sv.Metadata.Name, URL: sv.URL(), Text: sv.Describe(),
		})
	}
	volumes := map[string]Volume{}
	for _, v := range st.Volumes {
		volumes[v.Metadata.Name] = v
	}
	for _, c := range st.Claims {
		iv := InventoryVolume{
			Namespace: c.Metadata.Namespace, Name: c.Metadata.Name, Class: c.Class(), Phase: c.Status.Phase,
			Size:   cmp.Or(c.Status.Capacity["storage"], c.Spec.Resources.Requests["storage"]),
			UsedBy: append([]string{}, st.UsedBy[c.Metadata.Namespace+"/"+c.Metadata.Name]...),
		}
		if v, ok := volumes[c.Spec.VolumeName]; ok {
			iv.Node, iv.Path = v.Node(), v.Path()
		}
		slices.Sort(iv.UsedBy)
		out.Volumes = append(out.Volumes, iv)
	}
	for _, c := range st.Classes {
		if c.Default() {
			out.DefaultClass = c.Metadata.Name
		}
	}
	return out, nil
}

// syncInventory leest de inventaris als het tijd is. Een fout komt bij het
// cluster; de vorige inventaris blijft staan.
func (s *Service) syncInventory(ctx context.Context, clusterID uuid.UUID) {
	s.mu.Lock()
	due := s.Now().Sub(s.invAt[clusterID]) >= s.InventoryInterval
	if due {
		s.invAt[clusterID] = s.Now()
	}
	s.mu.Unlock()
	if !due {
		return
	}
	inv, err := s.ReadInventory(ctx, clusterID)
	if err != nil {
		if serr := s.q.SetKubeInventoryError(ctx, store.SetKubeInventoryErrorParams{ClusterID: clusterID, InventoryError: clip(err.Error(), 300)}); serr != nil {
			s.log.Warn("fout van de inventaris bewaren mislukt", "cluster", clusterID, "err", serr)
		}
		s.log.Warn("namespaces, webadressen en opslag lezen mislukt", "cluster", clusterID, "err", err)
		return
	}
	data, err := json.Marshal(inv)
	if err == nil {
		err = s.q.SetKubeInventory(ctx, store.SetKubeInventoryParams{ClusterID: clusterID, Inventory: data})
	}
	if err != nil {
		s.log.Warn("inventaris bewaren mislukt", "cluster", clusterID, "err", err)
	}
}
