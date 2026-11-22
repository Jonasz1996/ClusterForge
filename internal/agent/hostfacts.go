package agent

import (
	"bufio"
	"crypto/x509"
	"encoding/pem"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// confNode is een regel uit een configuratie in de stijl van keepalived:
// woorden, en eventueel een blok met regels.
type confNode struct {
	words    []string
	children []*confNode
}

// parseConfBlocks leest keepalived.conf als boom. Commentaar (# en !) valt
// weg; includes worden niet gevolgd.
func parseConfBlocks(conf string) []*confNode {
	root := &confNode{}
	stack := []*confNode{root}
	var last *confNode
	sc := bufio.NewScanner(strings.NewReader(conf))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexAny(line, "#!"); i >= 0 {
			line = line[:i]
		}
		line = strings.NewReplacer("{", " { ", "}", " } ").Replace(line)
		var cur *confNode
		for _, w := range strings.Fields(line) {
			top := stack[len(stack)-1]
			switch w {
			case "{":
				// Een blok hoort bij de regel ervoor als het op een eigen
				// regel begint.
				owner := cur
				if owner == nil {
					owner = last
				}
				if owner == nil {
					owner = &confNode{}
					top.children = append(top.children, owner)
				}
				stack = append(stack, owner)
				cur, last = nil, nil
			case "}":
				if len(stack) > 1 {
					stack = stack[:len(stack)-1]
				}
				cur, last = nil, nil
			default:
				if cur == nil {
					cur = &confNode{}
					top.children = append(top.children, cur)
				}
				cur.words = append(cur.words, w)
			}
		}
		if cur != nil {
			last = cur
		}
	}
	return root.children
}

// parseKeepalivedInstances haalt de vrrp_instance-blokken uit de
// configuratie.
func parseKeepalivedInstances(conf string) []protocol.VRRPInstance {
	var out []protocol.VRRPInstance
	for _, n := range parseConfBlocks(conf) {
		if len(n.words) == 0 || n.words[0] != "vrrp_instance" {
			continue
		}
		inst := protocol.VRRPInstance{VIPs: []string{}}
		if len(n.words) > 1 {
			inst.Name = n.words[1]
		}
		for _, ch := range n.children {
			if len(ch.words) == 0 {
				continue
			}
			arg := ""
			if len(ch.words) > 1 {
				arg = ch.words[1]
			}
			switch ch.words[0] {
			case "interface":
				inst.Interface = arg
			case "virtual_router_id":
				inst.VRID, _ = strconv.Atoi(arg)
			case "state":
				inst.State = strings.ToUpper(arg)
			case "priority":
				inst.Priority, _ = strconv.Atoi(arg)
			case "unicast_peer":
				for _, p := range ch.children {
					if len(p.words) > 0 && net.ParseIP(p.words[0]) != nil {
						inst.Peers = append(inst.Peers, p.words[0])
					}
				}
			case "track_script":
				// Een regel per script, of track_script { a b } op één
				// regel; een gewicht (weight -20) hoort bij het script.
				for _, p := range ch.children {
					for _, w := range p.words {
						if w != "weight" && strings.TrimLeft(w, "-+0123456789") != "" {
							inst.TrackScripts = append(inst.TrackScripts, w)
						}
					}
				}
			case "virtual_ipaddress", "virtual_ipaddress_excluded":
				for _, p := range ch.children {
					if len(p.words) == 0 {
						continue
					}
					addr, _, _ := strings.Cut(p.words[0], "/")
					if net.ParseIP(addr) != nil {
						inst.VIPs = append(inst.VIPs, addr)
					}
				}
			}
		}
		out = append(out, inst)
	}
	return out
}

// maintenanceFacts zet reboot_required, de nieuwste kernel en de leeftijd
// van de pakketlijsten.
func (c *Collector) maintenanceFacts(f *protocol.Facts) {
	f.KernelInstalled = c.newestKernel()
	_, err1 := os.Stat(c.path("/run/reboot-required"))
	_, err2 := os.Stat(c.path("/var/run/reboot-required"))
	f.RebootRequired = err1 == nil || err2 == nil || (f.KernelInstalled != "" && f.Kernel != "" && f.KernelInstalled != f.Kernel)
	for _, p := range []string{"/var/lib/apt/periodic/update-success-stamp", "/var/lib/apt/lists"} {
		if st, err := os.Stat(c.path(p)); err == nil && st.ModTime().After(f.AptUpdatedAt) {
			f.AptUpdatedAt = st.ModTime().UTC().Truncate(time.Second)
		}
	}
}

// newestKernel kijkt in /boot naar vmlinuz-<versie>. Een container heeft
// geen eigen kernel; dan is het leeg.
func (c *Collector) newestKernel() string {
	matches, _ := filepath.Glob(c.path("/boot/vmlinuz-*"))
	best := ""
	for _, m := range matches {
		v := strings.TrimPrefix(filepath.Base(m), "vmlinuz-")
		if best == "" || versionLess(best, v) {
			best = v
		}
	}
	return best
}

// versionLess vergelijkt kernelversies stuk voor stuk, cijfers als getal.
func versionLess(a, b string) bool {
	split := func(s string) []string {
		var out []string
		cur, digit := "", false
		for _, r := range s {
			d := unicode.IsDigit(r)
			if cur != "" && d != digit {
				out = append(out, cur)
				cur = ""
			}
			cur += string(r)
			digit = d
		}
		if cur != "" {
			out = append(out, cur)
		}
		return out
	}
	pa, pb := split(a), split(b)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] == pb[i] {
			continue
		}
		na, err1 := strconv.Atoi(pa[i])
		nb, err2 := strconv.Atoi(pb[i])
		if err1 == nil && err2 == nil {
			return na < nb
		}
		return pa[i] < pb[i]
	}
	return len(pa) < len(pb)
}

// certDirs zijn de plekken waar de agent naar servercertificaten zoekt.
// Mappen met sleutels, zoals /etc/ssl/private, horen er niet bij.
var certDirs = []string{
	"/etc/letsencrypt/live", "/etc/nginx", "/etc/apache2", "/etc/haproxy", "/etc/pve/local",
	"/etc/postgresql", "/etc/mysql", "/etc/redis", "/etc/cf-app", "/etc/ssl/localcerts",
}

const (
	maxCertFiles = 500
	maxCertSize  = 1 << 20
	maxCerts     = 50
)

// certificates leest de servercertificaten in certDirs. Alleen blokken
// CERTIFICATE worden gelezen; een sleutel in hetzelfde bestand wordt
// overgeslagen en nooit doorgegeven. CA's tellen niet mee.
func (c *Collector) certificates() []protocol.Certificate {
	var out []protocol.Certificate
	seen := map[string]bool{}
	files := 0
	for _, dir := range certDirs {
		root := c.path(dir)
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || files >= maxCertFiles {
				return filepath.SkipDir
			}
			rel, _ := filepath.Rel(root, p)
			if d.IsDir() {
				if strings.Count(rel, string(filepath.Separator)) >= 3 {
					return filepath.SkipDir
				}
				return nil
			}
			switch strings.ToLower(filepath.Ext(p)) {
			case ".pem", ".crt", ".cer":
			default:
				return nil
			}
			st, err := os.Stat(p)
			if err != nil || !st.Mode().IsRegular() || st.Size() > maxCertSize {
				return nil
			}
			files++
			data, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			for len(data) > 0 {
				var block *pem.Block
				block, data = pem.Decode(data)
				if block == nil {
					break
				}
				if block.Type != "CERTIFICATE" {
					continue
				}
				cert, err := x509.ParseCertificate(block.Bytes)
				if err != nil || cert.IsCA {
					continue
				}
				key := cert.Issuer.String() + "/" + cert.SerialNumber.String()
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, protocol.Certificate{
					Path: filepath.Join(dir, rel), Subject: cert.Subject.CommonName, DNSNames: limitStrings(cert.DNSNames, 20),
					Issuer: cert.Issuer.CommonName, NotAfter: cert.NotAfter.UTC(),
				})
			}
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].NotAfter.Equal(out[j].NotAfter) {
			return out[i].NotAfter.Before(out[j].NotAfter)
		}
		return out[i].Path < out[j].Path
	})
	if len(out) > maxCerts {
		out = out[:maxCerts]
	}
	return out
}

func limitStrings(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
