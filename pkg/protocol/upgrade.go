package protocol

import "time"

// De agent werkt zichzelf bij (agent.upgrade): hij haalt de nieuwe binary
// van zijn eigen server, controleert de sha256 uit het commando, vervangt
// zichzelf en herstart via systemd. Verbindt de nieuwe agent niet binnen
// UpgradeRollbackAfter, dan zet een timer van systemd de oude terug.

// SelfUpgradeSince is de eerste protocolversie die CmdAgentUpgrade kent en
// Arch en BinarySHA256 in de heartbeat meldt. Een oudere agent werk je één
// keer met de hand bij: agent.sh --upgrade.
const SelfUpgradeSince = 23

// CmdAgentUpgrade werkt de agent bij naar Command.Upgrade.
const CmdAgentUpgrade = "agent.upgrade"

// UpgradeRollbackAfter is hoe lang een nieuwe agent heeft om met de server
// te praten; daarna zet de agent de oude binary terug.
const UpgradeRollbackAfter = 3 * time.Minute

// UpgradeTimeout is hoe lang de agent over downloaden en controleren mag
// doen.
const UpgradeTimeout = 5 * time.Minute

// AgentUpgrade beschrijft de agent die de server aanbiedt.
type AgentUpgrade struct {
	// Version is de versie die de nieuwe agent meldt, voor de uitvoer.
	Version string `json:"version"`
	// Protocol is zijn protocolversie; de agent gaat nooit naar een lagere.
	Protocol int `json:"protocol"`
	// SHA256 is de sha256 van de binary per architectuur (amd64, arm64).
	// Die komt over de vastgepinde NATS-verbinding, dus de download zelf
	// mag over http.
	SHA256 map[string]string `json:"sha256"`
}

// SelfCheck is wat cf-agent self-check print: zo controleert de oude agent
// dat de nieuwe binary op deze machine draait en welk protocol hij heeft.
type SelfCheck struct {
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
	Arch     string `json:"arch"`
}
