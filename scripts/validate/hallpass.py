#!/usr/bin/env python3
"""Driver for scripts/validate/hallpass.sh (stdlib only; run with python3 -I).

Seeds a local HallPass through its operator API (administrator session and API tokens) and agent SSH commands, then
validates Deed's hallpass source connector against it: connectivity test, first sync compared with what HallPass
returns to the same auditor token, a second sync after removals in HallPass (absence handling), and a test with a
wrong token. Every step prints PASS/FAIL with evidence; results.json in VALIDATE_DIR records them. All names are
example.com; credentials are generated per run and stay in VALIDATE_DIR."""
import base64
import hashlib
import hmac
import http.cookiejar
import json
import os
import struct
import subprocess
import sys
import time
import urllib.error
import urllib.request

WORK = os.environ["VALIDATE_DIR"]
HP_URL = os.environ["HP_URL"]
HP_SSH = os.environ["HP_SSH"]
DEED_URL = os.environ["DEED_URL_"]
DEST = os.environ.get("VALIDATE_DEST") == "1"
DEST_CONTAINER = os.environ.get("HP_DEST_CONTAINER", "")
DEST_PORT = int(os.environ.get("HP_DEST_PORT", "0") or 0)
ADMIN_EMAIL = "admin@example.com"
results = []
S = {}  # state shared between steps


class Client:
    """JSON over HTTP with a cookie jar; an optional bearer token; CSRF header names differ per product."""

    def __init__(self, base, bearer=None, csrf_header=None):
        self.base, self.bearer, self.csrf_header, self.csrf = base, bearer, csrf_header, ""
        self.op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def call(self, method, path, body=None, expect=None):
        h = {"Accept": "application/json", "Origin": self.base}
        data = None
        if body is not None:
            data = json.dumps(body).encode()
            h["Content-Type"] = "application/json"
        if self.csrf and self.csrf_header:
            h[self.csrf_header] = self.csrf
        if self.bearer:
            h["Authorization"] = "Bearer " + self.bearer
        req = urllib.request.Request(self.base + "/api/v1" + path, data=data, method=method, headers=h)
        try:
            resp = self.op.open(req, timeout=120)
            code, raw = resp.status, resp.read()
        except urllib.error.HTTPError as e:
            code, raw = e.code, e.read()
        js = json.loads(raw) if raw[:1] in (b"{", b"[") else raw.decode(errors="replace")
        if expect is not None and code not in (expect if isinstance(expect, tuple) else (expect,)):
            raise AssertionError(f"{method} {path}: HTTP {code} (want {expect}): {str(js)[:500]}")
        return code, js

    def get(self, path):
        return self.call("GET", path, expect=200)[1]

    def post(self, path, body=None, expect=(200, 201)):
        return self.call("POST", path, body if body is not None else {}, expect=expect)[1]


def totp(secret, at=None):
    k = base64.b32decode(secret.upper() + "=" * (-len(secret) % 8))
    m = hmac.new(k, struct.pack(">Q", int((at if at is not None else time.time()) // 30)), hashlib.sha1).digest()
    o = m[-1] & 15
    return "%06d" % ((struct.unpack(">I", m[o:o + 4])[0] & 0x7FFFFFFF) % 1000000)


def step(n, title):
    def deco(fn):
        def wrapped(*a, **kw):
            t0 = time.time()
            try:
                ev = fn(*a, **kw) or {}
                results.append({"step": n, "title": title, "ok": True, "evidence": ev, "seconds": round(time.time() - t0, 1)})
                print(f"PASS {n:>2}. {title}\n        {json.dumps(ev, sort_keys=True)[:1500]}", flush=True)
                return ev
            except Exception as ex:  # noqa: BLE001 — every failure is reported; later steps run where possible
                results.append({"step": n, "title": title, "ok": False, "error": str(ex)[:2000], "seconds": round(time.time() - t0, 1)})
                print(f"FAIL {n:>2}. {title}\n        {ex}", flush=True)
                return None
        return wrapped
    return deco


def ssh_agent(agent, command, background=False):
    """Run a HallPass gateway command as an agent (pinned gateway host key, no user ssh config)."""
    cmd = ["ssh", "-F", "none", "-p", HP_SSH, "-i", os.path.join(WORK, f"agent-{agent}"), "-o", "IdentitiesOnly=yes",
           "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", f"UserKnownHostsFile={os.path.join(WORK, 'known_hosts')}",
           "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "ServerAliveInterval=30", "agent@127.0.0.1", command]
    if background:
        log = open(os.path.join(WORK, f"ssh-{agent}.log"), "w")
        return subprocess.Popen(cmd, stdin=subprocess.DEVNULL, stdout=log, stderr=log)
    p = subprocess.run(cmd, capture_output=True, text=True, timeout=180, stdin=subprocess.DEVNULL)
    if p.returncode != 0:
        raise AssertionError(f"ssh {agent} {command!r}: exit {p.returncode}: {p.stderr.strip()[-600:]}")
    return p.stdout


def docker(*args):
    return subprocess.run(["docker", *args], check=True, capture_output=True, text=True).stdout.strip()


def wait_for(fn, what, timeout=60, every=0.5):
    deadline, last = time.time() + timeout, None
    while time.time() < deadline:
        try:
            v = fn()
            if v:
                return v
        except Exception as ex:  # noqa: BLE001 — retried until the deadline
            last = ex
        time.sleep(every)
    raise AssertionError(f"timed out waiting for {what}" + (f" ({last})" if last else ""))


# ---------------------------------------------------------------------------------------------------------------- HallPass

def hp_pages(c, path):
    """Every item of a cursor-paged HallPass list."""
    items, cursor = [], None
    sep = "&" if "?" in path else "?"
    while True:
        page = c.get(path + sep + "limit=200" + (f"&cursor={urllib.request.quote(cursor)}" if cursor else ""))
        items += page["items"]
        cursor = page.get("next_cursor")
        if not cursor:
            return items


@step(1, "HallPass: first administrator (bootstrap, password, TOTP) and API tokens (administrator, auditor)")
def hp_admin():
    hp = Client(HP_URL, csrf_header="X-CSRF-Token")
    token = open(os.path.join(WORK, "hp-setup-token")).read().strip()
    password = base64.b32encode(os.urandom(15)).decode()
    hp.post("/auth/bootstrap", {"token": token, "email": ADMIN_EMAIL, "display_name": "Validation Admin", "password": password})
    login = hp.post("/auth/login", {"email": ADMIN_EMAIL, "password": password})
    hp.csrf = login["me"]["csrf_token"]
    secret = hp.post("/me/mfa/totp")["secret"]
    hp.post("/me/mfa/totp/confirm", {"code": totp(secret)})
    time.sleep(31 - time.time() % 30)  # a TOTP step is single-use: wait for the next one
    me = hp.post("/auth/mfa", {"code": totp(secret)})
    hp.csrf = me["csrf_token"]
    op = me["operator"]
    admin = hp.post("/api-tokens", {"name": "validate-admin", "role": "administrator", "expires_in_days": 1})
    # API tokens carry one of the caller's own roles: hold auditor too, so a read-only token can be minted. A role
    # change ends the operator's web sessions, so sign in again (with the next TOTP step) before minting it.
    op = hp.call("PATCH", f"/operators/{op['id']}", {"roles": ["administrator", "auditor"]}, expect=200)[1]
    hp = Client(HP_URL, csrf_header="X-CSRF-Token")
    hp.csrf = hp.post("/auth/login", {"email": ADMIN_EMAIL, "password": password})["me"]["csrf_token"]
    time.sleep(31 - time.time() % 30)
    hp.csrf = hp.post("/auth/mfa", {"code": totp(secret)})["csrf_token"]
    auditor = hp.post("/api-tokens", {"name": "deed-auditor", "role": "auditor", "expires_in_days": 1})
    S.update(hp=hp, hp_admin=Client(HP_URL, bearer=admin["token"]), hp_auditor=Client(HP_URL, bearer=auditor["token"]),
             auditor_token=auditor["token"], hp_operator=op["id"])
    for name, value in (("hp-admin-password", password), ("hp-totp-secret", secret), ("hp-auditor-token", auditor["token"])):
        path = os.path.join(WORK, name)
        with open(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600), "w") as f:
            f.write(value)
    st = S["hp_auditor"].get("/system/status")
    return {"operator_roles": op["roles"], "admin_token_role": admin["api_token"]["role"], "auditor_token_role": auditor["api_token"]["role"],
            "auditor_token_prefix": auditor["token"][:12], "version": st.get("version"), "status_keys": sorted(st)}


@step(2, "HallPass: agents, servers, roles, auto-approve policy (and a destination login key when docker is used)")
def hp_inventory():
    a = S["hp_admin"]

    def pub(name):
        return open(os.path.join(WORK, f"agent-{name}.pub")).read().strip()

    agents = {
        "ci-runner": a.post("/agents", {"name": "ci-runner", "description": "CI deploy agent (example.com)", "groups": ["ci"],
                                        "labels": {"host": "builder-1", "team": "platform"}, "public_keys": [pub("ci-runner")]}),
        "review-bot": a.post("/agents", {"name": "review-bot", "groups": ["ci"], "public_keys": [pub("review-bot")]}),
        "temp-bot": a.post("/agents", {"name": "temp-bot", "groups": ["ci"], "public_keys": [pub("temp-bot")]}),
        "retired-bot": a.post("/agents", {"name": "retired-bot", "enabled": False, "groups": ["ci"]}),
    }
    web_port = DEST_PORT if DEST else 22
    servers = {
        "web-1": a.post("/servers", {"alias": "web-1", "display_name": "Web 1", "description": "frontend.example.com", "environment": "production",
                                     "address": "127.0.0.1", "port": web_port, "groups": ["web"], "labels": {"tier": "frontend"}}),
        "db-1": a.post("/servers", {"alias": "db-1", "description": "db.example.com", "environment": "staging", "address": "127.0.0.2", "port": 22}),
        "spare-1": a.post("/servers", {"alias": "spare-1", "address": "127.0.0.3", "port": 22, "enabled": False}),
    }
    roles = {}
    for srv, alias, user, caps in (("web-1", "inspect", "ro", ["shell", "exec"]), ("web-1", "deploy", "deploy", ["shell", "exec"]),
                                   ("db-1", "dba", "postgres", ["exec"])):
        roles[srv + "/" + alias] = a.post(f"/servers/{servers[srv]['id']}/roles", {"alias": alias, "remote_user": user, "capabilities": caps,
                                                                                    "default_duration": 1800, "max_duration": 3600})
    ev = {}
    if DEST:
        # Pin the destination host key and install a generated login key for web-1/deploy, as an operator would.
        a.post(f"/servers/{servers['web-1']['id']}/host-identities",
               {"public_key": docker("exec", DEST_CONTAINER, "cat", "/etc/ssh/ssh_host_ed25519_key.pub")})
        cred = a.post(f"/roles/{roles['web-1/deploy']['id']}/credentials:generate")
        docker("exec", DEST_CONTAINER, "sh", "-c", f"printf '%s\\n' '{cred['authorized_keys_line']}' >> /home/deploy/.ssh/authorized_keys")
        probed = a.post(f"/credentials/{cred['id']}/probe")
        if probed.get("probe_error"):
            raise AssertionError(f"credential probe failed: {probed['probe_error']}")
        a.post(f"/credentials/{cred['id']}/activate")
        ev["destination"] = f"127.0.0.1:{DEST_PORT} (container {DEST_CONTAINER})"
    a.post("/policy/rules", {"name": "ci-auto", "effect": "allow", "level": "auto_approve", "enabled": True, "require_reason": False,
                             "subjects": {"agent_groups": ["ci"]}, "targets": {"all": True}, "roles": {"all": True},
                             "auto_max_duration": 3600, "max_duration": 3600})
    S.update(agents=agents, servers=servers, roles=roles)
    ev.update(agents={k: v["id"] for k, v in agents.items()}, servers={k: v["id"] for k, v in servers.items()},
              roles={k: v["id"] for k, v in roles.items()})
    return ev


@step(3, "HallPass: grants requested over agent SSH (auto-approved) and an open access session")
def hp_access():
    ev = {}
    # ci-runner's inspect grant (1 h) outlives its later deploy grant (30 min, the role default): two grants of one
    # agent on one server.
    for agent, srv, role, dur in (("ci-runner", "web-1", "inspect", "1h"), ("review-bot", "db-1", "dba", "30m"), ("temp-bot", "web-1", "inspect", "30m")):
        out = json.loads(ssh_agent(agent, f"request {srv} --role {role} --duration {dur} --wait --json"))
        ev[f"{agent}->{srv}/{role}"] = out
    if DEST:
        # A held-open exec over the gateway to the destination container (a live channel while Deed syncs).
        S["session_proc"] = ssh_agent("ci-runner", "exec web-1 --role deploy -- sleep 300", background=True)
        live = wait_for(lambda: S["hp_auditor"].get("/system/status")["live_channels"] >= 1, "a live channel")
        ev["live_channels"] = live and S["hp_auditor"].get("/system/status")["live_channels"]
    else:
        out = json.loads(ssh_agent("ci-runner", "request web-1 --role deploy --wait --json"))
        ev["ci-runner->web-1/deploy"] = out
    grants = wait_for(lambda: (lambda g: g if len(g) >= 4 else None)(hp_pages(S["hp_auditor"], "/grants?state=active")), "4 active grants")
    sess = hp_pages(S["hp_auditor"], "/access-sessions?state=open")
    # HallPass opens one access session per grant when the grant activates (HP-SES-001); the list has no channel counts.
    ev["active_grants"] = len(grants)
    ev["open_sessions"] = [{k: s.get(k) for k in ("id", "grant_id", "state", "remote_user", "channels_live")} for s in sess]
    ev["session_grants_equal_active_grants"] = sorted(s["grant_id"] for s in sess) == sorted(g["id"] for g in grants)
    detail = S["hp_auditor"].get(f"/access-sessions/{sess[0]['id']}")
    ev["session_detail_channels"] = {k: detail.get(k) for k in ("channels_total", "channels_live")}
    return ev


def hp_truth():
    """What HallPass returns to the auditor token, reduced to what the connector should import."""
    c = S["hp_auditor"]
    agents = c.get("/agents")["items"]
    servers = c.get("/servers")["items"]
    roles = {s["id"]: c.get(f"/servers/{s['id']}/roles")["items"] for s in servers if s["enabled"] and not s.get("deleted_at")}
    grants = hp_pages(c, "/grants?state=active")
    sessions = hp_pages(c, "/access-sessions?state=open") + hp_pages(c, "/access-sessions?state=closing")
    return {"agents": agents, "servers": servers, "roles": roles, "grants": grants, "sessions": sessions, "status": c.get("/system/status")}


def slug(s):
    """Mirror of the connectors' slug(): keep [a-z0-9._-], map the rest to '-', trim, collapse '--'."""
    out = "".join(ch if ("a" <= ch <= "z" or "0" <= ch <= "9" or ch in "._-") else "-" for ch in s.lower()).strip("-._")
    while "--" in out:
        out = out.replace("--", "-")
    return out[:200]


def expected(t):
    """Entity keys (kind/name) and edges (type from -> to, by name) the connector should produce for this truth."""
    ents, edges = {"service/hallpass"}, set()
    agent, server, role = {}, {}, {}
    for a in t["agents"]:
        if a.get("deleted_at") or not a["enabled"]:
            continue
        agent[a["id"]] = slug(a["name"])
        ents.add("hallpass_agent/" + agent[a["id"]])
        edges.add(f"protected_by {agent[a['id']]} -> hallpass")
        host = (a.get("labels") or {}).get("host")
        if host:
            box = slug(host + "-" + a["name"])
            ents.add("agent_box/" + box)
            edges.add(f"runs_on {agent[a['id']]} -> {box}")
    for s in t["servers"]:
        if s.get("deleted_at") or not s["enabled"]:
            continue
        server[s["id"]] = slug(s["alias"])
        ents.add("hallpass_server/" + server[s["id"]])
        edges.add(f"protected_by {server[s['id']]} -> hallpass")
        for r in t["roles"].get(s["id"], []):
            if r.get("deleted_at") or not r["enabled"]:
                continue
            role[r["id"]] = slug(s["alias"] + "-" + r["alias"])
            ents.add("hallpass_role/" + role[r["id"]])
            edges.add(f"contains {server[s['id']]} -> {role[r['id']]}")
    for g in t["grants"]:
        if g["state"] != "active" or g["agent_id"] not in agent or g["server_id"] not in server:
            continue
        gn = slug("grant-" + g["id"])
        ents.add("hallpass_grant/" + gn)
        edges.add(f"has_access_to {agent[g['agent_id']]} -> {server[g['server_id']]}")
        edges.add(f"references {gn} -> {agent[g['agent_id']]}")
        edges.add(f"has_access_to {gn} -> {role.get(g['role_id'], server[g['server_id']])}")
    for s in t["sessions"]:
        if s["agent_id"] not in agent or s["server_id"] not in server:
            continue
        sn = slug("session-" + s["id"])
        ents.add("agent_session/" + sn)
        edges.add(f"references {sn} -> {agent[s['agent_id']]}")
        edges.add(f"connects_to {sn} -> {server[s['server_id']]}")
    return ents, edges


# ---------------------------------------------------------------------------------------------------------------- Deed

def deed_all(path):
    items, cursor = [], None
    while True:
        page = S["deed"].get(path + "&limit=500" + (f"&cursor={urllib.request.quote(cursor)}" if cursor else ""))
        items += page["items"]
        cursor = page.get("next_cursor")
        if not cursor:
            return items


def deed_state(src_id):
    """Live entities and relations the source asserts in project home."""
    return deed_all(f"/entities?project=home&source={src_id}"), deed_all(f"/relations?project=home&source={src_id}")


def keyset(ents, rels):
    return ({e["kind"] + "/" + e["metadata"]["name"] for e in ents},
            {f"{r['type']} {r['from']['name']} -> {r['to']['name']}" for r in rels})


@step(4, "Deed: first owner (bootstrap link, password, TOTP), project and admin API token")
def deed_setup():
    owner = Client(DEED_URL, csrf_header="X-Deed-CSRF")
    tok = open(os.path.join(WORK, "deed-setup-token")).read().strip()
    password = base64.b32encode(os.urandom(15)).decode()
    me = owner.post("/auth/setup/complete", {"token": tok, "email": "owner@example.com", "display_name": "Validation Owner", "password": password},
                    expect=200)
    owner.csrf = me["csrf_token"]
    enr = owner.post("/me/mfa/totp", expect=200)
    owner.post("/me/mfa/totp/confirm", {"code": totp(enr["secret"])}, expect=200)
    owner.csrf = owner.get("/me")["csrf_token"]
    owner.post("/projects", {"name": "home", "display_name": "Home (validation)"}, expect=201)
    t = owner.post("/me/tokens", {"name": "validate", "installation_role": "admin"}, expect=201)
    S["deed"] = Client(DEED_URL, bearer=t["token"])
    info = S["deed"].get("/system/info")
    return {"deed_version": info.get("version"), "project": "home"}


@step(5, "Deed: create the hallpass source (auditor token, loopback allow-list, insecure http) and run its test")
def deed_source():
    d = S["deed"]
    cfg = {"url": HP_URL, "allow_cidrs": ["127.0.0.1/32"], "insecure_http": True}
    src = d.post("/sources", {"project": "home", "name": "hallpass", "provider": "hallpass", "config": cfg, "schedule_seconds": 0,
                              "secrets": {"token": S["auditor_token"]}}, expect=201)
    S["src"] = src["id"]
    test = d.post(f"/sources/{src['id']}/test", expect=200)
    assert test["ok"], f"test not ok: {test}"
    return {"source": src["id"], "policy": src.get("policy"), "secret_fields_set": src.get("secret_fields_set"), "test": test["diagnostics"]}


def sync():
    out = S["deed"].post(f"/sources/{S['src']}/sync", expect=200)
    run = out["run"]
    return run, out.get("result") or {}


def compare(t, ents, rels):
    want_e, want_r = expected(t)
    got_e, got_r = keyset(ents, rels)
    return {"missing_entities": sorted(want_e - got_e), "unexpected_entities": sorted(got_e - want_e),
            "missing_edges": sorted(want_r - got_r), "unexpected_edges": sorted(got_r - want_r)}, want_e, want_r


@step(6, "Deed: first sync matches what HallPass holds (entities, edges, fields, expiry)")
def first_sync():
    run, res = sync()
    assert run["result"] == "ok", f"run: {json.dumps(run)[:800]}"
    t = hp_truth()
    ents, rels = deed_state(S["src"])
    diff, want_e, want_r = compare(t, ents, rels)
    by = {e["kind"] + "/" + e["metadata"]["name"]: e for e in ents}
    checks = {}
    ci = by.get("hallpass_agent/ci-runner")
    checks["ci_runner_labels"] = ci and ci["metadata"]["labels"]
    web = by.get("hallpass_server/web-1")
    checks["web_1_spec"] = web and {k: web["spec"].get(k) for k in ("address", "port", "alias", "kind", "enabled")}
    checks["web_1_environment"] = web and web["metadata"].get("environment")
    inst = by.get("service/hallpass")
    checks["instance_status"] = inst and inst.get("status", {}).get("health")
    grants = [e for e in ents if e["kind"] == "hallpass_grant"]
    hp_exp = {g["id"]: g["expires_at"] for g in t["grants"]}
    checks["grant_expiry_matches"] = all(e["spec"].get("expires_at", "")[:19] == hp_exp.get(e["spec"].get("grant_id"), "")[:19] for e in grants)
    sess = [e for e in ents if e["kind"] == "agent_session"]
    checks["sessions"] = [{k: e["spec"].get(k) for k in ("status", "remote_user", "grant_id")} for e in sess]
    checks["session_observed"] = sorted({json.dumps(e["status"].get("observed") or {}) for e in sess})
    # ci-runner holds two grants on web-1: one has_access_to edge, lasting until the later of the two expires.
    ci_id, web_id = S["agents"]["ci-runner"]["id"], S["servers"]["web-1"]["id"]
    ci_grants = [g for g in t["grants"] if g["agent_id"] == ci_id and g["server_id"] == web_id]
    edge = [r for r in rels if r["type"] == "has_access_to" and r["from"]["name"] == "ci-runner" and r["to"]["name"] == "web-1"]
    detail = S["deed"].get(f"/relations/{edge[0]['id']}") if len(edge) == 1 else {}
    asr = [a for a in detail.get("assertions", []) if a.get("source_id") == S["src"]]
    checks["ci_runner_web_1_access"] = {"hallpass_grants": {g["id"]: g["expires_at"] for g in ci_grants},
                                        "edge_expires_at": asr and asr[0].get("expires_at"), "edge_evidence": asr and asr[0].get("evidence")}
    latest = max((g["expires_at"] for g in ci_grants), default="")
    access_ok = len(ci_grants) == 2 and bool(asr) and (asr[0].get("expires_at") or "")[:19] == latest[:19] and \
        sorted(asr[0]["evidence"].get("grant_ids") or []) == sorted(g["id"] for g in ci_grants)
    checks["first_sync_updates"] = (res.get("summary") or {}).get("updated")
    S["first"] = {"ents": by, "truth": t, "edges": keyset([], rels)[1]}
    ok = not any(diff.values()) and checks["grant_expiry_matches"] and len(sess) >= 4 and access_ok
    ev = {"run": {k: run.get(k) for k in ("result", "error", "stats")}, "summary": res.get("summary"), "entities": len(ents),
          "edges": len(rels), "expected_entities": len(want_e), "expected_edges": len(want_r), "diff": diff, "checks": checks,
          "diagnostics": run.get("diagnostics")}
    assert ok, json.dumps(ev)[:3000]
    return ev


@step(7, "HallPass: delete server db-1 and agent temp-bot, revoke ci-runner's inspect grant, end the exec")
def hp_remove():
    a, hp, aud = S["hp_admin"], S["hp"], S["hp_auditor"]
    a.call("DELETE", f"/servers/{S['servers']['db-1']['id']}", expect=204)
    a.call("DELETE", f"/agents/{S['agents']['temp-bot']['id']}", expect=204)
    revoked = []
    for g in hp_pages(aud, "/grants?state=active"):
        if g["agent_id"] == S["agents"]["ci-runner"]["id"] and g["role_id"] == S["roles"]["web-1/inspect"]["id"]:
            hp.post(f"/grants/{g['id']}/revoke", {"reason": "validation"})
            revoked.append(g["id"])
    if S.get("session_proc"):
        S["session_proc"].terminate()
        S["session_proc"].wait(timeout=30)
        wait_for(lambda: aud.get("/system/status")["live_channels"] == 0, "the exec channel to close")

    def settled():  # sessions of ended grants close; the remaining grant's session stays open (one session per grant)
        active = {g["id"] for g in hp_pages(aud, "/grants?state=active")}
        sess = hp_pages(aud, "/access-sessions?state=open") + hp_pages(aud, "/access-sessions?state=closing")
        return {s["grant_id"] for s in sess} == active and sess
    sess = wait_for(settled, "sessions of ended grants to close")
    closed = hp_pages(aud, "/access-sessions?state=closed")
    t = hp_truth()
    return {"revoked_grants": revoked, "active_grants_now": len(t["grants"]), "open_sessions_now": [s["remote_user"] for s in sess],
            "closed_sessions": sorted((s["remote_user"], s.get("close_reason")) for s in closed),
            "deleted_server_listed_by_default": any(s["alias"] == "db-1" for s in t["servers"]),
            "deleted_agent_listed_by_default": any(x["name"] == "temp-bot" for x in t["agents"])}


@step(8, "Deed: second sync removes what HallPass no longer holds (policy on_absent=tombstone)")
def second_sync():
    run, res = sync()
    assert run["result"] == "ok", f"run: {json.dumps(run)[:800]}"
    t = hp_truth()
    ents, rels = deed_state(S["src"])
    diff, want_e, want_r = compare(t, ents, rels)
    gone = sorted(set(S["first"]["ents"]) - {e["kind"] + "/" + e["metadata"]["name"] for e in ents})
    # The removed entities still exist as tombstones (by ID), with tombstonedAt set.
    tomb = {}
    for key in gone:
        code, e = S["deed"].call("GET", f"/entities/{S['first']['ents'][key]['metadata']['id']}")
        tomb[key] = (e.get("status") or {}).get("tombstonedAt") if code == 200 else f"HTTP {code}"
    for must in ("hallpass_server/db-1", "hallpass_role/db-1-dba", "hallpass_agent/temp-bot"):
        assert must in gone, f"{must} still present after removal in HallPass"
    assert all(tomb.values()), f"removed entities without tombstonedAt: {tomb}"
    ev = {"run": {k: run.get(k) for k in ("result", "error", "stats")}, "summary": res.get("summary"), "removed": gone,
          "tombstoned_at": tomb, "edges_removed": sorted(S["first"]["edges"] - keyset([], rels)[1]), "diff": diff}
    assert not any(diff.values()), json.dumps(ev)[:3000]
    return ev


@step(9, "Deed: connector test and sync with a wrong token fail with a clear diagnostic")
def bad_token():
    d = S["deed"]
    cfg = {"url": HP_URL, "allow_cidrs": ["127.0.0.1/32"], "insecure_http": True}
    bogus = "hallpass_at_" + base64.b32encode(os.urandom(20)).decode().lower()
    src = d.post("/sources", {"project": "home", "name": "hallpass-bad", "provider": "hallpass", "config": cfg, "schedule_seconds": 0,
                              "secrets": {"token": bogus}}, expect=201)
    test = d.post(f"/sources/{src['id']}/test", expect=200)
    assert not test["ok"], f"test with a wrong token reported ok: {test}"
    out = d.post(f"/sources/{src['id']}/sync", expect=200)
    ents = d.get(f"/entities?project=home&source={src['id']}&limit=5")["total"]
    assert out["run"]["result"] != "ok" and ents == 0
    return {"test": test, "sync_result": out["run"].get("result"), "sync_error": (out["run"].get("error") or "")[:300], "entities": ents}


@step(10, "Deed: with a token that may read addresses, catalog servers are matched to HallPass servers and agent boxes")
def address_matching():
    # The auditor role cannot read destination addresses by default (rbac.auditor_address_read); the administrator
    # token can, so this separate project exercises the address and hostname matching.
    d = S["deed"]
    d.post("/projects", {"name": "matching", "display_name": "Address matching (validation)"}, expect=201)
    d.post("/entities", {"project": "matching", "kind": "server", "name": "web-1-host", "spec": {"ipv4": "127.0.0.1", "hostname": "web-1.example.com"}},
           expect=201)
    d.post("/entities", {"project": "matching", "kind": "server", "name": "builder", "spec": {"hostname": "builder-1"}}, expect=201)
    cfg = {"url": HP_URL, "allow_cidrs": ["127.0.0.1/32"], "insecure_http": True}
    src = d.post("/sources", {"project": "matching", "name": "hallpass", "provider": "hallpass", "config": cfg, "schedule_seconds": 0,
                              "secrets": {"token": S["hp_admin"].bearer}}, expect=201)
    test = d.post(f"/sources/{src['id']}/test", expect=200)
    out = d.post(f"/sources/{src['id']}/sync", expect=200)
    assert out["run"]["result"] == "ok", out["run"]
    ents = {e["kind"] + "/" + e["metadata"]["name"]: e for e in deed_all(f"/entities?project=matching&source={src['id']}")}
    rels = deed_all(f"/relations?project=matching&source={src['id']}")
    web = ents.get("hallpass_server/web-1") or {}
    inferred = sorted(f"{r['type']} {r['from']['name']} -> {r['to']['name']}" for r in rels if r.get("inferred"))
    want = ["protected_by web-1-host -> web-1", "runs_on builder-1-ci-runner -> builder"]
    spec = web.get("spec") or {}
    assert all(w in inferred for w in want) and spec.get("address") == "127.0.0.1" and spec.get("port") == S["servers"]["web-1"]["port"], \
        {"inferred": inferred, "spec": spec}
    return {"test": [x["code"] for x in test["diagnostics"]], "web_1_address": [spec.get("address"), spec.get("port")], "inferred": inferred}


def main():
    hp_admin()
    if not results[-1]["ok"]:
        return finish()
    hp_inventory()
    hp_access()
    deed_setup()
    deed_source()
    first_sync()
    hp_remove()
    second_sync()
    bad_token()
    address_matching()
    finish()


def finish():
    if S.get("session_proc") and S["session_proc"].poll() is None:
        S["session_proc"].terminate()
    with open(os.path.join(WORK, "results.json"), "w") as f:
        json.dump({"hallpass_commit": os.environ.get("HP_COMMIT"), "destination": DEST, "steps": results}, f, indent=2)
    failed = [r["step"] for r in results if not r["ok"]]
    print(("FAILED steps: " + ", ".join(map(str, failed))) if failed else f"ALL {len(results)} STEPS PASSED", flush=True)
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
