package protocol

import (
	"fmt"
	"regexp"
	"strings"
)

// PVEAPISince is de eerste protocolversie die de API van Proxmox via pvesh
// doorgeeft (fase 26): lezen met het verzoek PVEOpGet, veranderen met de
// run-actie PVEAPI, de hardwarescan PVEOpHardware, en de acties
// PVEPassthrough, PVEVolumeFree, PVEZpoolScrub en PVEMaintenance.
//
// De agent controleert alleen de vorm (methode, pad, sleutels); welke
// paden en velden de GUI mag gebruiken en wat een bevestiging vraagt,
// beslist de server (internal/pveapi). Zo vraagt een nieuwe
// Proxmox-functie in ClusterForge meestal geen agentupdate.
const PVEAPISince = 35

// PVEQuietSince is de eerste protocolversie die PVEAPICall.Quiet kent
// (fase 26 M6): een aanroep waarvan het antwoord een geheim is, zoals een
// nieuw API-token.
const PVEQuietSince = 36

// Leesverzoeken vanaf PVEAPISince.
const (
	// PVEOpGet is pvesh get <Path> met Params.
	PVEOpGet = "get"
	// PVEOpHardware is de hardwarescan van de host: PCI met IOMMU-groepen
	// en drivers, USB, IOMMU/vfio, temperaturen, ECC.
	PVEOpHardware = "hardware"
)

// Acties van een run pve vanaf PVEAPISince.
const (
	// PVEAPI is pvesh create|set|delete op een pad, met wachten op de
	// taak (UPID) als Proxmox er een start.
	PVEAPI = "api"
	// PVEPassthrough zet IOMMU en vfio-pci op de host (kernelregel,
	// modules, vfio-ids) en bouwt de initramfs en de bootloader opnieuw.
	PVEPassthrough = "passthrough"
	// PVEVolumeFree verwijdert een volume met pvesm free (een back-up,
	// ongebruikte schijf of ISO).
	PVEVolumeFree = "volume_free"
	// PVEZpoolScrub start of stopt een scrub van een ZFS-pool.
	PVEZpoolScrub = "zpool_scrub"
	// PVEMaintenance zet de host in of uit onderhoud bij HA
	// (ha-manager crm-command node-maintenance).
	PVEMaintenance = "maintenance"
)

func init() {
	PVEActions = append(PVEActions, PVEAPI, PVEPassthrough, PVEVolumeFree, PVEZpoolScrub, PVEMaintenance)
}

// HostActions zijn de acties die niet over één gast gaan.
func isHostAction(a string) bool {
	switch a {
	case PVETemplate, PVEAPI, PVEPassthrough, PVEVolumeFree, PVEZpoolScrub, PVEMaintenance, PBSAPI:
		return true
	}
	return false
}

// PVEAPICall is één aanroep van de Proxmox-API via pvesh.
type PVEAPICall struct {
	// Method is create (POST), set (PUT) of delete (DELETE).
	Method string `json:"method"`
	// Path zoals /cluster/ha/resources of /nodes/pve1/qemu/101/config.
	Path   string            `json:"path"`
	Params map[string]string `json:"params,omitempty"`
	// Secret zijn de sleutels van Params die nooit in de uitvoer komen,
	// zoals password.
	Secret []string `json:"secret,omitempty"`
	// Quiet: het antwoord van Proxmox komt niet in de uitvoer, alleen in
	// RunStatus.PVE.Data (vanaf PVEQuietSince). Voor een antwoord met een
	// geheim, zoals de waarde van een nieuw API-token.
	Quiet bool `json:"quiet,omitempty"`
}

// PVEPassthroughSpec is hoe de host voor passthrough staat. De agent
// schrijft zijn eigen bestanden (clusterforge-vfio.conf) en past de
// kernelregel aan; wat er al stond, blijft.
type PVEPassthroughSpec struct {
	// IOMMU zet intel_iommu=on (Intel) en iommu=pt op de kernelregel.
	IOMMU bool `json:"iommu"`
	// VFIOIDs zijn vendor:device-paren (10de:1b80) die vfio-pci bij het
	// opstarten pakt.
	VFIOIDs []string `json:"vfio_ids,omitempty"`
	// Blacklist zijn drivers die niet meer laden (nouveau, nvidia, radeon,
	// amdgpu, i915, snd_hda_intel, …).
	Blacklist []string `json:"blacklist,omitempty"`
}

// PVEScrub is de spec van zpool_scrub.
type PVEScrub struct {
	Pool string `json:"pool"`
	Stop bool   `json:"stop,omitempty"`
}

var (
	pveAPIPath   = regexp.MustCompile(`^(/[A-Za-z0-9_.:@%+=,~-]+)+$`)
	pveAPIKey    = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	pveVFIOID    = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{4}$`)
	pveDriver    = regexp.MustCompile(`^[a-z0-9_]{2,40}$`)
	pveAnyVolid  = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{0,99}:[A-Za-z0-9][A-Za-z0-9._+/:=-]{0,300}$`)
	pveZpoolName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]{0,99}$`)
)

// ValidVFIOID is true voor vendor:device zoals 10de:1b80.
func ValidVFIOID(s string) bool { return pveVFIOID.MatchString(s) }

// ValidPVEAPIPath is true voor een pad van de Proxmox-API.
func ValidPVEAPIPath(p string) bool {
	if !pveAPIPath.MatchString(p) || len(p) > 512 {
		return false
	}
	for _, seg := range strings.Split(p[1:], "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// Validate controleert de vorm van een aanroep.
func (c *PVEAPICall) Validate() error {
	switch c.Method {
	case "create", "set", "delete":
	default:
		return fmt.Errorf("methode %q is geen create, set of delete", c.Method)
	}
	if !ValidPVEAPIPath(c.Path) {
		return fmt.Errorf("ongeldig API-pad %q", c.Path)
	}
	if len(c.Params) > 200 {
		return fmt.Errorf("te veel velden")
	}
	for k, v := range c.Params {
		if !pveAPIKey.MatchString(k) {
			return fmt.Errorf("ongeldig veld %q", k)
		}
		if len(v) > 65536 || strings.ContainsRune(v, 0) {
			return fmt.Errorf("ongeldige waarde voor %s", k)
		}
	}
	for _, k := range c.Secret {
		if _, ok := c.Params[k]; !ok {
			return fmt.Errorf("geheim veld %q staat niet in de velden", k)
		}
	}
	return nil
}

// ValidateGet controleert een leesverzoek.
func (r PVERequest) ValidateGet() error {
	if !ValidPVEAPIPath(r.Path) {
		return fmt.Errorf("ongeldig API-pad %q", r.Path)
	}
	for k, v := range r.Params {
		if !pveAPIKey.MatchString(k) || len(v) > 4096 || strings.ContainsAny(v, "\x00\n") {
			return fmt.Errorf("ongeldig veld %q", k)
		}
	}
	return nil
}

func (p *PVEPassthroughSpec) validate() error {
	if len(p.VFIOIDs) > 32 || len(p.Blacklist) > 16 {
		return fmt.Errorf("te veel vfio-ids of drivers")
	}
	for _, id := range p.VFIOIDs {
		if !pveVFIOID.MatchString(id) {
			return fmt.Errorf("ongeldig vendor:device %q (zoals 10de:1b80)", id)
		}
	}
	for _, d := range p.Blacklist {
		if !pveDriver.MatchString(d) {
			return fmt.Errorf("ongeldige driver %q", d)
		}
	}
	return nil
}

// validateHost controleert de acties van fase 26.
func (p PVERun) validateHost() error {
	switch p.Action {
	case PVEAPI:
		if p.API == nil {
			return fmt.Errorf("api zonder aanroep")
		}
		return p.API.Validate()
	case PVEPassthrough:
		if p.Passthrough == nil {
			return fmt.Errorf("passthrough zonder instellingen")
		}
		return p.Passthrough.validate()
	case PVEVolumeFree:
		if !pveAnyVolid.MatchString(p.Volume) {
			return fmt.Errorf("ongeldig volume %q", p.Volume)
		}
	case PVEZpoolScrub:
		if p.Scrub == nil || !pveZpoolName.MatchString(p.Scrub.Pool) {
			return fmt.Errorf("ongeldige ZFS-pool")
		}
	case PVEMaintenance:
	case PBSAPI:
		if p.API == nil {
			return fmt.Errorf("pbs_api zonder aanroep")
		}
		return p.API.ValidatePBS()
	}
	return nil
}

// PVEHardware is wat de hardwarescan van een host geeft.
type PVEHardware struct {
	// CPUVendor is GenuineIntel of AuthenticAMD.
	CPUVendor string `json:"cpu_vendor"`
	CPUModel  string `json:"cpu_model,omitempty"`
	// Cmdline is /proc/cmdline: wat nu geldt.
	Cmdline string `json:"cmdline"`
	// IOMMU: er zijn IOMMU-groepen (/sys/kernel/iommu_groups).
	IOMMU       bool `json:"iommu"`
	IOMMUGroups int  `json:"iommu_groups"`
	// Modules zijn de geladen vfio-modules.
	Modules []string `json:"modules"`
	// Boot is grub of systemd-boot (proxmox-boot-tool).
	Boot string `json:"boot"`
	// Configured is wat ClusterForge op de host zette (of wat er al stond):
	// de kernelregel na de volgende start, de vfio-ids en de blacklist.
	Configured PVEPassthroughState `json:"configured"`
	// RebootRequired: de configuratie geldt pas na een herstart (andere
	// kernelregel, vfio-ids zonder vfio-pci, of /run/reboot-required).
	RebootRequired bool      `json:"reboot_required"`
	RebootReasons  []string  `json:"reboot_reasons,omitempty"`
	PCI            []PVEPCI  `json:"pci"`
	USB            []PVEUSB  `json:"usb"`
	Temps          []PVETemp `json:"temps"`
	// EDAC is de ECC-telling per geheugencontroller; leeg zonder EDAC.
	EDAC []PVEEDAC `json:"edac"`
}

type PVEPassthroughState struct {
	Cmdline   string   `json:"cmdline"`
	VFIOIDs   []string `json:"vfio_ids"`
	Blacklist []string `json:"blacklist"`
	// Managed: de bestanden van ClusterForge staan er.
	Managed bool `json:"managed"`
}

// PVEPCI is één PCI-functie.
type PVEPCI struct {
	// Address zoals 0000:01:00.0.
	Address   string `json:"address"`
	Vendor    string `json:"vendor"`
	Device    string `json:"device"`
	SubVendor string `json:"subsystem_vendor,omitempty"`
	SubDevice string `json:"subsystem_device,omitempty"`
	// Class zoals 0300 (VGA).
	Class      string `json:"class"`
	VendorName string `json:"vendor_name,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
	ClassName  string `json:"class_name,omitempty"`
	Driver     string `json:"driver,omitempty"`
	// IOMMUGroup is -1 zonder IOMMU.
	IOMMUGroup int `json:"iommu_group"`
	// GroupSize is het aantal functies in dezelfde IOMMU-groep (zonder
	// bridges); groter dan de functies van dit apparaat betekent dat het
	// niet los door te geven is.
	GroupSize int `json:"group_size"`
	// Mdev: het apparaat kan gedeeld worden (vGPU / SR-IOV).
	Mdev bool `json:"mdev,omitempty"`
	// Host: dit apparaat gebruikt de host zelf (zijn netwerkkaart of de
	// schijfcontroller van een gemounte schijf).
	HostUse string `json:"host_use,omitempty"`
}

// PVEUSB is één USB-apparaat.
type PVEUSB struct {
	Bus          int    `json:"bus"`
	Port         string `json:"port"`
	Vendor       string `json:"vendor"`
	Product      string `json:"product"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Name         string `json:"name,omitempty"`
	Class        string `json:"class,omitempty"`
	Speed        string `json:"speed,omitempty"`
}

// PVETemp is een temperatuur uit /sys/class/hwmon.
type PVETemp struct {
	Chip  string  `json:"chip"`
	Label string  `json:"label"`
	C     float64 `json:"c"`
	// High en Crit zijn de grenzen van de chip; 0 als onbekend.
	High float64 `json:"high,omitempty"`
	Crit float64 `json:"crit,omitempty"`
}

// PVEEDAC is de ECC-telling van één geheugencontroller.
type PVEEDAC struct {
	Controller string `json:"controller"`
	// Corrected en Uncorrected sinds de start van de host.
	Corrected   int64 `json:"corrected"`
	Uncorrected int64 `json:"uncorrected"`
}
