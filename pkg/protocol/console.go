package protocol

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Console (fase 26 M7): een scherm (noVNC) of terminal (xterm) van een gast
// of host in de GUI, via de agent op de Proxmox-host. De server vraagt een
// sessie aan op cf.node.<id>.console; daarna stroomt het verkeer in frames:
// van de server naar de agent op ConsoleIn, terug op ConsoleOut. Elk frame
// begint met een byte die zegt wat het is.

// ConsoleSince is de eerste protocolversie met een console.
const ConsoleSince = 37

// SubjectConsole draagt het openen van een sessie: cf.node.<id>.console.
const SubjectConsole = "console"

const (
	TypeConsoleRequest = "console_request"
	TypeConsoleResult  = "console_result"
)

// Soorten console.
const (
	// ConsoleVNC is het scherm van een VM: qm vncproxy, RFB naar noVNC.
	ConsoleVNC = "vnc"
	// ConsoleSerial is de seriële poort van een VM: qm terminal.
	ConsoleSerial = "serial"
	// ConsoleCT is een rootshell in een CT: pct enter.
	ConsoleCT = "ct"
	// ConsoleHost is een rootshell op de Proxmox-host zelf.
	ConsoleHost = "host"
	// ConsoleDocker is een shell of commando in een container: docker exec
	// (vanaf DockerSince, op elke node met Docker).
	ConsoleDocker = "docker"
)

// Shells van ConsoleDocker.
const (
	// DockerShellAuto is bash als de container die heeft, anders sh.
	DockerShellAuto = "auto"
	DockerShellSh   = "sh"
	DockerShellBash = "bash"
)

// Frames. De eerste byte van elk bericht op ConsoleIn en ConsoleOut.
const (
	// FrameData: de rest zijn bytes voor of van het programma.
	FrameData byte = 'd'
	// FrameResize: de rest is JSON {"cols":…,"rows":…} (alleen terminals).
	FrameResize byte = 'r'
	// FramePing houdt de sessie open; de agent sluit hem na ConsoleIdle
	// zonder enig bericht.
	FramePing byte = 'p'
	// FrameClose sluit de sessie; de rest is de reden (naar de server) of
	// leeg (naar de agent).
	FrameClose byte = 'c'
)

const (
	// ConsoleIdle is hoe lang de agent een sessie zonder bericht openhoudt.
	ConsoleIdle = 60 * time.Second
	// ConsolePing is hoe vaak de server een ping stuurt.
	ConsolePing = 15 * time.Second
	// ConsoleChunk is het grootste dataframe; ruim onder de grens van NATS.
	ConsoleChunk = 32 << 10
	// MaxConsoleSessions is het meeste aantal sessies tegelijk op één agent.
	MaxConsoleSessions = 8
	// ConsoleRequestTimeout is hoe lang openen mag duren.
	ConsoleRequestTimeout = 20 * time.Second
)

type ConsoleRequest struct {
	ID       string    `json:"id"`
	Deadline time.Time `json:"deadline"`
	// Session is een UUID; de subjects van de sessie hangen eraan.
	Session string `json:"session"`
	Kind    string `json:"kind"`
	// VMID voor vnc, serial en ct.
	VMID int `json:"vmid,omitempty"`
	Cols int `json:"cols,omitempty"`
	Rows int `json:"rows,omitempty"`
	// Container en Shell voor docker; User is de gebruiker in de container
	// (leeg is wat het image zegt).
	Container string `json:"container,omitempty"`
	Shell     string `json:"shell,omitempty"`
	User      string `json:"user,omitempty"`
}

type ConsoleResponse struct {
	Error string `json:"error,omitempty"`
	// Password is het eenmalige VNC-wachtwoord (vnc); QEMU laat het na 30
	// seconden vervallen, een open verbinding blijft.
	Password string `json:"password,omitempty"`
}

// ConsoleResize is de inhoud van FrameResize.
type ConsoleResize struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// ConsoleIn is het subject waarop de agent de frames van de server krijgt.
func ConsoleIn(nodeID, session string) string {
	return Subject(nodeID, SubjectConsole) + "." + session + ".in"
}

// ConsoleOut is het subject waarop de agent zijn uitvoer publiceert.
func ConsoleOut(nodeID, session string) string {
	return Subject(nodeID, SubjectConsole) + "." + session + ".out"
}

// Validate controleert de vorm van een verzoek.
func (r ConsoleRequest) Validate() error {
	if _, err := uuid.Parse(r.Session); err != nil {
		return errors.New("ongeldige consolesessie")
	}
	switch r.Kind {
	case ConsoleVNC, ConsoleSerial, ConsoleCT:
		if r.VMID < 100 || r.VMID > 999999999 {
			return errors.New("ongeldig gast-ID")
		}
	case ConsoleHost:
		if r.VMID != 0 {
			return errors.New("een hostconsole heeft geen gast-ID")
		}
	case ConsoleDocker:
		if r.VMID != 0 || !ValidContainerName(r.Container) {
			return errors.New("ongeldige container")
		}
		switch r.Shell {
		case DockerShellAuto, DockerShellSh, DockerShellBash:
		default:
			return errors.New("onbekende shell")
		}
		if r.User != "" && !runUser.MatchString(r.User) {
			return errors.New("ongeldige gebruiker")
		}
	default:
		return errors.New("onbekende soort console")
	}
	if r.Cols < 0 || r.Cols > 1000 || r.Rows < 0 || r.Rows > 1000 {
		return errors.New("ongeldige terminalgrootte")
	}
	return nil
}

// Terminal zegt of de soort een terminal (xterm) is in plaats van een scherm.
func (r ConsoleRequest) Terminal() bool { return r.Kind != ConsoleVNC }
