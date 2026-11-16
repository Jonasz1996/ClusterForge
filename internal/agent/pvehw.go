package agent

// Fase 26: hardware van een Proxmox-host voor passthrough en bewaking:
// PCI met IOMMU-groepen en drivers, USB, de vfio-instellingen en of de
// host moet herstarten, temperaturen en ECC.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	vfioModprobe = "/etc/modprobe.d/clusterforge-vfio.conf"
	vfioModules  = "/etc/modules-load.d/clusterforge-vfio.conf"
	// vfioPending staat in /run (tmpfs): weg na een herstart.
	vfioPending = "/run/clusterforge-passthrough-pending"
)

var vfioModuleNames = []string{"vfio", "vfio_iommu_type1", "vfio_pci"}

func readTrim(p string) string {
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func hexID(p string) string { return strings.TrimPrefix(readTrim(p), "0x") }

// bootMode zegt hoe de host start: systemd-boot (proxmox-boot-tool met
// /etc/kernel/cmdline) of grub.
func (h pveHost) bootMode() string {
	if _, err := os.Stat(h.path("/etc/kernel/cmdline")); err == nil {
		return "systemd-boot"
	}
	return "grub"
}

var grubLineRe = regexp.MustCompile(`(?m)^GRUB_CMDLINE_LINUX_DEFAULT=(["']?)(.*?)(["']?)\s*$`)

// configuredCmdline is de kernelregel die na de volgende start geldt.
func (h pveHost) configuredCmdline() string {
	if h.bootMode() == "systemd-boot" {
		return readTrim(h.path("/etc/kernel/cmdline"))
	}
	data, _ := os.ReadFile(h.path("/etc/default/grub"))
	if m := grubLineRe.FindSubmatch(data); m != nil {
		return strings.TrimSpace(string(m[2]))
	}
	return ""
}

// vfioState leest de vfio-ids en de blacklist uit /etc/modprobe.d.
func (h pveHost) vfioState() (ids, blacklist []string, managed bool) {
	files, _ := filepath.Glob(h.path("/etc/modprobe.d/*.conf"))
	for _, f := range files {
		if strings.HasSuffix(f, "clusterforge-vfio.conf") {
			managed = true
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for line := range strings.Lines(string(data)) {
			fields := strings.Fields(line)
			switch {
			case len(fields) >= 3 && fields[0] == "options" && (fields[1] == "vfio-pci" || fields[1] == "vfio_pci"):
				for _, opt := range fields[2:] {
					if v, ok := strings.CutPrefix(opt, "ids="); ok {
						for _, id := range strings.Split(v, ",") {
							if id = strings.ToLower(strings.TrimSpace(id)); protocol.ValidVFIOID(id) && !slices.Contains(ids, id) {
								ids = append(ids, id)
							}
						}
					}
				}
			case len(fields) == 2 && fields[0] == "blacklist":
				if !slices.Contains(blacklist, fields[1]) {
					blacklist = append(blacklist, fields[1])
				}
			}
		}
	}
	sort.Strings(ids)
	sort.Strings(blacklist)
	return ids, blacklist, managed
}

// lspciNames leest de namen uit lspci -Dnnmm, per adres.
func (h pveHost) lspciNames(ctx context.Context) map[string][3]string {
	out, err := h.exec(ctx, "lspci", "-Dnnmm")
	if err != nil {
		return nil
	}
	return parseLspci(out)
}

var lspciField = regexp.MustCompile(`"([^"]*)"`)
var lspciID = regexp.MustCompile(`\s*\[[0-9a-fA-F]{4}\]$`)

// parseLspci geeft per adres de klasse, de fabrikant en het apparaat.
func parseLspci(out []byte) map[string][3]string {
	names := map[string][3]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		addr, _, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		f := lspciField.FindAllStringSubmatch(line, -1)
		if len(f) < 3 {
			continue
		}
		clean := func(s string) string { return strings.TrimSpace(lspciID.ReplaceAllString(s, "")) }
		names[addr] = [3]string{clean(f[0][1]), clean(f[1][1]), clean(f[2][1])}
	}
	return names
}

// hostUse zegt welke PCI-apparaten de host zelf gebruikt: netwerkkaarten
// en de controllers van schijven.
func (h pveHost) hostUse() map[string]string {
	use := map[string]string{}
	add := func(link, what string) {
		target, err := filepath.EvalSymlinks(link)
		if err != nil {
			return
		}
		for _, part := range strings.Split(target, "/") {
			if len(part) == 12 && part[4] == ':' && part[7] == ':' && part[10] == '.' {
				if use[part] == "" {
					use[part] = what
				} else if !strings.Contains(use[part], what) {
					use[part] += ", " + what
				}
			}
		}
	}
	nets, _ := os.ReadDir(h.path("/sys/class/net"))
	for _, n := range nets {
		add(h.path(filepath.Join("/sys/class/net", n.Name(), "device")), "netwerkkaart "+n.Name())
	}
	blocks, _ := os.ReadDir(h.path("/sys/block"))
	for _, b := range blocks {
		if strings.HasPrefix(b.Name(), "loop") || strings.HasPrefix(b.Name(), "zd") || strings.HasPrefix(b.Name(), "dm-") {
			continue
		}
		add(h.path(filepath.Join("/sys/block", b.Name())), "schijf "+b.Name())
	}
	return use
}

// hardware scant de host.
func (h pveHost) hardware(ctx context.Context) (protocol.PVEHardware, error) {
	if !h.isHost() {
		return protocol.PVEHardware{}, errNotPVE
	}
	hw := protocol.PVEHardware{
		Cmdline: readTrim(h.path("/proc/cmdline")), Boot: h.bootMode(),
		Modules: []string{}, PCI: []protocol.PVEPCI{}, USB: []protocol.PVEUSB{}, Temps: []protocol.PVETemp{}, EDAC: []protocol.PVEEDAC{},
	}
	if data, err := os.ReadFile(h.path("/proc/cpuinfo")); err == nil {
		for line := range strings.Lines(string(data)) {
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			switch strings.TrimSpace(k) {
			case "vendor_id":
				hw.CPUVendor = strings.TrimSpace(v)
			case "model name":
				hw.CPUModel = strings.TrimSpace(v)
			}
			if hw.CPUVendor != "" && hw.CPUModel != "" {
				break
			}
		}
	}
	groups, _ := os.ReadDir(h.path("/sys/kernel/iommu_groups"))
	hw.IOMMUGroups = len(groups)
	hw.IOMMU = hw.IOMMUGroups > 0
	if data, err := os.ReadFile(h.path("/proc/modules")); err == nil {
		for line := range strings.Lines(string(data)) {
			if f := strings.Fields(line); len(f) > 0 && slices.Contains(vfioModuleNames, f[0]) {
				hw.Modules = append(hw.Modules, f[0])
			}
		}
		sort.Strings(hw.Modules)
	}
	ids, bl, managed := h.vfioState()
	hw.Configured = protocol.PVEPassthroughState{Cmdline: h.configuredCmdline(), VFIOIDs: ids, Blacklist: bl, Managed: managed}
	if hw.Configured.VFIOIDs == nil {
		hw.Configured.VFIOIDs = []string{}
	}
	if hw.Configured.Blacklist == nil {
		hw.Configured.Blacklist = []string{}
	}

	names := h.lspciNames(ctx)
	use := h.hostUse()
	devs, _ := os.ReadDir(h.path("/sys/bus/pci/devices"))
	groupSize := map[int]int{}
	for _, d := range devs {
		dir := h.path(filepath.Join("/sys/bus/pci/devices", d.Name()))
		p := protocol.PVEPCI{
			Address: d.Name(), Vendor: hexID(dir + "/vendor"), Device: hexID(dir + "/device"),
			SubVendor: hexID(dir + "/subsystem_vendor"), SubDevice: hexID(dir + "/subsystem_device"), IOMMUGroup: -1,
		}
		if c := hexID(dir + "/class"); len(c) >= 4 {
			p.Class = c[:4]
		}
		if t, err := os.Readlink(dir + "/driver"); err == nil {
			p.Driver = filepath.Base(t)
		}
		if t, err := os.Readlink(dir + "/iommu_group"); err == nil {
			if n, err := strconv.Atoi(filepath.Base(t)); err == nil {
				p.IOMMUGroup = n
			}
		}
		if _, err := os.Stat(dir + "/mdev_supported_types"); err == nil {
			p.Mdev = true
		} else if n, _ := strconv.Atoi(readTrim(dir + "/sriov_totalvfs")); n > 0 {
			p.Mdev = true
		}
		if n, ok := names[p.Address]; ok {
			p.ClassName, p.VendorName, p.DeviceName = n[0], n[1], n[2]
		}
		p.HostUse = use[p.Address]
		if p.IOMMUGroup >= 0 && p.Class != "0604" && p.Class != "0600" {
			groupSize[p.IOMMUGroup]++
		}
		hw.PCI = append(hw.PCI, p)
	}
	for i := range hw.PCI {
		if g := hw.PCI[i].IOMMUGroup; g >= 0 {
			hw.PCI[i].GroupSize = groupSize[g]
		}
	}
	sort.Slice(hw.PCI, func(i, j int) bool { return hw.PCI[i].Address < hw.PCI[j].Address })

	usbs, _ := os.ReadDir(h.path("/sys/bus/usb/devices"))
	for _, u := range usbs {
		dir := h.path(filepath.Join("/sys/bus/usb/devices", u.Name()))
		vendor := readTrim(dir + "/idVendor")
		if vendor == "" || vendor == "1d6b" || strings.Contains(u.Name(), ":") {
			continue // geen apparaat, of een root hub
		}
		bus, _ := strconv.Atoi(readTrim(dir + "/busnum"))
		port := u.Name()
		if _, rest, ok := strings.Cut(port, "-"); ok {
			port = rest
		}
		hw.USB = append(hw.USB, protocol.PVEUSB{
			Bus: bus, Port: port, Vendor: vendor, Product: readTrim(dir + "/idProduct"),
			Manufacturer: readTrim(dir + "/manufacturer"), Name: readTrim(dir + "/product"),
			Class: readTrim(dir + "/bDeviceClass"), Speed: readTrim(dir + "/speed"),
		})
	}
	sort.Slice(hw.USB, func(i, j int) bool {
		if hw.USB[i].Bus != hw.USB[j].Bus {
			return hw.USB[i].Bus < hw.USB[j].Bus
		}
		return hw.USB[i].Port < hw.USB[j].Port
	})

	hw.Temps = h.temps()
	mcs, _ := filepath.Glob(h.path("/sys/devices/system/edac/mc/mc*"))
	for _, mc := range mcs {
		ce, err1 := strconv.ParseInt(readTrim(mc+"/ce_count"), 10, 64)
		ue, err2 := strconv.ParseInt(readTrim(mc+"/ue_count"), 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		hw.EDAC = append(hw.EDAC, protocol.PVEEDAC{Controller: filepath.Base(mc), Corrected: ce, Uncorrected: ue})
	}

	hw.RebootReasons = h.rebootReasons(hw)
	hw.RebootRequired = len(hw.RebootReasons) > 0
	return hw, nil
}

func (h pveHost) temps() []protocol.PVETemp {
	out := []protocol.PVETemp{}
	chips, _ := filepath.Glob(h.path("/sys/class/hwmon/hwmon*"))
	sort.Strings(chips)
	for _, chip := range chips {
		name := readTrim(chip + "/name")
		inputs, _ := filepath.Glob(chip + "/temp*_input")
		sort.Strings(inputs)
		for _, in := range inputs {
			base := strings.TrimSuffix(in, "_input")
			v, err := strconv.ParseFloat(readTrim(in), 64)
			if err != nil {
				continue
			}
			label := readTrim(base + "_label")
			if label == "" {
				label = filepath.Base(base)
			}
			t := protocol.PVETemp{Chip: name, Label: label, C: v / 1000}
			if hi, err := strconv.ParseFloat(readTrim(base+"_max"), 64); err == nil && hi > 0 {
				t.High = hi / 1000
			}
			if cr, err := strconv.ParseFloat(readTrim(base+"_crit"), 64); err == nil && cr > 0 {
				t.Crit = cr / 1000
			}
			out = append(out, t)
		}
	}
	return out
}

// rebootReasons zegt waarom de host moet herstarten om de
// passthrough-instellingen te laten gelden.
func (h pveHost) rebootReasons(hw protocol.PVEHardware) []string {
	var why []string
	now := strings.Fields(hw.Cmdline)
	for _, want := range iommuParams(hw.CPUVendor) {
		if slices.Contains(strings.Fields(hw.Configured.Cmdline), want) && !slices.Contains(now, want) {
			why = append(why, "kernelregel: "+want+" geldt pas na een herstart")
		}
	}
	for _, id := range hw.Configured.VFIOIDs {
		for _, p := range hw.PCI {
			if p.Vendor+":"+p.Device == id && p.Driver != "vfio-pci" && p.HostUse == "" {
				why = append(why, fmt.Sprintf("%s (%s) gebruikt nog %s in plaats van vfio-pci", p.Address, id, orNone(p.Driver)))
			}
		}
	}
	if _, err := os.Stat(h.path(vfioPending)); err == nil && len(why) == 0 {
		why = append(why, "de passthrough-instellingen zijn gewijzigd sinds de laatste start")
	}
	if _, err := os.Stat(h.path("/run/reboot-required")); err == nil {
		why = append(why, "een update vraagt een herstart (/run/reboot-required)")
	}
	return why
}

func orNone(s string) string {
	if s == "" {
		return "geen driver"
	}
	return s
}

// iommuParams zijn de kernelopties die ClusterForge voor IOMMU zet: bij
// AMD staat de IOMMU standaard aan.
func iommuParams(cpuVendor string) []string {
	if cpuVendor == "GenuineIntel" {
		return []string{"intel_iommu=on", "iommu=pt"}
	}
	return []string{"iommu=pt"}
}

// editCmdline zet of haalt de IOMMU-opties op een kernelregel.
func editCmdline(line string, params []string, on bool) string {
	var out []string
	managed := []string{"intel_iommu=on", "iommu=pt"}
	for _, f := range strings.Fields(line) {
		if !slices.Contains(managed, f) {
			out = append(out, f)
		}
	}
	if on {
		out = append(out, params...)
	}
	return strings.Join(out, " ")
}

// pvePassthrough zet de host klaar voor passthrough.
func (r *RunExecutor) pvePassthrough(ctx context.Context, dir string, p *protocol.PVEPassthroughSpec, out io.Writer, st *protocol.RunStatus) (int, error) {
	h := r.host()
	vendor := ""
	if data, err := os.ReadFile(r.path("/proc/cpuinfo")); err == nil {
		for line := range strings.Lines(string(data)) {
			if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "vendor_id" {
				vendor = strings.TrimSpace(v)
				break
			}
		}
	}
	changed := false
	backup := func(file string) {
		if data, err := os.ReadFile(file); err == nil {
			_ = os.WriteFile(filepath.Join(dir, filepath.Base(file)+".voor"), data, 0o600)
		}
	}
	write := func(file, content string) error {
		old, _ := os.ReadFile(file)
		if string(old) == content {
			return nil
		}
		backup(file)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return err
		}
		if err := writeFileAtomic(file, []byte(content), 0o644); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "%s geschreven\n", strings.TrimPrefix(file, r.Root))
		changed = true
		return nil
	}
	remove := func(file string) {
		if _, err := os.Stat(file); err == nil {
			backup(file)
			_ = os.Remove(file)
			_, _ = fmt.Fprintf(out, "%s verwijderd\n", strings.TrimPrefix(file, r.Root))
			changed = true
		}
	}

	// 1. Kernelregel.
	params := iommuParams(vendor)
	if h.bootMode() == "systemd-boot" {
		file := r.path("/etc/kernel/cmdline")
		cur := readTrim(file)
		if next := editCmdline(cur, params, p.IOMMU); next != cur {
			if err := write(file, next+"\n"); err != nil {
				return -1, err
			}
		}
	} else {
		file := r.path("/etc/default/grub")
		data, err := os.ReadFile(file)
		if err != nil {
			return -1, fmt.Errorf("/etc/default/grub lezen: %w", err)
		}
		m := grubLineRe.FindSubmatchIndex(data)
		if m == nil {
			return -1, fmt.Errorf("geen GRUB_CMDLINE_LINUX_DEFAULT in /etc/default/grub")
		}
		cur := string(data[m[4]:m[5]])
		if next := editCmdline(cur, params, p.IOMMU); next != cur {
			newData := string(data[:m[0]]) + `GRUB_CMDLINE_LINUX_DEFAULT="` + next + `"` + string(data[m[1]:])
			if err := write(file, newData); err != nil {
				return -1, err
			}
		}
	}

	// 2. vfio-ids, blacklist en modules.
	if len(p.VFIOIDs) > 0 || len(p.Blacklist) > 0 {
		var b strings.Builder
		b.WriteString("# Beheerd door ClusterForge (Proxmox → Hardware). Niet met de hand aanpassen.\n")
		if len(p.VFIOIDs) > 0 {
			ids := slices.Clone(p.VFIOIDs)
			sort.Strings(ids)
			fmt.Fprintf(&b, "options vfio-pci ids=%s disable_vga=1\n", strings.Join(ids, ","))
		}
		for _, d := range p.Blacklist {
			fmt.Fprintf(&b, "blacklist %s\n", d)
			// vfio-pci eerst, als de driver toch geladen wordt.
			fmt.Fprintf(&b, "softdep %s pre: vfio-pci\n", d)
		}
		if err := write(r.path(vfioModprobe), b.String()); err != nil {
			return -1, err
		}
	} else {
		remove(r.path(vfioModprobe))
	}
	if p.IOMMU || len(p.VFIOIDs) > 0 {
		if err := write(r.path(vfioModules), "# Beheerd door ClusterForge\n"+strings.Join(vfioModuleNames, "\n")+"\n"); err != nil {
			return -1, err
		}
	} else {
		remove(r.path(vfioModules))
	}

	if !changed {
		_, _ = fmt.Fprintln(out, "Alles stond al zo; niets veranderd.")
		return 0, nil
	}

	// 3. Initramfs en bootloader.
	if code, err := r.step(ctx, out, nil, "update-initramfs", "-u", "-k", "all"); err != nil {
		return code, err
	}
	if _, err := os.Stat(r.path("/etc/kernel/proxmox-boot-uuids")); err == nil {
		if code, err := r.step(ctx, out, nil, "proxmox-boot-tool", "refresh"); err != nil {
			return code, err
		}
	} else if code, err := r.step(ctx, out, nil, "update-grub"); err != nil {
		return code, err
	}
	_ = os.WriteFile(r.path(vfioPending), []byte("1\n"), 0o644)
	st.PVE = &protocol.PVERunResult{RebootRequired: true}
	_, _ = fmt.Fprintln(out, "Klaar. De host moet herstarten voor dit geldt.")
	return 0, nil
}
