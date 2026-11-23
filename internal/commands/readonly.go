package commands

import (
	"regexp"
	"strings"
)

// Testlijst 2.2 en 2.3: een leescommando (pvesm status, df, systemctl
// status, ...) verandert niets. Zo'n commando loopt op alle nodes door, ook
// als het op een node faalt, en heeft een korte tijdslimiet per node, zodat
// één hangende host (een offline opslag achter pvesm status) de rest niet
// ophoudt.

// ReadTimeoutSeconds is de tijdslimiet per node voor een leescommando.
const ReadTimeoutSeconds = 120

// readCmds zijn commando's die alleen lezen; een lijst achter de naam
// beperkt tot die subcommando's.
var readCmds = map[string][]string{
	"cat": nil, "ls": nil, "df": nil, "du": nil, "free": nil, "uptime": nil, "ps": nil, "top": nil,
	"uname": nil, "hostname": nil, "hostnamectl": {"status"}, "whoami": nil, "id": nil, "date": nil,
	"lsblk": nil, "findmnt": nil, "blkid": nil, "lscpu": nil, "lsmod": nil, "lspci": nil, "lsusb": nil,
	"ip": {"a", "addr", "address", "r", "route", "link", "neigh", "-br", "-4", "-6", "-s"},
	"ss": nil, "netstat": nil, "ping": nil, "dig": nil, "host": nil, "nslookup": nil, "curl": nil,
	"grep": nil, "egrep": nil, "head": nil, "tail": nil, "wc": nil, "sort": nil, "uniq": nil, "cut": nil,
	"tr": nil, "echo": nil, "printf": nil, "stat": nil, "file": nil, "which": nil, "test": nil, "true": nil,
	"journalctl": nil, "dmesg": nil, "last": nil, "who": nil, "w": nil, "env": nil, "getent": nil,
	"systemctl":  {"status", "is-active", "is-enabled", "is-failed", "list-units", "list-unit-files", "list-timers", "show", "cat", "--failed"},
	"pvesm":      {"status", "list", "path"},
	"pveam":      {"list", "available"},
	"pct":        {"list", "status", "config", "listsnapshot", "pending"},
	"qm":         {"list", "status", "config", "listsnapshot", "pending"},
	"pvesh":      {"get", "ls"},
	"pveversion": nil, "pvecm": {"status", "nodes"}, "ha-manager": {"status", "config"},
	"docker": {"ps", "inspect", "logs", "images", "info", "version", "stats", "top", "port"},
	"ufw":    {"status"},
	"nft":    {"list"},
	"apt":    {"list", "policy", "show"}, "apt-cache": nil, "dpkg": {"-l", "-s", "-L", "--list", "--status"},
	"zpool": {"status", "list", "iostat"}, "zfs": {"list", "get"},
	"smartctl": {"-a", "-H", "-i", "-x"}, "sensors": nil, "timedatectl": {"status", "show"},
	"keepalived": {"--version", "-v"}, "haproxy": {"-v", "-c"}, "nginx": {"-t", "-v", "-V"},
}

var splitCmds = regexp.MustCompile(`\|\||&&|[;|\n]`)

// ReadOnly zegt of elk commando in het script alleen leest. Bij twijfel is
// het antwoord nee: dan blijft het gedrag van vroeger.
func ReadOnly(script string) bool {
	if strings.TrimSpace(script) == "" || strings.ContainsAny(script, "><`") || strings.Contains(script, "$(") {
		return false
	}
	any := false
	for _, part := range splitCmds.Split(script, -1) {
		part = strings.TrimSpace(part)
		if part == "" || strings.HasPrefix(part, "#") {
			continue
		}
		f := strings.Fields(part)
		for len(f) > 0 && (f[0] == "sudo" || f[0] == "timeout" || isDuration(f[0]) || f[0] == "set") {
			if f[0] == "set" {
				f = nil
				break
			}
			f = f[1:]
		}
		if len(f) == 0 {
			continue
		}
		subs, ok := readCmds[f[0]]
		if !ok {
			return false
		}
		if subs != nil {
			if len(f) < 2 {
				// "systemctl" zonder argument toont units; "ip" zonder niets.
				if f[0] != "systemctl" && f[0] != "pct" && f[0] != "qm" && f[0] != "ufw" {
					return false
				}
			} else if !contains(subs, f[1]) {
				return false
			}
		}
		if f[0] == "ip" && containsAny(f[1:], "add", "del", "delete", "set", "flush", "change", "replace", "append") {
			return false
		}
		if f[0] == "curl" && hasWriteFlag(f[1:]) {
			return false
		}
		any = true
	}
	return any
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func containsAny(list []string, words ...string) bool {
	for _, w := range words {
		if contains(list, w) {
			return true
		}
	}
	return false
}

func isDuration(s string) bool {
	if s == "" {
		return false
	}
	s = strings.TrimRight(s, "smhd")
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return s != ""
}

// hasWriteFlag: curl met -X, -d, -o of -T stuurt of schrijft iets.
func hasWriteFlag(args []string) bool {
	for _, a := range args {
		switch {
		case a == "-X" || a == "-d" || a == "-o" || a == "-O" || a == "-T" || a == "-F",
			strings.HasPrefix(a, "--data"), strings.HasPrefix(a, "--request"), strings.HasPrefix(a, "--output"),
			strings.HasPrefix(a, "--upload"), strings.HasPrefix(a, "--form"):
			return true
		}
	}
	return false
}
