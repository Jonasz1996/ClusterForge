# ext56 — ClusterForge-koppeling (fase 38 van ClusterForge). AI-verslag krijgt context uit ClusterForge, zodat de AI weet welke
#  adressen servers van Jonas zijn, in welk cluster en welke omgeving ze zitten, wanneer er gepland onderhoud is en wat ClusterForge
#  er de laatste 24 uur aan veranderde. Zo geeft AI-verslag minder vals alarm bij een geplande herstart of failover, en kan het bij
#  een storing zeggen "om 14:02 werd via ClusterForge de agent bijgewerkt".
#
#  Hoe: ClusterForge (de cf-agent op deze machine, met de app-sleutel "ClusterForge") stuurt elke 5 minuten de context naar
#    POST /api/inzicht/clusterforge/context   {bron, versie, ts, nodes, vips, onderhoud, wijzigingen}
#  en ext56 zet die om in regels van de kennisbank (v2/kennis.py, 📘 Inzicht › Kennis) met een id die begint met "cf-": één regel
#  per server en per VIP. Een regel komt vanzelf mee in elke AI-prompt waarin het IP of de naam voorkomt. Tijdens gepland
#  onderhoud krijgt de regel "ernst hooguit midden" (tenzij de feiten een echte, lopende aanval tonen). Regels van servers die
#  niet meer in ClusterForge staan, verdwijnen. Een server wordt NOOIT als "eigen infrastructuur" (nooit een dreiging) gemarkeerd:
#  een gekaapte server moet nog altijd opvallen.
#    GET  /api/inzicht/clusterforge           stand: laatste context, aantal regels, onderhoud nu
#  Geen instellingen; uitzetten: de regels cf-* in Inzicht › Kennis uitzetten, of de koppeling ClusterForge intrekken (📱 App).
#
#  Inhaken: onderaan app.py `import ext56; ext56.routes(app, guard)` — ClusterForge doet dat zelf bij "Koppeling installeren"
#  (met een kopie van app.py en terugzetten als de webdienst daarna niet start).
import re
import sys
import time

VERSIE = "56.1"
PREFIX = "cf-"
MAX_NODES = 400
MAX_WIJZ = 300


def log(*a):
    print("ext56:", *a, file=sys.stderr, flush=True)


def _s(v, n=120):
    return re.sub(r"\s+", " ", str(v or "")).strip()[:n]


def _ip_patroon(ip):
    ip = _s(ip, 45)
    if not re.fullmatch(r"[0-9a-fA-F:.]{3,45}", ip):
        return None
    if ":" in ip:
        return re.escape(ip.lower())
    return r"(?<![\d.])" + re.escape(ip) + r"(?![\d])"


def _naam_patroon(naam):
    naam = _s(naam, 63)
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{2,62}", naam):
        return None
    return r"\b" + re.escape(naam) + r"\b"


def _slug(s):
    return re.sub(r"[^a-z0-9-]", "-", str(s).lower()).strip("-")[:30] or "x"


def _tijd(ts):
    try:
        return time.strftime("%d/%m %H:%M", time.localtime(int(ts)))
    except Exception:
        return "?"


def _onderhoud_nu(onderhoud, nu):
    """{naam van node of cluster: (tot, reden)} voor vensters die nu lopen of binnen het uur beginnen."""
    uit = {}
    for o in onderhoud or []:
        if not isinstance(o, dict):
            continue
        try:
            van, tot = int(o.get("van") or 0), int(o.get("tot") or 0)
        except Exception:
            continue
        if not (van - 3600 <= nu <= tot):
            continue
        for k in list(o.get("nodes") or []) + list(o.get("clusters") or []):
            uit[_s(k, 80).lower()] = (tot, _s(o.get("reden") or o.get("naam") or "gepland onderhoud", 120))
    return uit


def regels(ctx, nu=None):
    """De kennisregels (dicts zoals v2/kennis) die bij deze context horen."""
    nu = int(nu or time.time())
    onderhoud = _onderhoud_nu(ctx.get("onderhoud"), nu)
    wijz = {}
    for w in (ctx.get("wijzigingen") or [])[:MAX_WIJZ]:
        if not isinstance(w, dict):
            continue
        for k in (w.get("node"), w.get("cluster")):
            if k:
                wijz.setdefault(_s(k, 80).lower(), []).append(w)
    uit = []
    gezien = set()

    def maak(rid, delen_patroon, titel, wat, sleutels):
        delen_patroon = [p for p in delen_patroon if p]
        if not delen_patroon or rid in gezien:
            return
        gezien.add(rid)
        tekst = [wat]
        ernst = ""
        for k in sleutels:
            o = onderhoud.get((k or "").lower())
            if o:
                tekst.append(f"Gepland onderhoud via ClusterForge tot {_tijd(o[0])} ({o[1]}): herstarts, uitval en failover zijn dan verwacht.")
                ernst = "midden"
                break
        recent = []
        for k in sleutels:
            recent += wijz.get((k or "").lower(), [])
        recent = sorted({(int(w.get("ts") or 0), _s(w.get("tekst"), 90)) for w in recent}, reverse=True)[:4]
        if recent:
            tekst.append("Recente wijzigingen via ClusterForge: " + "; ".join(f"{_tijd(t)} {x}" for t, x in recent) + ".")
        uit.append({"id": rid[:40], "patroon": "|".join(delen_patroon)[:300], "titel": _s(titel, 80), "uitleg": " ".join(tekst)[:600],
                    "actie": "Kijk in ClusterForge wat er met deze server gebeurt (Nodes, Logboek, Aandacht nodig).", "ernst_max": ernst,
                    "richting": "lokaal"})

    for n in (ctx.get("nodes") or [])[:MAX_NODES]:
        if not isinstance(n, dict) or not n.get("naam"):
            continue
        naam = _s(n["naam"], 63)
        ips = [i for i in (n.get("ips") or []) if isinstance(i, str)][:6]
        cl, omg, rol = _s(n.get("cluster"), 60), _s(n.get("omgeving"), 12), _s(n.get("rol"), 60)
        wat = f"{naam} ({', '.join(ips) or 'geen IP'}) is een server van Jonas, beheerd door ClusterForge"
        if cl:
            wat += f": cluster {cl}" + (f" ({omg})" if omg else "")
        if rol:
            wat += f", {rol}"
        wat += "."
        maak(PREFIX + "n-" + _slug(naam), [_ip_patroon(i) for i in ips] + [_naam_patroon(naam)], f"ClusterForge: {naam}", wat, [naam, cl])
    for v in (ctx.get("vips") or [])[:MAX_NODES]:
        if not isinstance(v, dict) or not v.get("ip"):
            continue
        cl, omg = _s(v.get("cluster"), 60), _s(v.get("omgeving"), 12)
        eig = _s(v.get("eigenaar"), 63)
        wat = f"{_s(v['ip'], 45)} is het virtuele IP (VIP, Keepalived/CARP) van cluster {cl or '?'}" + (f" ({omg})" if omg else "") + \
              (f"; nu bij {eig}" if eig else "") + ". Een VIP dat van node wisselt is een failover, geen spoofing."
        maak(PREFIX + "v-" + _slug(v["ip"]), [_ip_patroon(v["ip"])], f"ClusterForge: VIP {cl or v['ip']}", wat, [cl])
    return uit


def toepassen(ctx):
    from v2 import kern, kennis
    nieuw = regels(ctx)
    c = kern.db()
    try:
        oud = {r[0] for r in c.execute("SELECT id FROM kennis WHERE id LIKE ?", (PREFIX + "%",))}
        for r in nieuw:
            c.execute("INSERT OR REPLACE INTO kennis(id, patroon, titel, uitleg, actie, ernst_max, richting, ts) VALUES(?,?,?,?,?,?,?,?)",
                      (r["id"], r["patroon"], r["titel"], r["uitleg"], r["actie"], r["ernst_max"], r["richting"], kern.nu()))
        weg = oud - {r["id"] for r in nieuw}
        for rid in weg:
            c.execute("DELETE FROM kennis WHERE id=?", (rid,))
        stand = {"ts": int(time.time()), "bron_ts": ctx.get("ts"), "bron": _s(ctx.get("bron"), 40), "versie": _s(ctx.get("versie"), 40),
                 "nodes": len(ctx.get("nodes") or []), "vips": len(ctx.get("vips") or []), "regels": len(nieuw), "weg": len(weg),
                 "onderhoud": [r["titel"] for r in nieuw if r["ernst_max"]][:20], "wijzigingen": len(ctx.get("wijzigingen") or [])}
        kern.mset(c, "clusterforge", stand)
        c.commit()
    finally:
        c.close()
    kennis.vergeet_cache()
    return stand


def p_context(req, body):
    if not isinstance(body, dict) or not isinstance(body.get("nodes"), list):
        raise ValueError("context van ClusterForge verwacht (nodes)")
    return {"ok": True, "ext": VERSIE, "stand": toepassen(body)}


def g_status(req, body):
    from v2 import kern
    c = kern.db()
    try:
        stand = kern.mget(c, "clusterforge") or {}
        n = c.execute("SELECT COUNT(*) FROM kennis WHERE id LIKE ?", (PREFIX + "%",)).fetchone()[0]
    finally:
        c.close()
    return {"ext": VERSIE, "regels": n, "stand": stand}


def routes(app, guard):
    if getattr(app.state, "aiv_ext56", False):
        return
    app.state.aiv_ext56 = True
    from v2 import web as w2
    w2.registreer(app, guard, {"clusterforge": g_status}, {"clusterforge/context": p_context})
    log("ClusterForge-koppeling", VERSIE, "geladen")
