#!/usr/bin/env python3
"""Origin deployment checks for Deed behind Cloudflare (stdlib only). Invoked by scripts/validate/origin.sh, which starts
five production-mode Deed containers, an nginx edge and two client containers on one docker network ($SUB.0/24):

  .10 edge        nginx: plain reverse proxy on 8001/8002, Cloudflare-like (sets CF-Connecting-IP) on 8003-8007
  .11 trusted     proxy.trusted_proxies = [edge]                       cloudflare: false
  .12 untrusted   proxy.trusted_proxies = []                           cloudflare: false
  .13 cloudflare  proxy.trusted_proxies = []                           cloudflare: true (built-in Cloudflare ranges)
  .14 localproxy  proxy.trusted_proxies = [edge]                       cloudflare: true (built-in Cloudflare ranges)
  .15 origin      TLS on 443 + client_ca_file (Authenticated Origin Pulls), cloudflare: true with cloudflare_ranges =
                  [edge] (the edge plays Cloudflare), published on 127.0.0.1:$ORIGIN_PORT
  .21/.22         clients (curl)

The address Deed resolved is read back from its access log (source.ip of the record with the response's X-Request-ID)
and from the audit records of failed logins (ECS source.ip). Every step prints PASS/FAIL with evidence;
$VAL_DIR/results.json holds the lot."""
import ipaddress, json, os, re, socket, ssl, subprocess, sys, time, warnings

warnings.filterwarnings("ignore", category=DeprecationWarning)  # TLS 1.0/1.1 are offered on purpose (step 4)

VAL = os.environ["VAL_DIR"]
SEC = os.path.join(VAL, "secrets")
P, SUB, PORT = os.environ["P"], os.environ["SUB"], int(os.environ["ORIGIN_PORT"])
FW_IMAGE = os.environ["FW_IMAGE"]
ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
EDGE, C1, C2 = f"{SUB}.10", f"{SUB}.21", f"{SUB}.22"
DEED = {"trusted": f"{SUB}.11", "untrusted": f"{SUB}.12", "cloudflare": f"{SUB}.13", "localproxy": f"{SUB}.14", "origin": f"{SUB}.15"}
CLIENT = {C1: f"{P}-client1", C2: f"{P}-client2"}
SPOOF = "198.51.100.7"  # RFC 5737: what a client claims to be
HOST = "deed.example.com"
results = []


def sec(name):
    return os.path.join(SEC, name)


def run(args, input=None, timeout=60):
    p = subprocess.run(args, input=input, capture_output=True, text=True, timeout=timeout)
    return p.returncode, p.stdout, p.stderr


def step(n, title):
    def deco(fn):
        def wrapped():
            t0 = time.time()
            try:
                ev = fn() or {}
                results.append({"step": n, "title": title, "ok": True, "evidence": ev, "seconds": round(time.time() - t0, 1)})
                print(f"PASS {n:>2}. {title}\n        {json.dumps(ev)[:1800]}")
            except Exception as ex:  # noqa: BLE001 — every failure is reported, then the run continues
                results.append({"step": n, "title": title, "ok": False, "error": str(ex)[:3000], "seconds": round(time.time() - t0, 1)})
                print(f"FAIL {n:>2}. {title}\n        {ex}")
        return wrapped
    return deco


def expect(cond, what, ev=None):
    if not cond:
        raise AssertionError(what + (f": {json.dumps(ev)[:1500]}" if ev is not None else ""))


# ---- host-side TLS client --------------------------------------------------------------------------------------------

def tls_get(cert=None, minv=None, maxv=None, legacy=False, path="/api/v1/system/info"):
    """GET path from the origin's published port as deed.example.com, verifying the server against the origin root.
    Returns a dict: ok, version, status, headers, peer (subject/SAN) or error (the TLS alert)."""
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    ctx.load_verify_locations(sec("origin-root.pem"))
    if cert:
        ctx.load_cert_chain(sec(cert + ".pem"), sec(cert + "-key.pem"))
    if legacy:  # let the local library offer TLS 1.0/1.1 so the refusal is the server's
        ctx.set_ciphers("DEFAULT:@SECLEVEL=0")
    if minv:
        ctx.minimum_version = minv
    if maxv:
        ctx.maximum_version = maxv
    try:
        with socket.create_connection(("127.0.0.1", PORT), timeout=10) as s, ctx.wrap_socket(s, server_hostname=HOST) as t:
            t.sendall(f"GET {path} HTTP/1.1\r\nHost: {HOST}\r\nConnection: close\r\n\r\n".encode())
            data = b""
            while chunk := t.recv(65536):
                data += chunk
            head = data.split(b"\r\n\r\n", 1)[0].decode(errors="replace").split("\r\n")
            hdrs = {k.lower(): v.strip() for k, _, v in (h.partition(":") for h in head[1:])}
            pc = t.getpeercert()
            return {"ok": bool(head[0]), "version": t.version(), "cipher": t.cipher()[0], "status": head[0], "headers": hdrs,
                    "peer": {"subject": dict(x[0] for x in pc["subject"]), "issuer": dict(x[0] for x in pc["issuer"]),
                             "san": pc.get("subjectAltName")}}
    except (ssl.SSLError, OSError) as e:
        return {"ok": False, "error": f"{type(e).__name__}: {getattr(e, 'reason', None) or e}"}


def refused(r):
    return not r["ok"] and ("ALERT" in r["error"].upper() or "RESET" in r["error"].upper() or "EOF" in r["error"].upper())


# ---- in-network clients ----------------------------------------------------------------------------------------------

def curl(client, url, headers=None, method="GET", body=None, extra=()):
    """curl from a client container; returns (exit code, status, headers, body)."""
    args = ["docker", "exec", CLIENT[client], "curl", "-sS", "-i", "--max-time", "8", "--connect-timeout", "4", "-X", method, *extra]
    for k, v in (headers or {}).items():
        args += ["-H", f"{k}: {v}"]
    if body is not None:
        args += ["-H", "Content-Type: application/json", "--data-binary", json.dumps(body)]
    rc, out, err = run(args + [url], timeout=30)
    if rc != 0:
        return rc, 0, {}, err.strip()
    blocks = out.split("\n\n")  # text mode: CRLF arrives as LF
    i = 0
    while blocks[i].startswith("HTTP/1.1 1"):  # interim responses (100 Continue)
        i += 1
    head = blocks[i].split("\n")
    hdrs = {k.lower(): v.strip() for k, _, v in (h.partition(":") for h in head[1:])}
    return rc, int(head[0].split()[1]), hdrs, "\n\n".join(blocks[i + 1:])


def logs(name):
    rc, out, err = run(["docker", "logs", f"{P}-{name}"])
    recs = []
    for line in (out + "\n" + err).splitlines():
        try:
            recs.append(json.loads(line))
        except ValueError:
            pass
    return recs


def resolved(name, rid):
    """source.ip of the access-log record of request rid on Deed instance name."""
    for _ in range(20):
        for r in logs(name):
            if r.get("http.request.id") == rid and r.get("msg") == "http request":
                return r.get("source.ip")
        time.sleep(0.25)
    raise AssertionError(f"no access-log record for {rid} on {name}")


def probe(client, url, name, headers=None):
    """GET system info from client via url and return the client address Deed `name` resolved."""
    rc, status, hdrs, body = curl(client, url, headers)
    expect(rc == 0 and status == 200, f"GET {url} from {client}", {"rc": rc, "status": status, "body": body[:300]})
    return resolved(name, hdrs["x-request-id"])


def ip_table(cases):
    """cases: (label, client, url, deed, headers, want) → evidence rows; fails if any resolved address differs."""
    rows, bad = [], []
    for label, client, url, name, hdr, want in cases:
        got = probe(client, url, name, hdr)
        rows.append({"case": label, "client": client, "sent": hdr or {}, "deed": name, "resolved": got, "want": want})
        if got != want:
            bad.append(rows[-1])
    expect(not bad, "resolved client address differs", bad)
    return rows


INFO = "/api/v1/system/info"
SPOOF_HDR = {"X-Forwarded-For": SPOOF, "CF-Connecting-IP": SPOOF}


# ---- steps -----------------------------------------------------------------------------------------------------------

@step(1, "production configs accepted; effective proxy and TLS settings")
def s1():
    ev = {}
    for name in DEED:
        rc, out, err = run(["docker", "exec", f"{P}-{name}", "/usr/local/bin/deed", "config", "check"])
        expect(rc == 0, f"config check {name}", err[-500:])
        expect(re.search(r"^mode: production$", out, re.M) is not None, f"{name} not production")
        proxy = re.search(r"^proxy:\n((?:  .*\n|  - .*\n)+)", out, re.M)
        http = re.search(r"^http:\n((?:  .*\n)+)", out, re.M)
        p = " ".join(proxy.group(1).split()) if proxy else ""
        ranges = re.findall(r"- (\S+)", p.split("cloudflare_ranges:", 1)[-1])
        ev[name] = {"proxy": p.split("cloudflare_ranges:", 1)[0].strip(),
                    "cloudflare_ranges": ranges if len(ranges) < 3 else f"built-in ({len(ranges)})",
                    "http": " ".join(l.strip() for l in http.group(1).splitlines()
                                     if l.strip().split(":")[0] in ("listen", "tls_cert_file", "client_ca_file") and '""' not in l) if http else None}
    expect("client_ca_file: /etc/deed-validate/deed/origin-pull-ca.pem" in (ev["origin"]["http"] or ""), "origin client_ca_file", ev["origin"])
    start = [r for r in logs("origin") if r.get("event.action") == "server.start"]
    expect(start and start[-1].get("tls") is True and start[-1].get("client_cert_required") is True, "origin start record", start[-1:])
    ev["origin_start"] = {k: start[-1].get(k) for k in ("public", "tls", "client_cert_required", "public_url", "version")}
    return ev


@step(2, "AOP: TLS without a client certificate is refused (TLS 1.3 and 1.2)")
def s2():
    r13 = tls_get(None, minv=ssl.TLSVersion.TLSv1_3)
    r12 = tls_get(None, maxv=ssl.TLSVersion.TLSv1_2)
    expect(refused(r13) and refused(r12), "handshake without a client certificate completed", {"tls1.3": r13, "tls1.2": r12})
    return {"tls1.3": r13["error"], "tls1.2": r12["error"]}


@step(3, "AOP: a client certificate from another CA is refused; right CA without clientAuth usage is refused")
def s3():
    ev = {}
    for cert in ("rogue", "pull-serveronly"):
        for v, kw in (("tls1.3", {"minv": ssl.TLSVersion.TLSv1_3}), ("tls1.2", {"maxv": ssl.TLSVersion.TLSv1_2})):
            r = tls_get(cert, **kw)
            expect(refused(r), f"{cert} accepted over {v}", r)
            ev[f"{cert}/{v}"] = r["error"]
    return ev


@step(4, "AOP: the origin-pull client certificate is accepted; TLS 1.2/1.3 only")
def s4():
    r13 = tls_get("pull", minv=ssl.TLSVersion.TLSv1_3)
    r12 = tls_get("pull", minv=ssl.TLSVersion.TLSv1_2, maxv=ssl.TLSVersion.TLSv1_2)
    for r in (r13, r12):
        expect(r["ok"] and r["status"].startswith("HTTP/1.1 200"), "request with the origin-pull certificate", r)
    expect(r13["version"] == "TLSv1.3" and r12["version"] == "TLSv1.2", "negotiated versions", [r13.get("version"), r12.get("version")])
    old = {}
    for v in (ssl.TLSVersion.TLSv1, ssl.TLSVersion.TLSv1_1):
        r = tls_get("pull", minv=v, maxv=v, legacy=True)
        expect(not r["ok"], f"{v.name} accepted", r)
        old[v.name] = r["error"]
    # The refusal must come from the server (protocol_version alert), not from a local library that will not offer it.
    expect(any("PROTOCOL_VERSION" in e.upper() or "UNSUPPORTED_PROTOCOL" in e.upper() for e in old.values()), "legacy refusal reason", old)
    return {"tls1.3": [r13["version"], r13["cipher"], r13["status"]], "tls1.2": [r12["version"], r12["cipher"], r12["status"]],
            "legacy": old}


@step(5, "served certificate chain: leaf deed.example.com + intermediate, verifies to the origin root")
def s5():
    base = ["openssl", "s_client", "-connect", f"127.0.0.1:{PORT}", "-servername", HOST, "-CAfile", sec("origin-root.pem"),
            "-cert", sec("pull.pem"), "-key", sec("pull-key.pem"), "-showcerts"]
    rc, out, err = run(base + ["-verify_hostname", HOST, "-verify_return_error"], input="", timeout=30)
    chain = re.findall(r"^\s*(\d+) s:(.*)\n\s+i:(.*)$", out, re.M)
    expect(rc == 0 and "Verify return code: 0 (ok)" in out, "chain verification", (out + err)[-800:])
    expect(len(chain) == 2 and "deed.example.com" in chain[0][1] and "Intermediate" in chain[0][2] and "Root" in chain[1][2],
           "served chain", chain)
    rc2, out2, err2 = run(base + ["-verify_hostname", "other.example.com", "-verify_return_error"], input="", timeout=30)
    expect(rc2 != 0 and "hostname mismatch" in (out2 + err2), "hostname mismatch not detected", (out2 + err2)[-400:])
    leaf = tls_get("pull")
    return {"chain": [{"depth": int(d), "subject": s.strip(), "issuer": i.strip()} for d, s, i in chain],
            "verify": "0 (ok)", "san": leaf.get("peer", {}).get("san"), "wrong_name": "hostname mismatch"}


@step(6, "HSTS and security headers on the TLS origin")
def s6():
    r = tls_get("pull")
    h = r["headers"]
    want = {"strict-transport-security": "max-age=63072000; includeSubDomains", "x-content-type-options": "nosniff",
            "x-frame-options": "DENY", "referrer-policy": "no-referrer", "cache-control": "no-store"}
    bad = {k: h.get(k) for k, v in want.items() if h.get(k) != v}
    expect(not bad and "frame-ancestors 'none'" in h.get("content-security-policy", ""), "headers", {"bad": bad, "headers": h})
    ev = {k: h.get(k) for k in [*want, "content-security-policy", "cross-origin-opener-policy", "permissions-policy"]}
    # Recorded for docs/CLOUDFLARE.md (compose): a host process reaching a published port arrives from the network
    # gateway, so that address (not 127.0.0.1) is what proxy.trusted_proxies must name for a host-side proxy.
    gw = run(["docker", "network", "inspect", "-f", "{{(index .IPAM.Config 0).Gateway}}", P])[1].strip()
    ev["host_via_published_port"] = {"resolved": resolved("origin", h["x-request-id"]), "network_gateway": gw}
    return ev


@step(7, "edge → origin: Full (strict) + AOP succeeds; edge without the client cert and direct clients are refused")
def s7():
    rc, status, hdrs, body = curl(C1, f"http://{EDGE}:8006{INFO}")
    expect(rc == 0 and status == 200 and '"api_version":"v1"' in body.replace(" ", ""), "via edge with AOP", [rc, status, body[:200]])
    rc2, status2, _, body2 = curl(C1, f"http://{EDGE}:8007{INFO}")
    expect(rc2 == 0 and status2 == 502, "edge without client certificate", [rc2, status2, body2[:200]])
    edge_err = [l for l in run(["docker", "logs", f"{P}-edge"])[2].splitlines() if "SSL" in l or "certificate" in l][-1:]
    # A client inside the network that bypasses the edge: TLS to the origin without a certificate fails.
    rc3, _, _, err3 = curl(C1, f"https://{HOST}{INFO}", extra=("--resolve", f"{HOST}:443:{DEED['origin']}", "-k"))
    expect(rc3 != 0, "direct client without certificate got a response", err3)
    return {"edge_aop": [status, hdrs.get("strict-transport-security")], "edge_no_cert": status2,
            "edge_error_log": [l[l.find("SSL"):][:200] for l in edge_err], "direct_no_cert": {"curl_exit": rc3, "error": err3[-200:]},
            "resumption": resumption()}


def resumption():
    """Recorded, not asserted: a TLS session established with the origin-pull certificate is resumable without
    presenting it again (standard TLS; the ticket is only issued after an authenticated handshake)."""
    sess = sec("pull.session")
    req = f"GET {INFO} HTTP/1.1\r\nHost: {HOST}\r\nConnection: close\r\n\r\n"
    base = ["openssl", "s_client", "-connect", f"127.0.0.1:{PORT}", "-servername", HOST, "-CAfile", sec("origin-root.pem"), "-ign_eof"]
    run(base + ["-cert", sec("pull.pem"), "-key", sec("pull-key.pem"), "-sess_out", sess], input=req, timeout=30)
    if not os.path.exists(sess):
        return "no session saved"
    rc, out, err = run(base + ["-sess_in", sess], input=req, timeout=30)
    os.remove(sess)
    return {"reused": "Reused, TLS" in out, "status": next((l for l in out.splitlines() if l.startswith("HTTP/1.1")), None)}


@step(8, "(a) trusted proxy hop: forwarded address honoured; spoofed headers from the client are not")
def s8():
    return ip_table([
        ("via edge", C1, f"http://{EDGE}:8001{INFO}", "trusted", None, C1),
        ("via edge, client prepends X-Forwarded-For/CF-Connecting-IP", C1, f"http://{EDGE}:8001{INFO}", "trusted", SPOOF_HDR, C1),
        ("second client via edge", C2, f"http://{EDGE}:8001{INFO}", "trusted", None, C2),
        ("direct (bypassing the edge) with spoofed headers", C1, f"http://{DEED['trusted']}:8080{INFO}", "trusted", SPOOF_HDR, C1),
    ])


@step(9, "(b) untrusted hop: X-Forwarded-For and CF-Connecting-IP ignored")
def s9():
    return ip_table([
        ("via edge (edge not trusted)", C1, f"http://{EDGE}:8002{INFO}", "untrusted", None, EDGE),
        ("via edge with spoofed headers", C1, f"http://{EDGE}:8002{INFO}", "untrusted", SPOOF_HDR, EDGE),
        ("direct with spoofed headers", C1, f"http://{DEED['untrusted']}:8080{INFO}", "untrusted", SPOOF_HDR, C1),
    ])


@step(10, "(c) CF-Connecting-IP only from a Cloudflare-range hop, even with proxy.cloudflare: true")
def s10():
    return ip_table([
        ("built-in ranges: edge (not Cloudflare) sets CF-Connecting-IP", C1, f"http://{EDGE}:8003{INFO}", "cloudflare", None, EDGE),
        ("built-in ranges: direct client sends CF-Connecting-IP", C1, f"http://{DEED['cloudflare']}:8080{INFO}", "cloudflare",
         {"CF-Connecting-IP": SPOOF}, C1),
        ("cloudflare_ranges=[edge]: edge sets CF-Connecting-IP (over TLS+AOP)", C1, f"http://{EDGE}:8006{INFO}", "origin", None, C1),
        ("cloudflare_ranges=[edge]: client spoofs, edge overwrites", C1, f"http://{EDGE}:8006{INFO}", "origin", SPOOF_HDR, C1),
        ("cloudflare_ranges=[edge]: second client", C2, f"http://{EDGE}:8006{INFO}", "origin", None, C2),
    ])


@step(11, "local proxy / tunnel shape (trusted_proxies=[proxy], cloudflare: true): X-Forwarded-For decides, "
          "CF-Connecting-IP from the trusted non-Cloudflare hop is not used")
def s11():
    return ip_table([
        ("local proxy forwards X-Forwarded-For + CF-Connecting-IP", C1, f"http://{EDGE}:8004{INFO}", "localproxy", None, C1),
        ("same, client spoofs both headers", C1, f"http://{EDGE}:8004{INFO}", "localproxy", SPOOF_HDR, C1),
        ("local proxy sends CF-Connecting-IP only", C1, f"http://{EDGE}:8005{INFO}", "localproxy", None, EDGE),
    ])


def hammer(client, url, tag, spoof=True, attempts=14):
    """Failed logins (distinct unknown accounts, so only the per-address throttles apply) until the first 429.
    With spoof, every attempt claims a different RFC 5737 address in X-Forwarded-For and CF-Connecting-IP."""
    seq = []
    for i in range(1, attempts + 1):
        hdr = {"X-Forwarded-For": f"198.51.100.{i}", "CF-Connecting-IP": f"198.51.100.{i}"} if spoof else {}
        rc, status, hdrs, body = curl(client, url, hdr, "POST", {"email": f"probe-{tag}-{i}@example.com", "password": "not-the-password-1"})
        seq.append(status)
        if status == 429:
            expect('"rate_limited"' in body.replace(" ", ""), "429 problem code", body[:300])
            return seq
    raise AssertionError(f"{client} via {url}: no 429 after {attempts} failed logins with rotating spoofed addresses: {seq}")


def login_audit(name):
    return sorted({r.get("source", {}).get("ip") for r in logs(name)
                   if r.get("event", {}).get("dataset") == "deed.audit" and r["event"].get("action") == "auth.login"})


LOGIN = "/api/v1/auth/login"


@step(12, "login throttling through the Cloudflare-shaped edge keys on the real client, not on spoofed headers")
def s12():
    seq1 = hammer(C1, f"http://{EDGE}:8006{LOGIN}", "cf1")
    rc, status, _, body = curl(C2, f"http://{EDGE}:8006{LOGIN}", None, "POST", {"email": "probe-cf2@example.com", "password": "not-the-password-1"})
    expect(status == 401, "second client behind the same edge must not share the throttle", [status, body[:200]])
    ips = login_audit("origin")
    expect(ips == sorted([C1, C2]), "audit source.ip of failed logins", ips)
    return {"client1_statuses": seq1, "client2_status": status, "audit_source_ips": ips}


@step(13, "login throttling cannot be evaded with spoofed headers (trusted-proxy, direct bypass, Cloudflare mode)")
def s13():
    ev = {"trusted via edge (client1)": hammer(C1, f"http://{EDGE}:8001{LOGIN}", "tr1"),
          "trusted, direct bypass (client2)": hammer(C2, f"http://{DEED['trusted']}:8080{LOGIN}", "tr2"),
          "cloudflare mode, direct (client1)": hammer(C1, f"http://{DEED['cloudflare']}:8080{LOGIN}", "cf")}
    ev["audit_trusted"], ev["audit_cloudflare"] = login_audit("trusted"), login_audit("cloudflare")
    expect(ev["audit_trusted"] == sorted([C1, C2]) and ev["audit_cloudflare"] == [C1], "audit source.ip", ev)
    return ev


@step(14, "misconfiguration made visible: an untrusted proxy collapses every client onto the proxy's address")
def s14():
    seq1 = hammer(C1, f"http://{EDGE}:8002{LOGIN}", "un1", spoof=False)
    rc, status, _, _ = curl(C2, f"http://{EDGE}:8002{LOGIN}", None, "POST", {"email": "probe-un2@example.com", "password": "not-the-password-1"})
    expect(status == 429, "second client should share the edge's throttle", status)
    ips = login_audit("untrusted")
    expect(ips == [EDGE], "audit source.ip", ips)
    return {"client1_statuses": seq1, "client2_status": status, "audit_source_ips": ips}


def doc_block(lang, after=None):
    text = open(os.path.join(ROOT, "docs", "CLOUDFLARE.md")).read()
    if after:
        text = text[text.index(after):]
    m = re.search(r"^```" + lang + r"\n(.*?)^```$", text, re.M | re.S)
    expect(m is not None, f"```{lang} block in docs/CLOUDFLARE.md")
    return m.group(1)


def builtin_ranges():
    src = open(os.path.join(ROOT, "internal", "config", "validate.go")).read()
    body = src[src.index("var CloudflareIPRanges"):]
    body = body[:body.index("}")]
    return re.findall(r'"([0-9a-f:.]+/\d+)"', body)


@step(15, "docs: the nftables ruleset passes nft -c; its sets equal Deed's built-in Cloudflare ranges")
def s15():
    nft = doc_block("nft")
    rc, out, err = run(["docker", "run", "--rm", "-i", "--network", "none", "--cap-add", "NET_ADMIN", FW_IMAGE, "sh", "-c",
                        "cat >/tmp/deed.nft && nft --version && nft -c -f /tmp/deed.nft && echo nft-check-ok"], input=nft)
    expect(rc == 0 and "nft-check-ok" in out, "nft -c", (out + err)[-800:])
    sets = {}
    for name in ("cloudflare4", "cloudflare6"):
        m = re.search(r"set " + name + r" \{[^}]*elements = \{([^}]*)\}", nft, re.S)
        sets[name] = [x.strip() for x in m.group(1).split(",") if x.strip()]
    ranges = builtin_ranges()
    expect(sorted(sets["cloudflare4"] + sets["cloudflare6"]) == sorted(ranges), "nft sets vs config.CloudflareIPRanges",
           {"nft": sets, "builtin": ranges})
    v = [ipaddress.ip_network(r) for r in ranges]
    return {"nft": out.split("\n")[0], "ipv4": len(sets["cloudflare4"]), "ipv6": len(sets["cloudflare6"]),
            "builtin_equal": True, "overlaps": [str(a) for a in v for b in v if a != b and a.version == b.version and a.overlaps(b)]}


@step(16, "docs: the UFW snippet runs (ufw --dry-run) and allows 443 only from every Cloudflare range")
def s16():
    snippet = doc_block("sh", "### UFW")
    ranges = builtin_ranges()
    v4 = "\n".join(r for r in ranges if ":" not in r)
    v6 = "\n".join(r for r in ranges if ":" in r)
    urls = re.findall(r"curl -fsS (\S+)\)", snippet)
    expect(urls == ["https://www.cloudflare.com/ips-v4", "https://www.cloudflare.com/ips-v6"], "documented range sources", urls)
    # curl is replaced by the built-in list (no network); ufw runs with --dry-run in a throwaway namespace.
    shim = ("set -u\ncurl() { case \"$*\" in\n  *ips-v4) printf '%s\\n' \"$V4\";;\n  *ips-v6) printf '%s\\n' \"$V6\";;\n"
            "  *) return 22;; esac; }\nufw() { command ufw --dry-run \"$@\"; }\n" + snippet + "\necho \"snippet-exit=$?\"\n")
    rc, out, err = run(["docker", "run", "--rm", "-i", "--network", "none", "--cap-add", "NET_ADMIN", "-e", f"V4={v4}", "-e", f"V6={v6}",
                        FW_IMAGE, "bash", "-s"], input=shim, timeout=120)
    expect(rc == 0 and "snippet-exit=0" in out, "UFW snippet", (out + err)[-800:])
    allowed = re.findall(r"^-A ufw6?-user-input -p tcp --dport 443 -s (\S+) -j ACCEPT$", out, re.M)
    ssh = re.findall(r"^-A ufw-user-input -p tcp --dport 22 -s (\S+) -j ACCEPT$", out, re.M)
    expect(sorted(allowed) == sorted(ranges), "443 rules vs ranges", {"allowed": allowed})
    expect(ssh == ["192.0.2.0/24"] and "Default incoming policy changed to 'deny'" in out and "Firewall is active" in out, "policy/ssh/enable", ssh)
    return {"sources": urls, "allow_443": len(allowed), "ssh_from": ssh, "default_incoming": "deny", "enabled": True}


@step(17, "docs: the nftables ruleset loaded into the origin's network namespace blocks bypass (edge address added to "
          "cloudflare4 as the Cloudflare stand-in)")
def s17():
    origin = DEED["origin"]
    before = {"client→origin:9464": curl(C1, f"http://{origin}:9464/healthz")[1]}
    nft = doc_block("nft")
    fw = ["docker", "run", "--rm", "-i", "--network", f"container:{P}-origin", "--cap-add", "NET_ADMIN", FW_IMAGE]
    rc, out, err = run(fw + ["sh", "-c", "cat >/tmp/deed.nft && nft -f /tmp/deed.nft && nft add element inet deed cloudflare4 '{ "
                             + EDGE + " }' && nft list chain inet deed input"], input=nft)
    expect(rc == 0, "load ruleset", (out + err)[-800:])
    after = {}
    rc1, s1_, _, _ = curl(C1, f"http://{EDGE}:8006{INFO}")
    after["client→edge→origin:443"] = s1_
    rc2, _, _, e2 = curl(C1, f"https://{HOST}{INFO}", extra=("--resolve", f"{HOST}:443:{origin}", "-k"))
    after["client→origin:443 (direct)"] = f"curl exit {rc2}"
    rc3, _, _, e3 = curl(C1, f"http://{origin}:9464/healthz")
    after["client→origin:9464"] = f"curl exit {rc3}"
    rc4, out4, err4 = run(["docker", "exec", f"{P}-edge", "curl", "-sS", "--connect-timeout", "3", f"http://{origin}:9464/healthz"])
    after["edge→origin:9464"] = f"curl exit {rc4}"
    rc5, out5, _ = run(["docker", "exec", f"{P}-origin", "/usr/local/bin/deed", "healthcheck"])
    after["origin loopback healthcheck"] = out5.strip() or f"exit {rc5}"
    expect(before["client→origin:9464"] == 200, "management port reachable before the firewall (control)", before)
    expect(s1_ == 200 and rc2 == 28 and rc3 == 28 and rc4 == 28 and rc5 == 0, "filtering", after)
    return {"before": before, "after": after, "chain": [l.strip() for l in out.splitlines() if "dport" in l or "policy" in l]}


CLOUDFLARE_ONLY = [
    "requests really arriving from Cloudflare's published ranges (here the nginx edge played Cloudflare through "
    "proxy.cloudflare_ranges = [edge]); whether the built-in list is current",
    "Cloudflare setting/overwriting CF-Connecting-IP and appending X-Forwarded-For as assumed (nginx did it here)",
    "Cloudflare presenting its origin-pull certificate when AOP is enabled, that it chains to the published "
    "origin-pull CA and passes Go's clientAuth verification (a throwaway CA stood in)",
    "zone-level (shared certificate) versus per-hostname/custom-certificate AOP",
    "Full (strict) validation of a real Cloudflare Origin CA certificate by Cloudflare (nginx verified a throwaway chain)",
    "cloudflared (Tunnel) forwarding X-Forwarded-For with the visitor address (the local-proxy shape was tested with nginx)",
    "edge behaviour: /api/* cache bypass, SSE through the 100 s proxy read timeout, WAF exclusions, Cloudflare Access, "
    "Origin Rules for a non-443 origin port",
    "the firewall on a real host with real Cloudflare traffic, IPv6 (only syntax-checked here), Docker's DOCKER-USER "
    "interaction on the host, UFW applying rules for real (--dry-run here) and the live range downloads",
]


def main():
    for fn in (s1, s2, s3, s4, s5, s6, s7, s8, s9, s10, s11, s12, s13, s14, s15, s16, s17):
        fn()
    rev = run(["docker", "image", "inspect", "-f", '{{index .Config.Labels "org.opencontainers.image.revision"}}', os.environ["IMAGE"]])[1].strip()
    edge = run(["docker", "image", "inspect", "-f", "{{index .RepoDigests 0}}", os.environ["EDGE_IMAGE"]])[1].strip()
    json.dump({"results": results, "image": os.environ["IMAGE"], "image_revision": rev, "edge_image": edge,
               "tree": run(["git", "-C", ROOT, "rev-parse", "HEAD"])[1].strip(), "network": f"{SUB}.0/24", "cloudflare_only": CLOUDFLARE_ONLY,
               "finished": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}, open(os.path.join(VAL, "results.json"), "w"), indent=1)
    failed = [r["step"] for r in results if not r["ok"]]
    print("\nNot validated here (needs a real Cloudflare zone):\n" + "\n".join("  - " + c for c in CLOUDFLARE_ONLY))
    print(f"\n{len(results) - len(failed)}/{len(results)} steps passed" + (f"; failed: {failed}" if failed else ""))
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
