// Testlijst 3, N11: dezelfde regel als internal/commands/readonly.go. Een
// leescommando (df, systemctl status, pvesm status, ...) verandert niets en
// vraagt op prod geen bevestiging. Bij twijfel: nee (de server beslist).

const readCmds: Record<string, string[] | null> = {
  cat: null, ls: null, df: null, du: null, free: null, uptime: null, ps: null, top: null,
  uname: null, hostname: null, hostnamectl: ["status"], whoami: null, id: null, date: null,
  lsblk: null, findmnt: null, blkid: null, lscpu: null, lsmod: null, lspci: null, lsusb: null,
  ip: ["a", "addr", "address", "r", "route", "link", "neigh", "-br", "-4", "-6", "-s"],
  ss: null, netstat: null, ping: null, dig: null, host: null, nslookup: null, curl: null,
  grep: null, egrep: null, head: null, tail: null, wc: null, sort: null, uniq: null, cut: null,
  tr: null, echo: null, printf: null, stat: null, file: null, which: null, test: null, true: null,
  journalctl: null, dmesg: null, last: null, who: null, w: null, env: null, getent: null,
  systemctl: ["status", "is-active", "is-enabled", "is-failed", "list-units", "list-unit-files", "list-timers", "show", "cat", "--failed"],
  pvesm: ["status", "list", "path"],
  pveam: ["list", "available"],
  pct: ["list", "status", "config", "listsnapshot", "pending"],
  qm: ["list", "status", "config", "listsnapshot", "pending"],
  pvesh: ["get", "ls"],
  pveversion: null, pvecm: ["status", "nodes"], "ha-manager": ["status", "config"],
  docker: ["ps", "inspect", "logs", "images", "info", "version", "stats", "top", "port"],
  ufw: ["status"],
  nft: ["list"],
  apt: ["list", "policy", "show"], "apt-cache": null, dpkg: ["-l", "-s", "-L", "--list", "--status"],
  zpool: ["status", "list", "iostat"], zfs: ["list", "get"],
  smartctl: ["-a", "-H", "-i", "-x"], sensors: null, timedatectl: ["status", "show"],
  keepalived: ["--version", "-v"], haproxy: ["-v", "-c"], nginx: ["-t", "-v", "-V"],
};

const isDuration = (s: string) => /^[0-9.]+[smhd]*$/.test(s) && /[0-9]/.test(s);

function hasWriteFlag(args: string[]): boolean {
  return args.some(
    (a) =>
      ["-X", "-d", "-o", "-O", "-T", "-F"].includes(a) ||
      ["--data", "--request", "--output", "--upload", "--form"].some((p) => a.startsWith(p)),
  );
}

export function readOnlyScript(script: string): boolean {
  if (!script.trim() || /[><`]/.test(script) || script.includes("$(")) return false;
  let any = false;
  for (let part of script.split(/\|\||&&|[;|\n]/)) {
    part = part.trim();
    if (!part || part.startsWith("#")) continue;
    let f = part.split(/\s+/);
    while (f.length > 0 && (f[0] === "sudo" || f[0] === "timeout" || isDuration(f[0]) || f[0] === "set")) {
      if (f[0] === "set") {
        f = [];
        break;
      }
      f = f.slice(1);
    }
    if (f.length === 0) continue;
    if (!Object.prototype.hasOwnProperty.call(readCmds, f[0])) return false;
    const subs = readCmds[f[0]];
    if (subs) {
      if (f.length < 2) {
        if (!["systemctl", "pct", "qm", "ufw"].includes(f[0])) return false;
      } else if (!subs.includes(f[1])) return false;
    }
    if (f[0] === "ip" && f.slice(1).some((a) => ["add", "del", "delete", "set", "flush", "change", "replace", "append"].includes(a))) return false;
    if (f[0] === "curl" && hasWriteFlag(f.slice(1))) return false;
    any = true;
  }
  return any;
}
