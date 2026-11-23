package protocol

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"
)

// PVESince is de eerste protocolversie die een Proxmox-host beheert: de
// facts proxmox, het onderwerp pve en de run pve (fase 17).
const PVESince = 32

// SubjectPVE draagt de leesverzoeken van een Proxmox-host:
// cf.node.<id>.pve. Alles wat iets verandert, is een run van de soort
// RunPVE.
const SubjectPVE = "pve"

const (
	TypePVERequest = "pve_request"
	TypePVEResult  = "pve_result"
)

// Soorten pve-verzoek.
const (
	// PVEOpInventory geeft de gasten, storages, bridges, containertemplates
	// en de publieke SSH-sleutels van root op de host.
	PVEOpInventory = "inventory"
	// PVEOpAvailable geeft de containertemplates die pveam kan downloaden.
	PVEOpAvailable = "available"
)

// PVERequestTimeout is hoe lang een pve-verzoek mag duren; pvesh en pveam
// zijn traag (Perl).
const PVERequestTimeout = 90 * time.Second

// RunPVE is de soort run die iets op een Proxmox-host verandert, met
// RunSpec.PVE.
const RunPVE = "pve"

// PVEFacts staat in de facts van een agent op een Proxmox-host.
type PVEFacts struct {
	// Version is de versie van pve-manager, zoals 9.0.10.
	Version string `json:"version"`
	// Node is de naam van de host in Proxmox.
	Node string `json:"node"`
	// Cluster is de naam van het Proxmox-cluster; leeg op een losse host.
	Cluster string `json:"cluster,omitempty"`
}

type PVERequest struct {
	ID       string    `json:"id"`
	Op       string    `json:"op"`
	Deadline time.Time `json:"deadline"`
	// Path en Params bij PVEOpGet (vanaf PVEAPISince).
	Path   string            `json:"path,omitempty"`
	Params map[string]string `json:"params,omitempty"`
	// Args bij TrueNASOpGet: een JSON-lijst met de argumenten van de methode
	// in Path.
	Args json.RawMessage `json:"args,omitempty"`
}

type PVEResponse struct {
	Error     string         `json:"error,omitempty"`
	Inventory *PVEInventory  `json:"inventory,omitempty"`
	Available []PVEAppliance `json:"available,omitempty"`
	// Data is het JSON-antwoord van pvesh get (PVEOpGet).
	Data json.RawMessage `json:"data,omitempty"`
	// Hardware is de hardwarescan (PVEOpHardware).
	Hardware *PVEHardware `json:"hardware,omitempty"`
}

// PVEInventory is wat de agent op een Proxmox-host ziet.
type PVEInventory struct {
	Node    string `json:"node"`
	Version string `json:"version"`
	Cluster string `json:"cluster,omitempty"`
	// Arch is de architectuur van de host (amd64, arm64); een CT heeft
	// dezelfde.
	Arch      string          `json:"arch"`
	Guests    []PVEGuest      `json:"guests"`
	Storages  []PVEStorage    `json:"storages"`
	Bridges   []string        `json:"bridges"`
	Templates []PVEOSTemplate `json:"templates"`
	// SSHKeys zijn de publieke sleutels van root op de host
	// (/root/.ssh/id_*.pub); een nieuwe CT krijgt ze.
	SSHKeys []string `json:"ssh_keys"`
	// UsedIDs zijn alle VMID's in het Proxmox-cluster (/etc/pve/.vmlist).
	UsedIDs []int `json:"used_ids"`
}

// PVEGuest is een CT (lxc) of VM (qemu) op de host.
type PVEGuest struct {
	Type      string `json:"type"`
	VMID      int    `json:"vmid"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Template  bool   `json:"template,omitempty"`
	Lock      string `json:"lock,omitempty"`
	Cores     int    `json:"cores,omitempty"`
	MemoryMiB int    `json:"memory_mib,omitempty"`
	// SwapMiB alleen bij een CT.
	SwapMiB     int       `json:"swap_mib,omitempty"`
	OnBoot      bool      `json:"onboot,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
	Description string    `json:"description,omitempty"`
	Disks       []PVEDisk `json:"disks,omitempty"`
	// Nets zijn de netwerkkaarten zoals in de configuratie (net0=…).
	Nets []string `json:"nets,omitempty"`
	// Features en Unprivileged alleen bij een CT.
	Features     string            `json:"features,omitempty"`
	Unprivileged bool              `json:"unprivileged,omitempty"`
	Nameserver   string            `json:"nameserver,omitempty"`
	Snapshots    []PVESnapshotInfo `json:"snapshots,omitempty"`
	// Uptime in seconden, als hij draait.
	Uptime int64 `json:"uptime,omitempty"`
}

type PVEDisk struct {
	// Name is rootfs, mp0, scsi0, virtio0, …
	Name    string `json:"name"`
	Storage string `json:"storage"`
	SizeGiB int    `json:"size_gib"`
}

type PVESnapshotInfo struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Time        time.Time `json:"time,omitzero"`
	Parent      string    `json:"parent,omitempty"`
	VMState     bool      `json:"vmstate,omitempty"`
}

type PVEStorage struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	Active  bool   `json:"active"`
	Shared  bool   `json:"shared,omitempty"`
	Total   int64  `json:"total"`
	Avail   int64  `json:"avail"`
}

// Holds zegt of er iets van soort content (rootdir, images, vztmpl) op mag.
func (s PVEStorage) Holds(content string) bool {
	return slices.Contains(strings.Split(s.Content, ","), content)
}

// PVEOSTemplate is een containertemplate die al op de host staat.
type PVEOSTemplate struct {
	// Volid is zoals local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst.
	Volid string `json:"volid"`
	Size  int64  `json:"size"`
}

// PVEAppliance is een containertemplate uit pveam available.
type PVEAppliance struct {
	Section string `json:"section"`
	File    string `json:"file"`
}

// Acties van een run pve.
const (
	PVEStart       = "start"
	PVEStop        = "stop"
	PVEShutdown    = "shutdown"
	PVEReboot      = "reboot"
	PVESuspend     = "suspend"
	PVEResume      = "resume"
	PVESet         = "set"
	PVEResize      = "resize"
	PVESnapshot    = "snapshot"
	PVERollback    = "rollback"
	PVEDelSnapshot = "delsnapshot"
	PVEMigrate     = "migrate"
	PVEDestroy     = "destroy"
	// PVECreateCT maakt een CT (pct create).
	PVECreateCT = "create_ct"
	// PVETemplate zorgt dat een containertemplate op de host staat
	// (pveam download als hij ontbreekt) en geeft zijn volid.
	PVETemplate = "template"
	// PVEBootstrap zet cf-agent met een aanmeldbestand in een CT.
	PVEBootstrap = "bootstrap"
)

// PVEActions zijn alle acties, in de volgorde van de GUI.
var PVEActions = []string{
	PVEStart, PVEShutdown, PVEReboot, PVEStop, PVESuspend, PVEResume, PVESet, PVEResize,
	PVESnapshot, PVERollback, PVEDelSnapshot, PVEMigrate, PVEDestroy, PVECreateCT, PVETemplate, PVEBootstrap,
}

// PVERun is één actie op een Proxmox-host.
type PVERun struct {
	Action string `json:"action"`
	// Type is lxc (CT) of qemu (VM); niet bij template.
	Type string `json:"type,omitempty"`
	VMID int    `json:"vmid,omitempty"`

	Set *PVESetting `json:"set,omitempty"`
	// Disk en Size bij resize: rootfs of scsi0, en +4G of 32G.
	Disk string `json:"disk,omitempty"`
	Size string `json:"size,omitempty"`
	// Snapshot bij snapshot, rollback en delsnapshot; Description en
	// VMState alleen bij snapshot (VMState alleen bij een VM).
	Snapshot    string `json:"snapshot,omitempty"`
	Description string `json:"description,omitempty"`
	VMState     bool   `json:"vmstate,omitempty"`
	// Target en TargetStorage bij migrate.
	Target        string `json:"target,omitempty"`
	TargetStorage string `json:"target_storage,omitempty"`

	Create    *PVECreate        `json:"create,omitempty"`
	Template  *PVETemplateSpec  `json:"template,omitempty"`
	Bootstrap *PVEBootstrapSpec `json:"bootstrap,omitempty"`

	// Vanaf PVEAPISince.
	API         *PVEAPICall         `json:"api,omitempty"`
	Passthrough *PVEPassthroughSpec `json:"passthrough,omitempty"`
	// Volume bij volume_free, zoals local:backup/vzdump-lxc-101-….tar.zst.
	Volume string    `json:"volume,omitempty"`
	Scrub  *PVEScrub `json:"scrub,omitempty"`
	// Enable bij maintenance: true zet de host in onderhoud.
	Enable bool `json:"enable,omitempty"`

	// Vanaf OPNsenseSince: OPN bij opn_api, Change bij opn_confirm en
	// opn_rollback, OPNKey bij opn_key.
	OPN    *OPNChange   `json:"opn,omitempty"`
	Change string       `json:"change,omitempty"`
	OPNKey *OPNKeyInput `json:"opn_key,omitempty"`
	// CARPMaintenance hoort bij opn_carp.
	CARPMaintenance *bool `json:"carp_maintenance,omitempty"`
	// Firmware hoort bij opn_firmware: check, update of upgrade.
	Firmware string `json:"firmware,omitempty"`

	// TrueNAS hoort bij truenas_call (vanaf TrueNASSince).
	TrueNAS *TrueNASMethodCall `json:"truenas,omitempty"`

	// AIV hoort bij aiv_call (vanaf AIVSince).
	AIV *AIVCallSpec `json:"aiv,omitempty"`
	// AIVExt hoort bij aiv_install.
	AIVExt *AIVExtension `json:"aiv_ext,omitempty"`
}

// PVESetting zijn de instellingen van set; nil laat staan.
type PVESetting struct {
	Cores       *int      `json:"cores,omitempty"`
	MemoryMiB   *int      `json:"memory_mib,omitempty"`
	SwapMiB     *int      `json:"swap_mib,omitempty"`
	OnBoot      *bool     `json:"onboot,omitempty"`
	Tags        *[]string `json:"tags,omitempty"`
	Description *string   `json:"description,omitempty"`
	// Nameserver en Features alleen bij een CT. Features is de hele
	// waarde, zoals nesting=1,keyctl=1; leeg haalt ze weg.
	Nameserver *string `json:"nameserver,omitempty"`
	Features   *string `json:"features,omitempty"`
}

// PVECreate is een nieuwe CT.
type PVECreate struct {
	Hostname string `json:"hostname"`
	// OSTemplate is een volid zoals local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst.
	OSTemplate string `json:"ostemplate"`
	Cores      int    `json:"cores"`
	MemoryMiB  int    `json:"memory_mib"`
	SwapMiB    int    `json:"swap_mib"`
	Storage    string `json:"storage"`
	DiskGiB    int    `json:"disk_gib"`
	Bridge     string `json:"bridge"`
	VLAN       int    `json:"vlan,omitempty"`
	// Address is een adres met prefix, zoals 10.0.20.11/24, of leeg voor
	// DHCP.
	Address string   `json:"address,omitempty"`
	Gateway string   `json:"gateway,omitempty"`
	DNS     []string `json:"dns,omitempty"`
	// SSHKeys zijn publieke sleutels voor root; HostKeys zet die van root
	// op de host erbij.
	SSHKeys  []string `json:"ssh_keys,omitempty"`
	HostKeys bool     `json:"host_keys,omitempty"`
	// Privileged maakt een bevoegde CT; standaard onbevoegd.
	Privileged bool `json:"privileged,omitempty"`
	// Nesting en Keyctl zijn features; Keyctl is nodig voor Docker in een
	// onbevoegde CT.
	Nesting     bool     `json:"nesting,omitempty"`
	Keyctl      bool     `json:"keyctl,omitempty"`
	OnBoot      bool     `json:"onboot,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Description string   `json:"description,omitempty"`
	// Start start de CT na het maken.
	Start bool `json:"start,omitempty"`
}

// PVETemplateSpec zegt welke containertemplate er moet staan. Met File
// precies dat bestand; anders de nieuwste uit pveam available die met
// Match begint en bij de architectuur van de host hoort.
type PVETemplateSpec struct {
	Storage string `json:"storage"`
	File    string `json:"file,omitempty"`
	Match   string `json:"match,omitempty"`
}

// PVEDefaultTemplate is het begin van de standaardtemplate: Debian 13.
const PVEDefaultTemplate = "debian-13-standard_"

// PVEBootstrapSpec zet cf-agent in een CT: downloaden van Server, de
// sha256 controleren, erin zetten met een aanmeldbestand en starten.
type PVEBootstrapSpec struct {
	Server    string `json:"server"`
	AllowHTTP bool   `json:"allow_http,omitempty"`
	// Token is het eenmalige aanmeldtoken; het komt nooit in de uitvoer.
	Token string `json:"token"`
	// SHA256 van cf-agent per architectuur.
	SHA256 map[string]string `json:"sha256"`
	// KnownHost is het adres van de CT voor ssh-keyscan; leeg slaat dat
	// over.
	KnownHost string `json:"known_host,omitempty"`
	// Reach zijn host:poort-adressen die de CT moet bereiken (het adres
	// van ClusterForge en NATS op 4222), vóór er iets in de CT komt
	// (fase 48). Een oudere agent slaat de controle over.
	Reach []string `json:"reach,omitempty"`
}

// PVERunResult is wat een run pve teruggeeft.
type PVERunResult struct {
	// Volid van de containertemplate (template).
	Volid string `json:"volid,omitempty"`
	// Existed: de CT bestond al met deze hostname (create_ct), of de
	// template stond er al.
	Existed bool `json:"existed,omitempty"`
	// Data is het JSON-antwoord van een aanroep (api), zoals het geheim
	// van een nieuw API-token; hoogstens 256 kB.
	Data json.RawMessage `json:"data,omitempty"`
	// UPID is de Proxmox-taak die de aanroep startte.
	UPID string `json:"upid,omitempty"`
	// RebootRequired: de host moet herstarten voor de wijziging geldt
	// (passthrough).
	RebootRequired bool `json:"reboot_required,omitempty"`
}

var (
	pveName     = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
	pveStorage  = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{0,99}$`)
	pveBridge   = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,15}$`)
	pveSnap     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{1,39}$`)
	pveDisk     = regexp.MustCompile(`^(rootfs|mp[0-9]{1,3}|scsi[0-9]{1,2}|virtio[0-9]{1,2}|sata[0-9]|ide[0-9])$`)
	pveSize     = regexp.MustCompile(`^\+?[1-9][0-9]{0,4}G$`)
	pveTag      = regexp.MustCompile(`^[a-z0-9_][a-z0-9_.+-]{0,63}$`)
	pveVolid    = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{0,99}:vztmpl/[A-Za-z0-9][A-Za-z0-9._+-]{0,200}\.tar\.(gz|xz|zst)$`)
	pveTmplFile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,200}\.tar\.(gz|xz|zst)$`)
	pveMatch    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,100}$`)
	pveFeatures = regexp.MustCompile(`^((nesting|keyctl|fuse|mknod)=[01])?(,(nesting|keyctl|fuse|mknod)=[01])*$`)
	pveToken    = regexp.MustCompile(`^[A-Za-z0-9._~+/=_-]{16,512}$`)
	pveSHA      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// SSHKeyRe is een publieke OpenSSH-sleutel op één regel.
	SSHKeyRe = regexp.MustCompile(`^(ssh-(ed25519|rsa|dss)|ecdsa-sha2-nistp(256|384|521)|sk-(ssh-ed25519|ecdsa-sha2-nistp256)@openssh\.com) [A-Za-z0-9+/=]+( [^\r\n]*)?$`)
)

// ValidPVEName is true voor een hostname of hostnaam in Proxmox.
func ValidPVEName(s string) bool { return pveName.MatchString(s) }

// ValidPVEStorage is true voor de naam van een storage.
func ValidPVEStorage(s string) bool { return pveStorage.MatchString(s) }

// ValidPVEVolid is true voor het volid van een containertemplate.
func ValidPVEVolid(s string) bool { return pveVolid.MatchString(s) }

// ValidPVETag is true voor een tag van Proxmox.
func ValidPVETag(s string) bool { return pveTag.MatchString(s) }

// ValidPVEFeatures is true voor de features van een CT.
func ValidPVEFeatures(s string) bool { return pveFeatures.MatchString(s) }

func validText(s string, n int) error {
	if len(s) > n {
		return fmt.Errorf("tekst langer dan %d tekens", n)
	}
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' {
			return fmt.Errorf("tekst met een stuurteken")
		}
	}
	return nil
}

func validDNS(list []string) error {
	if len(list) > 3 {
		return fmt.Errorf("hoogstens 3 DNS-servers")
	}
	for _, d := range list {
		if a, err := netip.ParseAddr(d); err != nil || !a.Is4() {
			return fmt.Errorf("DNS-server %q is geen IPv4-adres", d)
		}
	}
	return nil
}

// Validate controleert een actie; de agent en de server gebruiken dezelfde
// regels.
func (p PVERun) Validate() error {
	if !slices.Contains(PVEActions, p.Action) {
		return fmt.Errorf("onbekende Proxmox-actie %q", p.Action)
	}
	if !isHostAction(p.Action) {
		if p.Type != "lxc" && p.Type != "qemu" {
			return fmt.Errorf("soort gast %q is geen lxc of qemu", p.Type)
		}
		if p.VMID < 100 || p.VMID > 999999999 {
			return fmt.Errorf("ongeldig VMID %d", p.VMID)
		}
	}
	ct := p.Type == "lxc"
	switch p.Action {
	case PVESuspend, PVEResume:
		// pct suspend bestaat, maar is experimenteel; alleen VM's.
		if ct {
			return fmt.Errorf("pauzeren kan alleen bij een VM")
		}
	case PVESet:
		s := p.Set
		if s == nil {
			return fmt.Errorf("set zonder instellingen")
		}
		if s.Cores != nil && (*s.Cores < 1 || *s.Cores > 512) {
			return fmt.Errorf("cores moet tussen 1 en 512 liggen")
		}
		if s.MemoryMiB != nil && (*s.MemoryMiB < 16 || *s.MemoryMiB > 16<<20) {
			return fmt.Errorf("ongeldig geheugen %d MB", *s.MemoryMiB)
		}
		if s.SwapMiB != nil && (*s.SwapMiB < 0 || *s.SwapMiB > 16<<20 || !ct) {
			return fmt.Errorf("swap kan alleen bij een CT, tussen 0 en 16 TB")
		}
		if s.Tags != nil {
			for _, t := range *s.Tags {
				if !pveTag.MatchString(t) {
					return fmt.Errorf("ongeldige tag %q", t)
				}
			}
		}
		if s.Description != nil {
			if err := validText(*s.Description, 8192); err != nil {
				return fmt.Errorf("beschrijving: %w", err)
			}
		}
		if s.Nameserver != nil {
			if !ct {
				return fmt.Errorf("nameserver kan alleen bij een CT; een VM zet hem met cloud-init")
			}
			if err := validDNS(strings.Fields(*s.Nameserver)); err != nil {
				return err
			}
		}
		if s.Features != nil && (!ct || !pveFeatures.MatchString(*s.Features)) {
			return fmt.Errorf("ongeldige features %q (alleen bij een CT: nesting, keyctl, fuse, mknod)", *s.Features)
		}
		if s.Cores == nil && s.MemoryMiB == nil && s.SwapMiB == nil && s.OnBoot == nil && s.Tags == nil &&
			s.Description == nil && s.Nameserver == nil && s.Features == nil {
			return fmt.Errorf("set zonder instellingen")
		}
	case PVEResize:
		if !pveDisk.MatchString(p.Disk) || !pveSize.MatchString(p.Size) {
			return fmt.Errorf("ongeldige schijf %q of grootte %q (zoals +4G of 32G)", p.Disk, p.Size)
		}
	case PVESnapshot, PVERollback, PVEDelSnapshot:
		if !pveSnap.MatchString(p.Snapshot) {
			return fmt.Errorf("ongeldige snapshotnaam %q", p.Snapshot)
		}
		if err := validText(p.Description, 1024); err != nil {
			return fmt.Errorf("beschrijving: %w", err)
		}
		if p.VMState && (ct || p.Action != PVESnapshot) {
			return fmt.Errorf("het geheugen bewaren kan alleen bij een snapshot van een VM")
		}
	case PVEMigrate:
		if !pveName.MatchString(p.Target) {
			return fmt.Errorf("ongeldige doelhost %q", p.Target)
		}
		if p.TargetStorage != "" && !pveStorage.MatchString(p.TargetStorage) {
			return fmt.Errorf("ongeldige doelstorage %q", p.TargetStorage)
		}
	case PVECreateCT:
		if !ct || p.Create == nil {
			return fmt.Errorf("create_ct vraagt een CT met instellingen")
		}
		return p.Create.validate()
	case PVETemplate:
		t := p.Template
		if t == nil || !pveStorage.MatchString(t.Storage) {
			return fmt.Errorf("template zonder geldige storage")
		}
		if (t.File == "") == (t.Match == "") {
			return fmt.Errorf("geef een bestand of een begin van de naam, niet allebei")
		}
		if t.File != "" && !pveTmplFile.MatchString(t.File) {
			return fmt.Errorf("ongeldige containertemplate %q", t.File)
		}
		if t.Match != "" && !pveMatch.MatchString(t.Match) {
			return fmt.Errorf("ongeldig begin van een naam %q", t.Match)
		}
	case PVEAPI, PVEPassthrough, PVEVolumeFree, PVEZpoolScrub, PVEMaintenance, PBSAPI,
		OPNAPI, OPNConfirm, OPNRollback, OPNHASync, OPNKey, TrueNASCall, AIVCall, AIVInstall:
		return p.validateHost()
	case PVEBootstrap:
		b := p.Bootstrap
		if !ct || b == nil {
			return fmt.Errorf("bootstrap vraagt een CT met instellingen")
		}
		if !strings.HasPrefix(b.Server, "https://") && (!b.AllowHTTP || !strings.HasPrefix(b.Server, "http://")) {
			return fmt.Errorf("ongeldig adres van ClusterForge %q", b.Server)
		}
		if strings.ContainsAny(b.Server, " \"'\n") {
			return fmt.Errorf("ongeldig adres van ClusterForge %q", b.Server)
		}
		if !pveToken.MatchString(b.Token) {
			return fmt.Errorf("ongeldig aanmeldtoken")
		}
		if len(b.SHA256) == 0 {
			return fmt.Errorf("de sha256 van cf-agent ontbreekt")
		}
		for arch, h := range b.SHA256 {
			if !pveSHA.MatchString(h) || (arch != "amd64" && arch != "arm64") {
				return fmt.Errorf("ongeldige sha256 voor %q", arch)
			}
		}
		if b.KnownHost != "" {
			if a, err := netip.ParseAddr(b.KnownHost); err != nil || !a.Is4() {
				return fmt.Errorf("ongeldig adres %q", b.KnownHost)
			}
		}
	}
	return nil
}

func (c *PVECreate) validate() error {
	switch {
	case !pveName.MatchString(c.Hostname):
		return fmt.Errorf("ongeldige hostname %q", c.Hostname)
	case !pveVolid.MatchString(c.OSTemplate):
		return fmt.Errorf("ongeldige containertemplate %q", c.OSTemplate)
	case c.Cores < 1 || c.Cores > 512:
		return fmt.Errorf("cores moet tussen 1 en 512 liggen")
	case c.MemoryMiB < 64 || c.MemoryMiB > 16<<20:
		return fmt.Errorf("ongeldig geheugen %d MB", c.MemoryMiB)
	case c.SwapMiB < 0 || c.SwapMiB > 16<<20:
		return fmt.Errorf("ongeldige swap %d MB", c.SwapMiB)
	case !pveStorage.MatchString(c.Storage):
		return fmt.Errorf("ongeldige storage %q", c.Storage)
	case c.DiskGiB < 1 || c.DiskGiB > 65536:
		return fmt.Errorf("de schijf moet tussen 1 en 65536 GB zijn")
	case !pveBridge.MatchString(c.Bridge):
		return fmt.Errorf("ongeldige bridge %q", c.Bridge)
	case c.VLAN < 0 || c.VLAN > 4094:
		return fmt.Errorf("VLAN moet tussen 1 en 4094 liggen")
	}
	if c.Address != "" {
		pf, err := netip.ParsePrefix(c.Address)
		if err != nil || !pf.Addr().Is4() || pf.Bits() < 8 || pf.Bits() > 30 {
			return fmt.Errorf("ongeldig adres %q (zoals 10.0.20.11/24)", c.Address)
		}
		gw, err := netip.ParseAddr(c.Gateway)
		if err != nil || !gw.Is4() || !pf.Masked().Contains(gw) {
			return fmt.Errorf("de gateway %q ligt niet in %s", c.Gateway, pf.Masked())
		}
	} else if c.Gateway != "" {
		return fmt.Errorf("een gateway hoort bij een vast adres")
	}
	if err := validDNS(c.DNS); err != nil {
		return err
	}
	if len(c.SSHKeys) > 20 {
		return fmt.Errorf("hoogstens 20 SSH-sleutels")
	}
	for _, k := range c.SSHKeys {
		if !SSHKeyRe.MatchString(k) {
			return fmt.Errorf("%.30q is geen publieke SSH-sleutel", k)
		}
	}
	for _, t := range c.Tags {
		if !pveTag.MatchString(t) {
			return fmt.Errorf("ongeldige tag %q", t)
		}
	}
	return validText(c.Description, 8192)
}

// PVETimeout is de tijdslimiet van een run pve.
func (p PVERun) Timeout() time.Duration {
	switch p.Action {
	case PVEMigrate, PVETemplate, PVEAPI, PBSAPI, TrueNASCall:
		// Een aanroep kan een back-up, terugzetten of verhuizen zijn.
		return 4 * time.Hour
	case PVEPassthrough:
		return 30 * time.Minute
	case PVECreateCT, PVEBootstrap, PVESnapshot, PVERollback, PVEDelSnapshot, PVEDestroy:
		return 30 * time.Minute
	}
	return 10 * time.Minute
}
