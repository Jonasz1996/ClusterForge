package kubefake

import (
	"fmt"
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
	if p.OwnerKind != "" {
		md["ownerReferences"] = []any{map[string]any{
			"apiVersion": ownerAPIVersion(p.OwnerKind), "kind": p.OwnerKind, "name": p.OwnerName, "uid": "owner-" + p.OwnerName, "controller": true,
		}}
	}
	cond := "False"
	if ready {
		cond = "True"
	}
	status := map[string]any{
		"phase": p.Phase, "hostIP": "", "podIP": p.IP,
		"conditions": []any{map[string]any{"type": "Ready", "status": cond}},
		"containerStatuses": []any{map[string]any{
			"name": "main", "image": p.Image, "ready": ready, "restartCount": p.Restarts, "state": state, "started": ready,
		}},
	}
	if p.Phase != "Pending" || p.Node != "" {
		status["startTime"] = time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339)
	}
	return map[string]any{
		"apiVersion": "v1", "kind": "Pod", "metadata": md,
		"spec":   map[string]any{"nodeName": p.Node, "containers": []any{map[string]any{"name": "main", "image": p.Image}}},
		"status": status,
	}
}

func ownerAPIVersion(kind string) string {
	switch kind {
	case "ReplicaSet", "StatefulSet", "DaemonSet", "Deployment":
		return "apps/v1"
	case "Job", "CronJob":
		return "batch/v1"
	case "Node":
		return "v1"
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
			"observedGeneration": w.observed(), "replicas": w.Replicas, "readyReplicas": w.Ready, "availableReplicas": w.Ready,
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
	s.Add(map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{"name": "kubernetes", "namespace": "default", "labels": map[string]any{"component": "apiserver"}},
		"spec":     map[string]any{"type": "ClusterIP", "clusterIP": "10.43.0.1", "ports": []any{map[string]any{"name": "https", "port": 443, "protocol": "TCP", "targetPort": 6443}}},
	})
	s.Add(map[string]any{
		"apiVersion": "v1", "kind": "Secret", "type": "kubernetes.io/tls",
		"metadata": map[string]any{"name": "k3s-serving", "namespace": "kube-system"},
		"data":     map[string]any{"tls.crt": "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0t", "tls.key": "LS0tLS1CRUdJTiBFQyBQUklWQVRFIEtFWS0tLS0t"},
	})
}
