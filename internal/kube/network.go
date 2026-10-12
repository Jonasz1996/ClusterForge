package kube

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

type loadBalancerStatus struct {
	Ingress []struct {
		IP       string `json:"ip"`
		Hostname string `json:"hostname"`
	} `json:"ingress"`
}

func (l loadBalancerStatus) addresses() []string {
	var out []string
	for _, i := range l.Ingress {
		if a := cmpOr(i.IP, i.Hostname); a != "" && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// ServicePort is een poort van een Service. TargetPort is een nummer of de
// naam van een poort in de container.
type ServicePort struct {
	Name       string          `json:"name"`
	Protocol   string          `json:"protocol"`
	Port       int             `json:"port"`
	TargetPort json.RawMessage `json:"targetPort"`
	NodePort   int             `json:"nodePort"`
}

// Target is de poort in de container, als tekst.
func (p ServicePort) Target() string {
	var n int
	if json.Unmarshal(p.TargetPort, &n) == nil {
		return strconv.Itoa(n)
	}
	var s string
	if json.Unmarshal(p.TargetPort, &s) == nil && s != "" {
		return s
	}
	return strconv.Itoa(p.Port)
}

// KService is een Service; Service is al de naam van de dienst die met de
// API praat.
type KService struct {
	Metadata Meta `json:"metadata"`
	Spec     struct {
		Type        string            `json:"type"`
		ClusterIP   string            `json:"clusterIP"`
		Selector    map[string]string `json:"selector"`
		Ports       []ServicePort     `json:"ports"`
		ExternalIPs []string          `json:"externalIPs"`
	} `json:"spec"`
	Status struct {
		LoadBalancer loadBalancerStatus `json:"loadBalancer"`
	} `json:"status"`
}

// Selects is true als de Service pods met deze labels kiest.
func (sv KService) Selects(labels map[string]string) bool {
	if len(sv.Spec.Selector) == 0 {
		return false
	}
	for k, v := range sv.Spec.Selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// External zijn de adressen buiten het cluster: van ServiceLB of vast.
func (sv KService) External() []string {
	out := sv.Status.LoadBalancer.addresses()
	for _, ip := range sv.Spec.ExternalIPs {
		if !slices.Contains(out, ip) {
			out = append(out, ip)
		}
	}
	return out
}

// Describe vat de Service samen, zoals 'LoadBalancer op 10.0.40.11, poort
// 80 en 443'.
func (sv KService) Describe() string {
	ports := make([]string, 0, len(sv.Spec.Ports))
	for _, p := range sv.Spec.Ports {
		ports = append(ports, strconv.Itoa(p.Port))
	}
	where := sv.Spec.ClusterIP
	if ext := sv.External(); len(ext) > 0 {
		where = strings.Join(ext, ", ")
	}
	txt := cmpOr(sv.Spec.Type, "ClusterIP")
	if where != "" && where != "None" {
		txt += " op " + where
	}
	switch len(ports) {
	case 0:
	case 1:
		txt += ", poort " + ports[0]
	default:
		txt += ", poorten " + strings.Join(ports, ", ")
	}
	return txt
}

// URL is een webadres van buiten het cluster, als de Service er een heeft.
func (sv KService) URL() string {
	ext := sv.External()
	if len(ext) == 0 {
		return ""
	}
	host := ext[0]
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		host = "[" + host + "]"
	}
	for _, p := range sv.Spec.Ports {
		switch p.Port {
		case 80:
			return "http://" + host + "/"
		case 443:
			return "https://" + host + "/"
		}
	}
	if len(sv.Spec.Ports) > 0 {
		return fmt.Sprintf("http://%s:%d/", host, sv.Spec.Ports[0].Port)
	}
	return ""
}

// Ingress stuurt verkeer op hostnaam en pad naar een Service.
type Ingress struct {
	Metadata Meta `json:"metadata"`
	Spec     struct {
		IngressClassName *string `json:"ingressClassName"`
		TLS              []struct {
			Hosts      []string `json:"hosts"`
			SecretName string   `json:"secretName"`
		} `json:"tls"`
		Rules []struct {
			Host string `json:"host"`
			HTTP *struct {
				Paths []struct {
					Path    string         `json:"path"`
					Backend ingressBackend `json:"backend"`
				} `json:"paths"`
			} `json:"http"`
		} `json:"rules"`
		DefaultBackend *ingressBackend `json:"defaultBackend"`
	} `json:"spec"`
	Status struct {
		LoadBalancer loadBalancerStatus `json:"loadBalancer"`
	} `json:"status"`
}

type ingressBackend struct {
	Service *struct {
		Name string `json:"name"`
		Port struct {
			Number int    `json:"number"`
			Name   string `json:"name"`
		} `json:"port"`
	} `json:"service"`
}

func (b ingressBackend) target() (string, string) {
	if b.Service == nil {
		return "", ""
	}
	port := b.Service.Port.Name
	if b.Service.Port.Number > 0 {
		port = strconv.Itoa(b.Service.Port.Number)
	}
	return b.Service.Name, port
}

// IngressRule is één hostnaam en pad met de Service erachter.
type IngressRule struct {
	Host    string
	Path    string
	Service string
	Port    string
	TLS     bool
	// URL is het adres om te openen; leeg als er geen hostnaam of adres is.
	URL string
}

// Class is de ingressklasse, zoals traefik.
func (in Ingress) Class() string {
	if in.Spec.IngressClassName != nil {
		return *in.Spec.IngressClassName
	}
	return in.Metadata.Annotations["kubernetes.io/ingress.class"]
}

// Address zijn de adressen waarop de ingress-controller luistert.
func (in Ingress) Address() []string { return in.Status.LoadBalancer.addresses() }

// Rules zijn de regels, met een webadres per regel.
func (in Ingress) Rules() []IngressRule {
	tls := func(host string) bool {
		for _, t := range in.Spec.TLS {
			if slices.Contains(t.Hosts, host) || (host == "" && len(t.Hosts) == 0) {
				return true
			}
		}
		return false
	}
	addr := ""
	if a := in.Address(); len(a) > 0 {
		addr = a[0]
	}
	var out []IngressRule
	add := func(host, path string, b ingressBackend) {
		svc, port := b.target()
		r := IngressRule{Host: host, Path: cmpOr(path, "/"), Service: svc, Port: port, TLS: tls(host)}
		if h := cmpOr(host, addr); h != "" && !strings.Contains(h, "*") {
			scheme := "http"
			if r.TLS {
				scheme = "https"
			}
			r.URL = scheme + "://" + h + r.Path
		}
		out = append(out, r)
	}
	for _, rule := range in.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for _, p := range rule.HTTP.Paths {
			add(rule.Host, p.Path, p.Backend)
		}
	}
	if in.Spec.DefaultBackend != nil {
		add("", "/", *in.Spec.DefaultBackend)
	}
	return out
}

// Network geeft de Services en Ingresses, in één namespace of in alle.
func (s *Service) Network(ctx context.Context, clusterID uuid.UUID, ns string) ([]KService, []Ingress, error) {
	sp, err := nsPath("/api/v1", ns, "services")
	if err != nil {
		return nil, nil, err
	}
	ip, _ := nsPath("/apis/networking.k8s.io/v1", ns, "ingresses")
	var (
		services  []KService
		ingresses []Ingress
		errs      [2]error
		wg        sync.WaitGroup
	)
	wg.Go(func() { services, errs[0] = List[KService](ctx, s, clusterID, sp, nil) })
	wg.Go(func() { ingresses, errs[1] = List[Ingress](ctx, s, clusterID, ip, nil) })
	wg.Wait()
	if err := cmp.Or(errs[0], errs[1]); err != nil {
		return nil, nil, err
	}
	slices.SortFunc(services, func(a, b KService) int { return compareMeta(a.Metadata, b.Metadata) })
	slices.SortFunc(ingresses, func(a, b Ingress) int { return compareMeta(a.Metadata, b.Metadata) })
	return services, ingresses, nil
}

func compareMeta(a, b Meta) int {
	return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
}

// Claim is een PersistentVolumeClaim.
type Claim struct {
	Metadata Meta `json:"metadata"`
	Spec     struct {
		AccessModes      []string `json:"accessModes"`
		StorageClassName *string  `json:"storageClassName"`
		VolumeName       string   `json:"volumeName"`
		Resources        struct {
			Requests map[string]string `json:"requests"`
		} `json:"resources"`
	} `json:"spec"`
	Status struct {
		Phase    string            `json:"phase"`
		Capacity map[string]string `json:"capacity"`
	} `json:"status"`
}

// Class is de StorageClass van de claim.
func (c Claim) Class() string {
	if c.Spec.StorageClassName != nil {
		return *c.Spec.StorageClassName
	}
	return ""
}

// Volume is een PersistentVolume.
type Volume struct {
	Metadata Meta `json:"metadata"`
	Spec     struct {
		Capacity                      map[string]string `json:"capacity"`
		AccessModes                   []string          `json:"accessModes"`
		StorageClassName              string            `json:"storageClassName"`
		PersistentVolumeReclaimPolicy string            `json:"persistentVolumeReclaimPolicy"`
		ClaimRef                      *struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		} `json:"claimRef"`
		HostPath *struct {
			Path string `json:"path"`
		} `json:"hostPath"`
		Local *struct {
			Path string `json:"path"`
		} `json:"local"`
		NodeAffinity *struct {
			Required *struct {
				NodeSelectorTerms []struct {
					MatchExpressions []struct {
						Key      string   `json:"key"`
						Operator string   `json:"operator"`
						Values   []string `json:"values"`
					} `json:"matchExpressions"`
				} `json:"nodeSelectorTerms"`
			} `json:"required"`
		} `json:"nodeAffinity"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

// Node is de node waaraan het volume vastzit, zoals bij local-path.
func (v Volume) Node() string {
	if v.Spec.NodeAffinity == nil || v.Spec.NodeAffinity.Required == nil {
		return ""
	}
	for _, t := range v.Spec.NodeAffinity.Required.NodeSelectorTerms {
		for _, e := range t.MatchExpressions {
			if e.Key == "kubernetes.io/hostname" && e.Operator == "In" && len(e.Values) == 1 {
				return e.Values[0]
			}
		}
	}
	return ""
}

// Path is de map op de node, bij een volume op de schijf van een node.
func (v Volume) Path() string {
	switch {
	case v.Spec.HostPath != nil:
		return v.Spec.HostPath.Path
	case v.Spec.Local != nil:
		return v.Spec.Local.Path
	}
	return ""
}

// StorageClass zegt hoe een volume gemaakt wordt.
type StorageClass struct {
	Metadata             Meta    `json:"metadata"`
	Provisioner          string  `json:"provisioner"`
	ReclaimPolicy        *string `json:"reclaimPolicy"`
	VolumeBindingMode    *string `json:"volumeBindingMode"`
	AllowVolumeExpansion *bool   `json:"allowVolumeExpansion"`
}

// Default is true voor de standaardklasse.
func (c StorageClass) Default() bool {
	return c.Metadata.Annotations["storageclass.kubernetes.io/is-default-class"] == "true"
}

// Storage is de opslag van een cluster.
type Storage struct {
	Claims  []Claim
	Volumes []Volume
	Classes []StorageClass
	// UsedBy zijn per claim (namespace/naam) de pods die hem gebruiken.
	UsedBy map[string][]string
}

// Storage geeft de claims, de volumes en de StorageClasses. Met een
// namespace zijn het alleen de claims daar en hun volumes.
func (s *Service) Storage(ctx context.Context, clusterID uuid.UUID, ns string) (Storage, error) {
	cp, err := nsPath("/api/v1", ns, "persistentvolumeclaims")
	if err != nil {
		return Storage{}, err
	}
	var (
		st   = Storage{UsedBy: map[string][]string{}}
		pods []Pod
		errs [4]error
		wg   sync.WaitGroup
	)
	wg.Go(func() { st.Claims, errs[0] = List[Claim](ctx, s, clusterID, cp, nil) })
	wg.Go(func() { st.Volumes, errs[1] = List[Volume](ctx, s, clusterID, "/api/v1/persistentvolumes", nil) })
	wg.Go(func() {
		st.Classes, errs[2] = List[StorageClass](ctx, s, clusterID, "/apis/storage.k8s.io/v1/storageclasses", nil)
	})
	wg.Go(func() { pods, errs[3] = s.Pods(ctx, clusterID, ns, "") })
	wg.Wait()
	if err := cmp.Or(errs[0], errs[1], errs[2], errs[3]); err != nil {
		return Storage{}, err
	}
	if ns != "" {
		st.Volumes = slices.DeleteFunc(st.Volumes, func(v Volume) bool { return v.Spec.ClaimRef == nil || v.Spec.ClaimRef.Namespace != ns })
	}
	for _, p := range pods {
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				key := p.Metadata.Namespace + "/" + v.PersistentVolumeClaim.ClaimName
				st.UsedBy[key] = append(st.UsedBy[key], p.Metadata.Name)
			}
		}
	}
	slices.SortFunc(st.Claims, func(a, b Claim) int { return compareMeta(a.Metadata, b.Metadata) })
	slices.SortFunc(st.Volumes, func(a, b Volume) int { return compareMeta(a.Metadata, b.Metadata) })
	slices.SortFunc(st.Classes, func(a, b StorageClass) int { return compareMeta(a.Metadata, b.Metadata) })
	return st, nil
}

// ConfigMap is configuratie als sleutels en waarden.
type ConfigMap struct {
	Metadata   Meta              `json:"metadata"`
	Data       map[string]string `json:"data"`
	BinaryData map[string]string `json:"binaryData"`
}

// Secret is een Secret zoals de agent hem doorgeeft: de waarden zijn
// vervangen door hun lengte.
type Secret struct {
	Metadata Meta              `json:"metadata"`
	Type     string            `json:"type"`
	Data     map[string]string `json:"data"`
}

// SecretKey is een sleutel van een Secret met de lengte van de waarde.
type SecretKey struct {
	Name  string
	Bytes int
}

// Keys zijn de sleutels met hun lengte, op naam.
func (sec Secret) Keys() []SecretKey {
	out := make([]SecretKey, 0, len(sec.Data))
	for k, v := range sec.Data {
		n, _ := protocol.KubeHiddenLength(v)
		out = append(out, SecretKey{Name: k, Bytes: n})
	}
	slices.SortFunc(out, func(a, b SecretKey) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// Config geeft de ConfigMaps en Secrets.
func (s *Service) Config(ctx context.Context, clusterID uuid.UUID, ns string) ([]ConfigMap, []Secret, error) {
	mp, err := nsPath("/api/v1", ns, "configmaps")
	if err != nil {
		return nil, nil, err
	}
	sp, _ := nsPath("/api/v1", ns, "secrets")
	var (
		maps    []ConfigMap
		secrets []Secret
		errs    [2]error
		wg      sync.WaitGroup
	)
	wg.Go(func() { maps, errs[0] = List[ConfigMap](ctx, s, clusterID, mp, nil) })
	wg.Go(func() { secrets, errs[1] = List[Secret](ctx, s, clusterID, sp, nil) })
	wg.Wait()
	if err := cmp.Or(errs[0], errs[1]); err != nil {
		return nil, nil, err
	}
	slices.SortFunc(maps, func(a, b ConfigMap) int { return compareMeta(a.Metadata, b.Metadata) })
	slices.SortFunc(secrets, func(a, b Secret) int { return compareMeta(a.Metadata, b.Metadata) })
	return maps, secrets, nil
}
