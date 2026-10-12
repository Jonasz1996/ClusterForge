package kubefake

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"time"
)

// NodeSpec beschrijft een node voor Node en SeedK3s.
type NodeSpec struct {
	Name string
	IP   string
	// Server is true voor een k3s-server (control-plane en etcd).
	Server bool
	// NotReady zet de conditie Ready op False.
	NotReady bool
	// Unschedulable is een node met cordon.
	Unschedulable bool
}

// Node maakt een Node zoals k3s die meldt.
func Node(n NodeSpec) map[string]any {
	labels := map[string]any{
		"kubernetes.io/hostname": n.Name, "kubernetes.io/os": "linux", "kubernetes.io/arch": "amd64",
		"node.kubernetes.io/instance-type": "k3s",
	}
	cpu, mem := "2", "4015180Ki"
	if n.Server {
		labels["node-role.kubernetes.io/control-plane"] = "true"
		labels["node-role.kubernetes.io/etcd"] = "true"
		labels["node-role.kubernetes.io/master"] = "true"
	}
	ready, reason, msg := "True", "KubeletReady", "kubelet is posting ready status"
	if n.NotReady {
		ready, reason, msg = "Unknown", "NodeStatusUnknown", "Kubelet stopped posting node status."
	}
	t := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	cond := func(typ, status, reason, msg string) map[string]any {
		return map[string]any{"type": typ, "status": status, "reason": reason, "message": msg, "lastHeartbeatTime": t, "lastTransitionTime": t}
	}
	spec := map[string]any{"podCIDR": "10.42.0.0/24", "providerID": "k3s://" + n.Name}
	if n.Unschedulable {
		spec["unschedulable"] = true
		spec["taints"] = []any{map[string]any{"key": "node.kubernetes.io/unschedulable", "effect": "NoSchedule"}}
	}
	res := map[string]any{"cpu": cpu, "memory": mem, "pods": "110", "ephemeral-storage": "30784420Ki"}
	return map[string]any{
		"apiVersion": "v1", "kind": "Node",
		"metadata": map[string]any{"name": n.Name, "labels": labels, "annotations": map[string]any{
			"k3s.io/internal-ip": n.IP, "k3s.io/hostname": n.Name, "flannel.alpha.coreos.com/public-ip": n.IP,
		}},
		"spec": spec,
		"status": map[string]any{
			"addresses":   []any{map[string]any{"type": "InternalIP", "address": n.IP}, map[string]any{"type": "Hostname", "address": n.Name}},
			"capacity":    res,
			"allocatable": res,
			"conditions": []any{
				cond("MemoryPressure", "False", "KubeletHasSufficientMemory", "kubelet has sufficient memory available"),
				cond("DiskPressure", "False", "KubeletHasNoDiskPressure", "kubelet has no disk pressure"),
				cond("PIDPressure", "False", "KubeletHasSufficientPID", "kubelet has sufficient PID available"),
				cond("Ready", ready, reason, msg),
			},
			"nodeInfo": map[string]any{
				"kubeletVersion": "v1.35.9+k3s1", "kubeProxyVersion": "v1.35.9+k3s1", "osImage": "Debian GNU/Linux 13 (trixie)",
				"kernelVersion": "6.12.48+deb13-amd64", "containerRuntimeVersion": "containerd://2.1.4-k3s1",
				"architecture": "amd64", "operatingSystem": "linux",
			},
		},
	}
}

// PodSpec beschrijft een pod voor Pod.
type PodSpec struct {
	Namespace string
	Name      string
	Node      string
	// Phase is Running, Pending, Succeeded of Failed; leeg is Running.
	Phase string
	// Waiting is de reden waarom de container wacht, zoals
	// CrashLoopBackOff of ImagePullBackOff; de pod is dan niet klaar.
	Waiting  string
	Restarts int
	Image    string
	Labels   map[string]string
	// OwnerKind en OwnerName: van wie de pod is, zoals ReplicaSet web-5d9c.
	OwnerKind string
	OwnerName string
	IP        string
	// LastReason is waarom de vorige container stopte, zoals Error of
	// OOMKilled, met exitcode 1.
	LastReason string
	// Init is de naam van een init-container die al klaar is.
	Init string
	// Claim is een PersistentVolumeClaim die de pod als volume data heeft.
	Claim string
	// EmptyDir geeft de pod een volume cache in een emptyDir.
	EmptyDir bool
	// Created is wanneer de pod ontstond; leeg laat de tijd weg.
	Created time.Time
}

// Pod maakt een pod met één container.
func Pod(p PodSpec) map[string]any {
	if p.Phase == "" {
		p.Phase = "Running"
	}
	if p.Image == "" {
		p.Image = "docker.io/library/nginx:1.29"
	}
	ready := p.Phase == "Running" && p.Waiting == ""
	state := map[string]any{"running": map[string]any{"startedAt": time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339)}}
	switch {
	case p.Waiting != "":
		state = map[string]any{"waiting": map[string]any{"reason": p.Waiting, "message": "back-off restarting failed container"}}
	case p.Phase == "Succeeded":
		state = map[string]any{"terminated": map[string]any{"reason": "Completed", "exitCode": 0}}
	case p.Phase == "Pending":
		state = map[string]any{"waiting": map[string]any{"reason": "ContainerCreating"}}
	}
	labels := map[string]any{}
	for k, v := range p.Labels {
		labels[k] = v
	}
	md := map[string]any{"name": p.Name, "namespace": p.Namespace, "labels": labels}
	if !p.Created.IsZero() {
		md["creationTimestamp"] = p.Created.UTC().Format(time.RFC3339)
	}
	if p.OwnerKind != "" {
		md["ownerReferences"] = []any{map[string]any{
			"apiVersion": ownerAPIVersion(p.OwnerKind), "kind": p.OwnerKind, "name": p.OwnerName, "uid": "owner-" + p.OwnerName, "controller": true,
		}}
	}
	cond := "False"
	if ready {
		cond = "True"
	}
	cs := map[string]any{"name": "main", "image": p.Image, "ready": ready, "restartCount": p.Restarts, "state": state, "started": ready}
	if p.LastReason != "" {
		cs["lastState"] = map[string]any{"terminated": map[string]any{
			"reason": p.LastReason, "exitCode": 1, "finishedAt": time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339),
		}}
	}
	status := map[string]any{
		"phase": p.Phase, "hostIP": "", "podIP": p.IP, "qosClass": "BestEffort",
		"conditions":        []any{map[string]any{"type": "Ready", "status": cond}},
		"containerStatuses": []any{cs},
	}
	spec := map[string]any{"nodeName": p.Node, "containers": []any{map[string]any{"name": "main", "image": p.Image}}}
	var volumes []any
	if p.Claim != "" {
		volumes = append(volumes, map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": p.Claim}})
	}
	if p.EmptyDir {
		volumes = append(volumes, map[string]any{"name": "cache", "emptyDir": map[string]any{}})
	}
	if volumes != nil {
		spec["volumes"] = volumes
	}
	if p.Init != "" {
		spec["initContainers"] = []any{map[string]any{"name": p.Init, "image": "docker.io/library/busybox:1.37"}}
		status["initContainerStatuses"] = []any{map[string]any{
			"name": p.Init, "image": "docker.io/library/busybox:1.37", "ready": true, "restartCount": 0,
			"state": map[string]any{"terminated": map[string]any{"reason": "Completed", "exitCode": 0}},
		}}
	}
	if p.Phase != "Pending" || p.Node != "" {
		status["startTime"] = time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339)
	}
	return map[string]any{
		"apiVersion": "v1", "kind": "Pod", "metadata": md,
		"spec": spec, "status": status,
	}
}

func ownerAPIVersion(kind string) string {
	switch kind {
	case "ReplicaSet", "StatefulSet", "DaemonSet", "Deployment":
		return "apps/v1"
	case "Job", "CronJob":
		return "batch/v1"
	case "Ingress":
		return "networking.k8s.io/v1"
	}
	return "v1"
}

// WorkloadSpec beschrijft een Deployment, StatefulSet of DaemonSet.
type WorkloadSpec struct {
	Namespace string
	Name      string
	Image     string
	Replicas  int
	Ready     int
	// Generation en ObservedGeneration; gelijk als de uitrol klaar is.
	Generation         int
	ObservedGeneration int
	// Old is het aantal oude pods van een Deployment die nog wachten op de
	// nieuwe, zoals bij een uitrol die hangt; Ready telt ze mee.
	Old int
}

func (w WorkloadSpec) meta() map[string]any {
	gen := max(w.Generation, 1)
	return map[string]any{"name": w.Name, "namespace": w.Namespace, "generation": gen, "labels": map[string]any{"app": w.Name}}
}

func (w WorkloadSpec) template() map[string]any {
	return map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": w.Name}},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": w.Name, "image": w.Image}}},
	}
}

func (w WorkloadSpec) observed() int {
	if w.ObservedGeneration > 0 {
		return w.ObservedGeneration
	}
	return max(w.Generation, 1)
}

// Deployment maakt een Deployment.
func Deployment(w WorkloadSpec) map[string]any {
	return map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment", "metadata": w.meta(),
		"spec": map[string]any{
			"replicas": w.Replicas, "selector": map[string]any{"matchLabels": map[string]any{"app": w.Name}},
			"template": w.template(), "strategy": map[string]any{"type": "RollingUpdate"},
		},
		"status": map[string]any{
			"observedGeneration": w.observed(), "replicas": w.Replicas + w.Old, "readyReplicas": w.Ready, "availableReplicas": w.Ready,
			"updatedReplicas": w.Replicas,
		},
	}
}

// StatefulSet maakt een StatefulSet.
func StatefulSet(w WorkloadSpec) map[string]any {
	return map[string]any{
		"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": w.meta(),
		"spec": map[string]any{
			"replicas": w.Replicas, "selector": map[string]any{"matchLabels": map[string]any{"app": w.Name}},
			"template": w.template(), "serviceName": w.Name,
		},
		"status": map[string]any{
			"observedGeneration": w.observed(), "replicas": w.Replicas, "readyReplicas": w.Ready, "availableReplicas": w.Ready,
			"currentReplicas": w.Replicas, "updatedReplicas": w.Replicas,
		},
	}
}

// DaemonSet maakt een DaemonSet; Replicas is het aantal nodes waarop hij
// moet draaien.
func DaemonSet(w WorkloadSpec) map[string]any {
	return map[string]any{
		"apiVersion": "apps/v1", "kind": "DaemonSet", "metadata": w.meta(),
		"spec": map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": w.Name}}, "template": w.template()},
		"status": map[string]any{
			"observedGeneration": w.observed(), "desiredNumberScheduled": w.Replicas, "currentNumberScheduled": w.Replicas,
			"numberReady": w.Ready, "numberAvailable": w.Ready, "updatedNumberScheduled": w.Replicas,
		},
	}
}

// PodMetrics maakt de meting van metrics-server voor een pod met één
// container, met CPU en geheugen zoals Kubernetes ze schrijft: 250m en
// 128Mi.
func PodMetrics(namespace, name, cpu, memory string) map[string]any {
	return map[string]any{
		"apiVersion": "metrics.k8s.io/v1beta1", "kind": "PodMetrics",
		"metadata":  map[string]any{"name": name, "namespace": namespace},
		"timestamp": time.Now().UTC().Format(time.RFC3339), "window": "15s",
		"containers": []any{map[string]any{"name": "main", "usage": map[string]any{"cpu": cpu, "memory": memory}}},
	}
}

// NodeMetrics maakt de meting van metrics-server voor een node.
func NodeMetrics(name, cpu, memory string) map[string]any {
	return map[string]any{
		"apiVersion": "metrics.k8s.io/v1beta1", "kind": "NodeMetrics", "metadata": map[string]any{"name": name},
		"timestamp": time.Now().UTC().Format(time.RFC3339), "window": "15s", "usage": map[string]any{"cpu": cpu, "memory": memory},
	}
}

// Namespace maakt een Namespace.
func Namespace(name string) map[string]any {
	return map[string]any{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": map[string]any{"name": name, "labels": map[string]any{"kubernetes.io/metadata.name": name}},
		"spec":     map[string]any{"finalizers": []any{"kubernetes"}}, "status": map[string]any{"phase": "Active"},
	}
}

// SeedK3s vult de API zoals een vers k3s-cluster: de systeemnamespaces, de
// nodes, en CoreDNS, Traefik, local-path, metrics-server en ServiceLB met
// hun pods.
func (s *Server) SeedK3s(nodes ...NodeSpec) {
	for _, ns := range []string{"default", "kube-system", "kube-public", "kube-node-lease"} {
		s.Add(Namespace(ns))
	}
	first := ""
	for _, n := range nodes {
		s.Add(Node(n))
		if first == "" {
			first = n.Name
		}
	}
	type sys struct{ name, image string }
	for i, d := range []sys{
		{"coredns", "rancher/mirrored-coredns-coredns:1.12.3"},
		{"local-path-provisioner", "rancher/local-path-provisioner:v0.0.32"},
		{"metrics-server", "rancher/mirrored-metrics-server:v0.8.0"},
		{"traefik", "rancher/mirrored-library-traefik:3.5.1"},
	} {
		s.Add(Deployment(WorkloadSpec{Namespace: "kube-system", Name: d.name, Image: d.image, Replicas: 1, Ready: 1}))
		node := nodes[i%len(nodes)].Name
		rs := d.name + "-5d9c7b6f4"
		s.Add(Pod(PodSpec{
			Namespace: "kube-system", Name: fmt.Sprintf("%s-%s-x%dq", d.name, "5d9c7b6f4", i), Node: node, Image: d.image,
			Labels: map[string]string{"app": d.name}, OwnerKind: "ReplicaSet", OwnerName: rs, IP: fmt.Sprintf("10.42.0.%d", 10+i),
		}))
	}
	s.Add(DaemonSet(WorkloadSpec{Namespace: "kube-system", Name: "svclb-traefik-2f0b8c41", Image: "rancher/klipper-lb:v0.4.13", Replicas: len(nodes), Ready: len(nodes)}))
	for i, n := range nodes {
		s.Add(Pod(PodSpec{
			Namespace: "kube-system", Name: fmt.Sprintf("svclb-traefik-2f0b8c41-%c%c", 'a'+i, 'k'+i), Node: n.Name, Image: "rancher/klipper-lb:v0.4.13",
			Labels: map[string]string{"app": "svclb-traefik-2f0b8c41"}, OwnerKind: "DaemonSet", OwnerName: "svclb-traefik-2f0b8c41", IP: fmt.Sprintf("10.42.%d.2", i),
		}))
	}
	for _, j := range []string{"helm-install-traefik-crd", "helm-install-traefik"} {
		s.Add(Pod(PodSpec{
			Namespace: "kube-system", Name: j + "-q7m2c", Node: first, Phase: "Succeeded", Image: "rancher/klipper-helm:v0.9.8-build20250709",
			OwnerKind: "Job", OwnerName: j,
		}))
	}
	s.Add(Service(ServiceSpec{
		Namespace: "default", Name: "kubernetes", ClusterIP: "10.43.0.1", Ports: []ServicePort{{Name: "https", Port: 443, TargetPort: 6443}},
	}))
	var ips []string
	for _, n := range nodes {
		ips = append(ips, n.IP)
	}
	s.Add(Service(ServiceSpec{
		Namespace: "kube-system", Name: "traefik", Type: "LoadBalancer", ClusterIP: "10.43.120.15", Selector: map[string]string{"app": "traefik"},
		LoadBalancer: ips, Ports: []ServicePort{{Name: "web", Port: 80, TargetPort: 8000, NodePort: 30080}, {Name: "websecure", Port: 443, TargetPort: 8443, NodePort: 30443}},
	}))
	s.Add(
		StorageClass("local-path", "rancher.io/local-path", true),
		map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "IngressClass", "metadata": map[string]any{"name": "traefik"}, "spec": map[string]any{"controller": "traefik.io/ingress-controller"}},
		ConfigMap("kube-system", "coredns", map[string]string{"Corefile": ".:53 {\n    errors\n    health\n    kubernetes cluster.local in-addr.arpa ip6.arpa\n    forward . /etc/resolv.conf\n}\n"}),
	)
	s.Add(map[string]any{
		"apiVersion": "v1", "kind": "Secret", "type": "kubernetes.io/tls",
		"metadata": map[string]any{"name": "k3s-serving", "namespace": "kube-system"},
		"data":     map[string]any{"tls.crt": "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0t", "tls.key": "LS0tLS1CRUdJTiBFQyBQUklWQVRFIEtFWS0tLS0t"},
	})
}

// ReplicaSet maakt de ReplicaSet van een Deployment.
func ReplicaSet(namespace, name, deployment string, replicas int) map[string]any {
	return map[string]any{
		"apiVersion": "apps/v1", "kind": "ReplicaSet",
		"metadata": map[string]any{
			"name": name, "namespace": namespace, "labels": map[string]any{"app": deployment},
			"ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": deployment, "uid": "owner-" + deployment, "controller": true}},
		},
		"spec":   map[string]any{"replicas": replicas, "selector": map[string]any{"matchLabels": map[string]any{"app": deployment}}},
		"status": map[string]any{"replicas": replicas, "readyReplicas": replicas},
	}
}

// Revision maakt de ReplicaSet van één versie van een Deployment, zoals
// de Deployment-controller hem maakt: met de hash in de naam en de labels,
// en het versienummer in een annotatie.
func Revision(namespace, deployment, hash string, revision, replicas int, image string) map[string]any {
	labels := map[string]any{"app": deployment, "pod-template-hash": hash}
	return map[string]any{
		"apiVersion": "apps/v1", "kind": "ReplicaSet",
		"metadata": map[string]any{
			"name": deployment + "-" + hash, "namespace": namespace, "labels": labels,
			"annotations": map[string]any{"deployment.kubernetes.io/revision": strconv.Itoa(revision)},
			"ownerReferences": []any{map[string]any{
				"apiVersion": "apps/v1", "kind": "Deployment", "name": deployment, "uid": "owner-" + deployment, "controller": true,
			}},
		},
		"spec": map[string]any{
			"replicas": replicas, "selector": map[string]any{"matchLabels": labels},
			"template": map[string]any{
				"metadata": map[string]any{"labels": labels},
				"spec":     map[string]any{"containers": []any{map[string]any{"name": deployment, "image": image}}},
			},
		},
		"status": map[string]any{"replicas": replicas, "readyReplicas": replicas},
	}
}

// ServicePort is een poort van een Service.
type ServicePort struct {
	Name       string
	Port       int
	TargetPort int
	NodePort   int
}

// ServiceSpec beschrijft een Service.
type ServiceSpec struct {
	Namespace string
	Name      string
	// Type is ClusterIP, NodePort of LoadBalancer; leeg is ClusterIP.
	Type      string
	ClusterIP string
	Selector  map[string]string
	Ports     []ServicePort
	// LoadBalancer zijn de adressen die ServiceLB gaf.
	LoadBalancer []string
}

// Service maakt een Service.
func Service(sv ServiceSpec) map[string]any {
	if sv.Type == "" {
		sv.Type = "ClusterIP"
	}
	var ports []any
	for _, p := range sv.Ports {
		port := map[string]any{"name": p.Name, "port": p.Port, "protocol": "TCP", "targetPort": max(p.TargetPort, p.Port)}
		if p.NodePort > 0 {
			port["nodePort"] = p.NodePort
		}
		ports = append(ports, port)
	}
	sel := map[string]any{}
	for k, v := range sv.Selector {
		sel[k] = v
	}
	spec := map[string]any{"type": sv.Type, "clusterIP": sv.ClusterIP, "ports": ports}
	if len(sel) > 0 {
		spec["selector"] = sel
	}
	var lb []any
	for _, ip := range sv.LoadBalancer {
		lb = append(lb, map[string]any{"ip": ip, "ipMode": "VIP"})
	}
	return map[string]any{
		"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": sv.Name, "namespace": sv.Namespace},
		"spec": spec, "status": map[string]any{"loadBalancer": map[string]any{"ingress": lb}},
	}
}

// IngressSpec beschrijft een Ingress met één regel.
type IngressSpec struct {
	Namespace string
	Name      string
	Host      string
	Path      string
	Service   string
	Port      int
	TLS       bool
	// Address is het adres in de status, zoals Traefik het zet.
	Address []string
}

// Ingress maakt een Ingress voor Traefik.
func Ingress(in IngressSpec) map[string]any {
	if in.Path == "" {
		in.Path = "/"
	}
	spec := map[string]any{
		"ingressClassName": "traefik",
		"rules": []any{map[string]any{"host": in.Host, "http": map[string]any{"paths": []any{map[string]any{
			"path": in.Path, "pathType": "Prefix",
			"backend": map[string]any{"service": map[string]any{"name": in.Service, "port": map[string]any{"number": in.Port}}},
		}}}}},
	}
	if in.TLS {
		spec["tls"] = []any{map[string]any{"hosts": []any{in.Host}, "secretName": in.Name + "-tls"}}
	}
	var lb []any
	for _, ip := range in.Address {
		lb = append(lb, map[string]any{"ip": ip})
	}
	return map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "Ingress", "metadata": map[string]any{"name": in.Name, "namespace": in.Namespace},
		"spec": spec, "status": map[string]any{"loadBalancer": map[string]any{"ingress": lb}},
	}
}

// VolumeSpec beschrijft een PersistentVolumeClaim met zijn volume.
type VolumeSpec struct {
	Namespace string
	Claim     string
	Volume    string
	Size      string
	Class     string
	// Node is de node waar local-path het volume maakte.
	Node string
	// Pending: de claim wacht nog op een volume.
	Pending bool
}

// Volume maakt een PersistentVolumeClaim en, als hij gebonden is, het
// PersistentVolume.
func Volume(v VolumeSpec) []any {
	if v.Class == "" {
		v.Class = "local-path"
	}
	claim := map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": v.Claim, "namespace": v.Namespace},
		"spec": map[string]any{
			"accessModes": []any{"ReadWriteOnce"}, "storageClassName": v.Class,
			"resources": map[string]any{"requests": map[string]any{"storage": v.Size}},
		},
		"status": map[string]any{"phase": "Pending"},
	}
	if v.Pending {
		return []any{claim}
	}
	claim["spec"].(map[string]any)["volumeName"] = v.Volume
	claim["status"] = map[string]any{"phase": "Bound", "accessModes": []any{"ReadWriteOnce"}, "capacity": map[string]any{"storage": v.Size}}
	pv := map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolume", "metadata": map[string]any{"name": v.Volume},
		"spec": map[string]any{
			"capacity": map[string]any{"storage": v.Size}, "accessModes": []any{"ReadWriteOnce"}, "storageClassName": v.Class,
			"persistentVolumeReclaimPolicy": "Delete",
			"claimRef":                      map[string]any{"kind": "PersistentVolumeClaim", "namespace": v.Namespace, "name": v.Claim},
			"hostPath":                      map[string]any{"path": "/var/lib/rancher/k3s/storage/" + v.Volume + "_" + v.Namespace + "_" + v.Claim},
			"nodeAffinity": map[string]any{"required": map[string]any{"nodeSelectorTerms": []any{map[string]any{"matchExpressions": []any{
				map[string]any{"key": "kubernetes.io/hostname", "operator": "In", "values": []any{v.Node}},
			}}}}},
		},
		"status": map[string]any{"phase": "Bound"},
	}
	return []any{claim, pv}
}

// StorageClass maakt een StorageClass.
func StorageClass(name, provisioner string, isDefault bool) map[string]any {
	md := map[string]any{"name": name}
	if isDefault {
		md["annotations"] = map[string]any{"storageclass.kubernetes.io/is-default-class": "true"}
	}
	return map[string]any{
		"apiVersion": "storage.k8s.io/v1", "kind": "StorageClass", "metadata": md, "provisioner": provisioner,
		"reclaimPolicy": "Delete", "volumeBindingMode": "WaitForFirstConsumer",
	}
}

// ConfigMap maakt een ConfigMap.
func ConfigMap(namespace, name string, data map[string]string) map[string]any {
	d := map[string]any{}
	for k, v := range data {
		d[k] = v
	}
	return map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": name, "namespace": namespace}, "data": d}
}

// Secret maakt een Secret; de waarden gaan zoals in Kubernetes als base64.
func Secret(namespace, name, typ string, data map[string]string) map[string]any {
	d := map[string]any{}
	for k, v := range data {
		d[k] = base64.StdEncoding.EncodeToString([]byte(v))
	}
	if typ == "" {
		typ = "Opaque"
	}
	return map[string]any{"apiVersion": "v1", "kind": "Secret", "type": typ, "metadata": map[string]any{"name": name, "namespace": namespace}, "data": d}
}

// EventSpec beschrijft een event over een object.
type EventSpec struct {
	Namespace string
	Name      string
	// Type is Normal of Warning; leeg is Normal.
	Type    string
	Reason  string
	Message string
	Count   int
	// Het object waar het event over gaat.
	Kind, ObjectName, UID string
	// Ago is hoe lang geleden het event laatst gebeurde.
	Ago time.Duration
}

// Event maakt een event.
func Event(e EventSpec) map[string]any {
	if e.Type == "" {
		e.Type = "Normal"
	}
	last := time.Now().UTC().Add(-e.Ago).Format(time.RFC3339)
	return map[string]any{
		"apiVersion": "v1", "kind": "Event", "metadata": map[string]any{"name": e.Name, "namespace": e.Namespace},
		"type": e.Type, "reason": e.Reason, "message": e.Message, "count": max(e.Count, 1),
		"firstTimestamp": last, "lastTimestamp": last, "source": map[string]any{"component": "kubelet"},
		"involvedObject": map[string]any{"apiVersion": ownerAPIVersion(e.Kind), "kind": e.Kind, "namespace": e.Namespace, "name": e.ObjectName, "uid": e.UID},
	}
}
