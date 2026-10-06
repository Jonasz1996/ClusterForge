package backups

import (
	"encoding/base64"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/proxmox"
)

// Een sandbox is een teruggezette kopie van een productie-VM. Hij mag het
// netwerk nooit bereiken: elke netwerkkaart krijgt link_down=1, en alleen een
// VM waarvan elke configsleutel hieronder staat, wordt gestart. Een sleutel
// die de VM aan iets van de host koppelt (hostpci, usb, virtiofs, args,
// hookscript) staat er bewust niet in.

var (
	// indexed zijn sleutels met een volgnummer, zoals net0 of scsi1.
	indexed = regexp.MustCompile(`^(net|scsi|virtio|sata|ide|unused|ipconfig|serial|numa)([0-9]+)$`)
	// storageVolume is een volume op een Proxmox-storage, zoals
	// local-lvm:vm-131-disk-0.
	storageVolume = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]*:[^,]+$`)

	// allowed zijn de sleutels zonder volgnummer die mogen.
	allowed = map[string]bool{
		// Hardware binnen de VM.
		"cores": true, "sockets": true, "vcpus": true, "memory": true, "balloon": true, "shares": true,
		"cpu": true, "cpuunits": true, "cpulimit": true, "affinity": true, "numa": true, "machine": true,
		"bios": true, "boot": true, "bootdisk": true, "agent": true, "smbios1": true, "vmgenid": true,
		"ostype": true, "scsihw": true, "vga": true, "kvm": true, "acpi": true, "tablet": true,
		"hotplug": true, "localtime": true, "keyboard": true, "freeze": true, "watchdog": true,
		"efidisk0": true, "tpmstate0": true,
		// Cloud-init.
		"ciuser": true, "cipassword": true, "citype": true, "ciupgrade": true, "cicustom": true,
		"nameserver": true, "searchdomain": true, "sshkeys": true,
		// Beschrijving en beheer.
		"name": true, "tags": true, "description": true, "digest": true, "meta": true,
		"onboot": true, "protection": true, "startup": true, "template": true,
	}
)

// diskKey is true voor een configsleutel die een schijf is.
func diskKey(k string) bool {
	if k == "efidisk0" || k == "tpmstate0" {
		return true
	}
	m := indexed.FindStringSubmatch(k)
	return m != nil && slices.Contains([]string{"scsi", "virtio", "sata", "ide", "unused"}, m[1])
}

// netKey is true voor een netwerkkaart.
func netKey(k string) bool {
	m := indexed.FindStringSubmatch(k)
	return m != nil && m[1] == "net"
}

// notIsolatable geeft de sleutels die niet op de allowlist staan, of wier
// waarde de VM aan iets buiten zichzelf koppelt, met de reden erbij.
func notIsolatable(cfg map[string]string) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(cfg)) {
		v := cfg[k]
		m := indexed.FindStringSubmatch(k)
		switch {
		case diskKey(k):
			if !diskOnStorage(v) {
				out = append(out, k+" (schijf buiten een Proxmox-storage)")
			}
		case m != nil && m[1] == "serial":
			if v != "socket" {
				out = append(out, k+" (seriële poort van de host)")
			}
		case m != nil:
			// net, ipconfig en numa.
		case !allowed[k]:
			out = append(out, k)
		}
	}
	return out
}

// diskOnStorage is true als een schijf op een Proxmox-storage staat, of een
// lege cd-speler is. Een schijf van de host (/dev/...) mag niet.
func diskOnStorage(v string) bool {
	vol, _, _ := strings.Cut(v, ",")
	if vol == "none" || vol == "cdrom" {
		return true
	}
	vol = strings.TrimPrefix(vol, "file=")
	return storageVolume.MatchString(vol) && !strings.HasPrefix(vol, "/")
}

// option leest een optie uit een Proxmox-eigenschap, zoals size uit
// "local-lvm:vm-1-disk-0,size=32G"; de eerste waarde zonder naam heet def.
func option(v, name, def string) (string, bool) {
	for i, part := range strings.Split(v, ",") {
		k, val, found := strings.Cut(part, "=")
		if !found {
			if i == 0 && name == def {
				return part, true
			}
			continue
		}
		if k == name {
			return val, true
		}
	}
	return "", false
}

// setOption zet of vervangt één optie in een Proxmox-eigenschap.
func setOption(v, name, value string) string {
	parts := strings.Split(v, ",")
	for i, part := range parts {
		if k, _, found := strings.Cut(part, "="); found && k == name {
			parts[i] = name + "=" + value
			return strings.Join(parts, ",")
		}
	}
	return v + "," + name + "=" + value
}

// diskBytes telt de grootte van de schijven in een configuratie: wat het
// terugzetten op de sandbox-storage nodig heeft.
func diskBytes(cfg map[string]string) int64 {
	var total int64
	for k, v := range cfg {
		if !diskKey(k) || strings.Contains(v, "media=cdrom") {
			continue
		}
		if size, ok := option(v, "size", ""); ok {
			total += parseSize(size)
		}
	}
	return total
}

// parseSize leest een grootte zoals 32G, 512M of 4T; zonder eenheid bytes.
func parseSize(s string) int64 {
	if s == "" {
		return 0
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'K', 'k':
		mult = 1 << 10
	case 'M', 'm':
		mult = 1 << 20
	case 'G', 'g':
		mult = 1 << 30
	case 'T', 't':
		mult = 1 << 40
	}
	if mult != 1 {
		s = s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(f * float64(mult))
}

// defaultMemoryMB is het geheugen als de config niets zegt. Proxmox zelf
// kiest dan minder, maar te ruim schatten is hier veilig.
const defaultMemoryMB = 2048

// memoryBytes leest het geheugen van de VM: "2048" of "current=2048".
func memoryBytes(cfg map[string]string) int64 {
	v, ok := cfg["memory"]
	if !ok {
		return defaultMemoryMB << 20
	}
	if cur, found := option(v, "current", "current"); found {
		v = cur
	}
	mb, err := strconv.ParseInt(v, 10, 64)
	if err != nil || mb <= 0 {
		return defaultMemoryMB << 20
	}
	return mb << 20
}

// hasAgent is true als de config de QEMU guest agent aanzet: "1" of
// "enabled=1,...".
func hasAgent(cfg map[string]string) bool {
	v, ok := cfg["agent"]
	if !ok {
		return false
	}
	on, _ := option(v, "enabled", "enabled")
	return on == "1"
}

// sandboxTag is de tag op elke sandbox, naast de pool.
const sandboxTag = "cf-sandbox"

// sandboxSerial is het SMBIOS-serienummer van een sandbox. Vanaf mijlpaal 16
// leest cf-agent het en verbindt hij dan niet met NATS.
const sandboxSerial = "cf-sandbox"

// isolation is de wijziging die een teruggezette VM afsluit.
func isolation(cfg map[string]string, description string) map[string]string {
	out := map[string]string{"onboot": "0", "protection": "0", "description": description}
	for _, k := range slices.Sorted(maps.Keys(cfg)) {
		v := cfg[k]
		switch {
		case netKey(k):
			out[k] = setOption(v, "link_down", "1")
		case diskKey(k) && strings.Contains(v, "media=cdrom") && !strings.Contains(v, "cloudinit"):
			// Een ISO van de bron staat misschien niet op deze host; zonder
			// ISO start de VM gewoon van zijn schijf.
			if vol, _, _ := strings.Cut(v, ","); vol != "none" {
				out[k] = "none,media=cdrom"
			}
		}
	}
	out["tags"] = withTag(cfg["tags"], sandboxTag)
	out["smbios1"] = withSerial(cfg["smbios1"], sandboxSerial)
	return out
}

// withTag voegt een tag toe aan een lijst zoals "web;prod".
func withTag(tags, tag string) string {
	var out []string
	for _, t := range strings.FieldsFunc(tags, func(r rune) bool { return r == ';' || r == ',' || r == ' ' }) {
		if t != tag {
			out = append(out, t)
		}
	}
	return strings.Join(append(out, tag), ";")
}

func hasTag(tags, tag string) bool {
	return slices.Contains(strings.FieldsFunc(tags, func(r rune) bool { return r == ';' || r == ',' || r == ' ' }), tag)
}

// withSerial zet het serienummer in smbios1 en laat uuid en de rest staan.
// Met base64=1 zijn alle tekstvelden in base64.
func withSerial(smbios, serial string) string {
	if smbios == "" {
		return "serial=" + serial
	}
	if b64, _ := option(smbios, "base64", ""); b64 == "1" {
		serial = base64.StdEncoding.EncodeToString([]byte(serial))
	}
	return setOption(smbios, "serial", serial)
}

// serialOf leest het serienummer uit smbios1.
func serialOf(smbios string) string {
	v, _ := option(smbios, "serial", "")
	if b64, _ := option(smbios, "base64", ""); b64 == "1" {
		if d, err := base64.StdEncoding.DecodeString(v); err == nil {
			return string(d)
		}
	}
	return v
}

// isolationProblems controleert een teruggelezen config. Leeg betekent dat
// de VM gestart mag worden.
func isolationProblems(cfg map[string]string) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(cfg)) {
		if !netKey(k) {
			continue
		}
		if v, _ := option(cfg[k], "link_down", ""); v != "1" {
			out = append(out, k+" heeft geen link_down=1")
		}
	}
	if bad := notIsolatable(cfg); len(bad) > 0 {
		out = append(out, "sleutels buiten de allowlist: "+strings.Join(bad, ", "))
	}
	if cfg["onboot"] == "1" {
		out = append(out, "onboot staat aan")
	}
	if cfg["protection"] == "1" {
		out = append(out, "protection staat aan")
	}
	if cfg["template"] == "1" {
		out = append(out, "de VM is een template")
	}
	if !hasTag(cfg["tags"], sandboxTag) {
		out = append(out, "de tag "+sandboxTag+" ontbreekt")
	}
	if serialOf(cfg["smbios1"]) != sandboxSerial {
		out = append(out, "het SMBIOS-serienummer is niet "+sandboxSerial)
	}
	return out
}

// configStrings maakt van de JSON-config van Proxmox tekst, zoals in het
// configbestand: getallen als 2048, niet als 2048.0.
func configStrings(cfg map[string]any) map[string]string {
	out := make(map[string]string, len(cfg))
	for k, v := range cfg {
		switch x := v.(type) {
		case string:
			out[k] = x
		case float64:
			out[k] = strconv.FormatFloat(x, 'f', -1, 64)
		case nil:
		default:
			out[k] = fmt.Sprint(x)
		}
	}
	return out
}

// sandboxDescription staat in de notities van de sandbox in Proxmox.
func sandboxDescription(hostname string, sourceVMID int, volid, runID string) string {
	return fmt.Sprintf("Tijdelijke sandbox van ClusterForge: back-upcontrole van %s (VM %d), back-up %s. "+
		"Netwerk staat uit (link_down). ClusterForge verwijdert deze VM zelf na de controle (run %s).",
		hostname, sourceVMID, volid, runID)
}

// inSandboxPool is true als Proxmox de VM nu in de sandbox-pool toont.
func inSandboxPool(r proxmox.Resource) bool {
	return r.Type == "qemu" && r.Pool == proxmox.SandboxPool
}
