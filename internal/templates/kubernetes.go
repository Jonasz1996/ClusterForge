package templates

import (
	"cmp"
	"fmt"
	"strconv"
)

// KubeVersion is de k3s-release die een templateversie installeert, of ""
// als de template geen Kubernetes is.
func (t *Template) KubeVersion() string {
	if t.Kubernetes == nil {
		return ""
	}
	return t.Kubernetes.Version
}

// KubeMinor geeft de minorversie van Kubernetes, zoals 36 bij
// v1.36.5+k3s1.
func KubeMinor(version string) (int, bool) {
	m := k3sVersionRe.FindStringSubmatch(version)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	return n, true
}

// CompareKube vergelijkt twee k3s-releases: minor, patch en dan de k3s-ronde.
func CompareKube(a, b string) int {
	ma, mb := k3sVersionRe.FindStringSubmatch(a), k3sVersionRe.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		return cmp.Compare(a, b)
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(ma[i])
		y, _ := strconv.Atoi(mb[i])
		if x != y {
			return cmp.Compare(x, y)
		}
	}
	return 0
}

// ShortKube is een k3s-release zonder de k3s-ronde, zoals v1.36.5.
func ShortKube(version string) string {
	if m := k3sVersionRe.FindStringSubmatch(version); m != nil {
		return "v1." + m[1] + "." + m[2]
	}
	return version
}

// KubeStep controleert of een cluster van templateversie from naar to kan.
// Kubernetes gaat één minorversie per keer omhoog en nooit terug: een
// kubelet mag niet nieuwer zijn dan de API-server, en etcd kent geen weg
// terug. Bij een template zonder Kubernetes is alles goed.
func KubeStep(from, to *Template) error {
	a, b := from.KubeVersion(), to.KubeVersion()
	if a == "" || b == "" || a == b {
		return nil
	}
	ma, _ := KubeMinor(a)
	mb, _ := KubeMinor(b)
	switch {
	case CompareKube(b, a) < 0:
		return fmt.Errorf("%s %s installeert k3s %s, ouder dan de %s die nu draait; Kubernetes kan niet terug naar een oudere versie",
			to.Name, to.Version, ShortKube(b), ShortKube(a))
	case mb > ma+1:
		return fmt.Errorf("van Kubernetes 1.%d kan een cluster alleen naar 1.%d, niet meteen naar 1.%d: Kubernetes gaat één minorversie per keer omhoog. "+
			"Kies eerst een versie van %s met k3s 1.%d", ma, ma+1, mb, to.Name, ma+1)
	}
	return nil
}

// KubeNext geeft de versie van een template waar een cluster op version nu
// naartoe kan: de nieuwste met hoogstens één minorversie van Kubernetes
// erbij. Nil als er geen nieuwere is, of als de template geen Kubernetes is.
func (r *Registry) KubeNext(name, version string) *Template {
	cur, ok := r.Get(name, version)
	if !ok || cur.KubeVersion() == "" {
		return nil
	}
	var next *Template
	for _, t := range r.byName[name] {
		if compareVersions(t.Version, cur.Version) <= 0 || CompareKube(t.KubeVersion(), cur.KubeVersion()) <= 0 || KubeStep(cur, t) != nil {
			continue
		}
		next = t
	}
	return next
}

// Upto geeft een kopie van de registry waarin template name hoogstens
// versie max heeft, zoals een oudere ClusterForge. Tests rollen er een
// cluster mee uit op een oudere versie.
func (r *Registry) Upto(name, max string) *Registry {
	out := &Registry{byName: map[string][]*Template{}}
	for n, ts := range r.byName {
		for _, t := range ts {
			if n != name || compareVersions(t.Version, max) <= 0 {
				out.byName[n] = append(out.byName[n], t)
			}
		}
	}
	return out
}
