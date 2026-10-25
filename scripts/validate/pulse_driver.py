#!/usr/bin/env python3
"""Real-pulsed validation driver for Deed's Pulse integration (stdlib only). Invoked by scripts/validate/pulse.sh:

  pulse_driver.py clients   create the Pulse clients and grants through the admin API (operator PAT), write their
                            .creds and Pulse's internal CA into $VAL_DIR/secrets (0600)
  pulse_driver.py run       set Deed up through its API and check the admin adapter, the tail, the ingest consumer and
                            the outbox relay against the running pulsed; writes $VAL_DIR/results.json

Every step prints PASS/FAIL with evidence. Names are example.com; payloads are synthetic."""
import base64, hashlib, hmac, http.cookiejar, json, os, re, struct, subprocess, sys, time, urllib.error, urllib.parse, urllib.request

VAL = os.environ["VAL_DIR"]
SEC = os.path.join(VAL, "secrets")
BIN = os.path.join(VAL, "bin")
NATS_URL = f"tls://127.0.0.1:{os.environ['NATS_PORT']}"
PULSE_ADMIN = f"http://127.0.0.1:{os.environ['ADMIN_PORT']}"
PORT, MGMT = os.environ.get("PORT", ""), os.environ.get("MGMT", "")
CA = os.path.join(SEC, "pulse-ca.pem")

# Pulse clients: Deed itself, a publisher (porch), a subscriber with a durable consumer (notifier), the push agent that
# feeds a Deed source over Pulse (billing-agent) and a second agent that tries to write into billing-agent's source.
CLIENTS = {
    "deed": [{"action": "publish", "subject": "evt.deed.>"}, {"action": "subscribe", "subject": "evt.>"},
             {"action": "durable", "name": "deed_ingest", "subject": "evt.deed.ingest.>"}],
    "porch": [{"action": "publish", "subject": "evt.porch.>"}],
    "notifier": [{"action": "subscribe", "subject": "evt.porch.tenancy.>"},
                 {"action": "durable", "name": "tenants", "subject": "evt.porch.tenancy.>"}],
    "billing-agent": [{"action": "publish", "subject": "evt.deed.ingest.billing-agent.>"}],
    "rogue-agent": [{"action": "publish", "subject": "evt.deed.ingest.rogue-agent.>"}],
}
PORCH_SUBJECT = "evt.porch.tenancy.tenant.updated.ten_01jb9w2v3x4y5z6a7b8c9d0%03d"
PORCH_TYPE = "porch.tenancy.tenant_updated.v1"
results = []


def secret(name):
    return open(os.path.join(SEC, name)).read().strip()


def write_secret(name, data):
    fd = os.open(os.path.join(SEC, name), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        f.write(data)


def http_json(method, url, body=None, headers=None):
    h = {"Content-Type": "application/json", "Accept": "application/json"}
    h.update(headers or {})
    req = urllib.request.Request(url, method=method, data=json.dumps(body).encode() if body is not None else None, headers=h)
    try:
        resp = urllib.request.urlopen(req, timeout=60)
        code, data = resp.status, resp.read()
    except urllib.error.HTTPError as e:
        code, data = e.code, e.read()
    try:
        return code, json.loads(data) if data else None
    except ValueError:
        return code, data.decode(errors="replace")


def pulse_admin(method, path, body=None, pat="operator.pat"):
    return http_json(method, PULSE_ADMIN + "/api/admin/v1" + path, body, {"Authorization": "Bearer " + secret(pat)})


def clients():
    code, info = pulse_admin("GET", "/system/info")
    assert code == 200, (code, info)
    write_secret("pulse-ca.pem", info["connect"]["ca_cert"])
    ids = {}
    for name, grants in CLIENTS.items():
        code, js = pulse_admin("POST", "/clients", {"name": name, "description": f"{name} (Deed validation, example.com)",
                                                    "tags": ["deed-validation"], "grants": grants, "expires_in_days": 1})
        assert code == 201, (name, code, js)
        write_secret(f"{name}.creds", js["creds"])
        ids[name] = js["client"]["id"]
        print(f"  client {name}: id {js['client']['id']}, status {js['client']['status']}, grants {[g['action'] + ':' + g.get('subject', '') for g in grants]}")
    json.dump(ids, open(os.path.join(VAL, "pulse-clients.json"), "w"))


class Deed:
    def __init__(self, bearer=None):
        self.base = f"http://127.0.0.1:{PORT}"
        self.bearer, self.csrf = bearer, ""
        self.op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def call(self, method, path, body=None, expect=None, base=None, raw=False):
        h = {"Content-Type": "application/json", "Origin": self.base}
        if self.csrf:
            h["X-Deed-CSRF"] = self.csrf
        if self.bearer:
            h["Authorization"] = "Bearer " + self.bearer
        req = urllib.request.Request((base or self.base + "/api/v1") + path, method=method,
                                     data=json.dumps(body).encode() if body is not None else None, headers=h)
        try:
            resp = self.op.open(req, timeout=120)
            code, data = resp.status, resp.read()
        except urllib.error.HTTPError as e:
            code, data = e.code, e.read()
        js = data.decode(errors="replace") if raw else (json.loads(data) if data and data[:1] in (b"{", b"[") else data.decode(errors="replace"))
        if expect is not None and code != expect:
            raise AssertionError(f"{method} {path}: {code} (want {expect}): {str(js)[:600]}")
        return code, js

    def get(self, path):
        return self.call("GET", path, expect=200)[1]

    def post(self, path, body=None, expect=None):
        return self.call("POST", path, body if body is not None else {}, expect=expect)[1]


def totp(sec):
    k = base64.b32decode(sec + "=" * ((8 - len(sec) % 8) % 8))
    h = hmac.new(k, struct.pack(">Q", int(time.time()) // 30), hashlib.sha1).digest()
    o = h[-1] & 15
    return "%06d" % ((struct.unpack(">I", h[o:o + 4])[0] & 0x7FFFFFFF) % 1000000)


def pulsepub(cmd, client, *args, check=True):
    p = subprocess.run([os.path.join(BIN, "pulsepub"), cmd, "-url", NATS_URL, "-creds", os.path.join(SEC, client + ".creds"), "-ca", CA,
                        "-source", client, *args], capture_output=True, text=True, timeout=120)
    if check and p.returncode != 0:
        raise AssertionError(f"pulsepub {cmd} as {client}: exit {p.returncode}: {p.stderr[-600:]}")
    return p


def events(filt):
    out = pulsepub("events", "deed", "-filter", filt).stdout
    return [json.loads(l) for l in out.splitlines() if l.strip()]


def metrics():
    _, txt = Deed().call("GET", "/metrics", base=f"http://127.0.0.1:{MGMT}", raw=True)
    vals = {}
    for l in txt.splitlines():
        if l.startswith(("deed_pulse", "deed_outbox")):
            k, _, v = l.rpartition(" ")
            vals[k] = float(v)
    return vals


def wait_for(fn, what, timeout=120, every=2.0):
    deadline, last = time.time() + timeout, None
    while time.time() < deadline:
        try:
            v = fn()
            if v:
                return v
        except Exception as ex:  # noqa: BLE001 — retried until the deadline
            last = ex
        time.sleep(every)
    raise AssertionError(f"timed out after {timeout}s waiting for {what}" + (f" ({last})" if last else ""))


def step(n, title):
    def deco(fn):
        def wrapped():
            t0 = time.time()
            try:
                ev = fn() or {}
                results.append({"step": n, "title": title, "ok": True, "evidence": ev, "seconds": round(time.time() - t0, 1)})
                print(f"PASS {n:>2}. {title}\n        {json.dumps(ev)[:1500]}")
            except Exception as ex:  # noqa: BLE001 — every failure is reported, then the run continues where possible
                results.append({"step": n, "title": title, "ok": False, "error": str(ex)[:2000], "seconds": round(time.time() - t0, 1)})
                print(f"FAIL {n:>2}. {title}\n        {ex}")
        return wrapped
    return deco


S = {}


def rels(api, **q):
    qs = "&".join(f"{k}={urllib.parse.quote(str(v))}" for k, v in q.items())
    return api.get(f"/relations?project=platform&limit=500&{qs}")["items"]


def name(e):
    return e["metadata"]["name"]


def run_summary(r):
    return {"id": r["id"], "result": r["result"], "stats": r.get("stats"), "diagnostics": [d.get("message") for d in r.get("diagnostics") or []],
            "error": r.get("error")}


def edge(r):
    return f"{r['from']['kind']}:{r['from']['name']} -{r['type']}-> {r['to']['kind']}:{r['to']['name']} [{r['evidence_class']}]"


@step(1, "pulsed runs from source with operator-mode NATS, TLS and the admin API")
def s1():
    code, info = pulse_admin("GET", "/system/info", pat="viewer.pat")
    assert code == 200 and info["ready"] and info["nats"]["connected"], (code, info)
    code, cl = pulse_admin("GET", "/clients", pat="viewer.pat")
    code_w, _ = pulse_admin("POST", "/clients", {"name": "viewer-cannot-create", "grants": []}, pat="viewer.pat")
    assert code_w in (401, 403), code_w
    return {"pulse_commit": os.environ.get("PULSE_COMMIT"), "pulse_tracked_changes": os.environ.get("PULSE_DIRTY"), "pulsed": info["version"],
            "profile": info["profile"], "nats": info["nats"]["version"], "server_name": info["nats"]["server_name"],
            "nats_url": info["connect"]["nats_url"], "clients": sorted(c["name"] for c in cl), "viewer_pat_create_client_status": code_w}


@step(2, "Deed starts with its Pulse .creds; publisher, tail and ingest connections are up")
def s2():
    owner = Deed()
    me = owner.post("/auth/setup/complete", {"token": secret("setup-token"), "email": "owner@example.com", "display_name": "Validation Owner",
                                             "password": "correct horse battery staple pulse"}, expect=200)
    owner.csrf = me["csrf_token"]
    enr = owner.post("/me/mfa/totp", expect=200)
    owner.post("/me/mfa/totp/confirm", {"code": totp(enr["secret"])}, expect=200)
    owner.csrf = owner.get("/me")["csrf_token"]
    t = owner.post("/me/tokens", {"name": "pulse-validation", "installation_role": "admin"}, expect=201)
    write_secret("deed-api.token", t["token"])  # for inspecting a HOLD=1 run
    S["api"] = api = Deed(bearer=t["token"])
    S["project"] = api.post("/projects", {"name": "platform", "display_name": "Platform (validation)"}, expect=201)
    m = wait_for(lambda: (lambda v: v if all(v.get(f'deed_pulse_connected{{conn="{c}"}}') == 1 for c in ("publisher", "tail", "ingest")) else None)(metrics()),
                 "deed_pulse_connected for publisher, tail and ingest", timeout=60)
    log = open(os.path.join(VAL, "deed.log")).read()
    cons = re.findall(r'"msg":"pulse ingest consumer running"[^\n]*?"consumer":"([^"]+)"', log)
    ids = json.load(open(os.path.join(VAL, "pulse-clients.json")))
    assert cons and cons[0] == ids["deed"] + "_deed_ingest", cons
    S["ids"] = ids
    return {"connected": {k: v for k, v in m.items() if k.startswith("deed_pulse_connected")}, "ingest_consumer": cons[0]}


@step(3, "Publish traffic as Pulse clients (porch publisher, notifier durable consumer)")
def s3():
    d = pulsepub("durable", "notifier", "-filter", "evt.porch.tenancy.>", "-name", "tenants").stdout.strip()
    p = pulsepub("publish", "porch", "-subject", PORCH_SUBJECT, "-type", PORCH_TYPE, "-n", "25").stdout.strip()
    denied = pulsepub("publish", "porch", "-subject", "evt.deed.ingest.porch.items", "-type", "deed.ingest.items.v1", "-timeout", "5s", check=False)
    assert denied.returncode != 0 and "Permissions Violation" in denied.stderr, denied.stderr
    return {"durable": d, "published": p, "outside_grant": denied.stderr.strip().splitlines()[0][:200]}


@step(4, "pulse_admin source: viewer PAT, test, sync (grants, streams, durables, subject index)")
def s4():
    api = S["api"]
    src = api.post("/sources", {"project": "platform", "name": "pulse-admin", "provider": "pulse_admin",
                                "config": {"admin_url": PULSE_ADMIN, "insecure_http": True, "allow_cidrs": ["127.0.0.1/32"]},
                                "secrets": {"token": secret("viewer.pat")}}, expect=201)
    S["admin_src"] = src["id"]
    test = api.post(f"/sources/{src['id']}/test", expect=200)
    assert test["ok"], test
    run = api.post(f"/sources/{src['id']}/sync", expect=200)
    assert run["run"]["result"] == "ok", run["run"]
    ents = api.get(f"/entities?project=platform&source={src['id']}&limit=500")["items"]
    kinds = {}
    for e in ents:
        kinds.setdefault(e["kind"], []).append(name(e))
    rs = rels(api, source=src["id"])
    edges = sorted(edge(r) for r in rs)
    want = ["service:porch -publishes-> nats_subject:evt.porch.all [declared]",
            "service:deed -subscribes_to-> nats_subject:evt.all [declared]",
            "service:deed -publishes-> nats_subject:evt.deed.all [declared]",
            "service:billing-agent -publishes-> nats_subject:evt.deed.ingest.billing-agent.all [declared]",
            "service:notifier -subscribes_to-> nats_subject:evt.porch.tenancy.all [declared]",
            "nats_consumer:events-" + S["ids"]["notifier"] + "_tenants -subscribes_to-> nats_subject:evt.porch.tenancy.all [declared]",
            "nats_consumer:events-" + S["ids"]["deed"] + "_deed_ingest -subscribes_to-> nats_subject:evt.deed.ingest.all [declared]",
            "service:deed -subscribes_to-> nats_subject:evt.deed.ingest.all [declared]",
            "nats_system:pulse -contains-> nats_stream:events [declared]",
            "event_contract:porch.tenancy.tenant_updated.v1 -carried_on-> nats_subject:evt.porch.tenancy.tenant.updated.star [observed]",
            "nats_subject:evt.porch.all -contains-> nats_subject:evt.porch.tenancy.tenant.updated.star [structural]"]
    missing = [w for w in want if w not in edges]
    subj = next((e for e in ents if name(e) == "evt.porch.tenancy.tenant.updated.star"), None)
    assert not missing, {"missing": missing, "edges": edges}
    obs = (subj or {}).get("status", {}).get("observed") or {}
    assert obs.get("messages_retained", 0) >= 25 and obs.get("last_type") == PORCH_TYPE and obs.get("concrete_subjects", 0) >= 25, subj
    return {"test": [d["message"] for d in test["diagnostics"]], "run": run_summary(run["run"]),
            "entities_by_kind": {k: sorted(v) for k, v in kinds.items()}, "edges": edges, "subject_activity": obs}


@step(5, "pulse_tail: observed publishes edges and rollups from real pulsed traffic")
def s5():
    api = S["api"]
    tail = api.post("/sources", {"project": "platform", "name": "pulse-tail", "provider": "pulse_tail", "config": {"window_seconds": 15}}, expect=201)
    S["tail_src"] = tail["id"]

    def observed():
        pulsepub("publish", "porch", "-subject", PORCH_SUBJECT, "-type", PORCH_TYPE, "-n", "10")
        rs = [r for r in rels(api, source=tail["id"], type="publishes") if r["from"]["name"] == "porch"]
        return rs or None
    rs = wait_for(observed, "observed publishes edge porch -> template", timeout=150, every=5)
    m = metrics()
    carried = [edge(r) for r in rels(api, source=tail["id"], type="carried_on")]
    assert rs[0]["evidence_class"] == "observed" and rs[0]["to"]["name"] == "evt.porch.tenancy.tenant.updated.star", rs[0]
    return {"edges": [edge(r) for r in rs], "attributes": rs[0].get("attributes"), "carried_on": carried,
            "tail_counted": m.get('deed_pulse_tail_messages_total{outcome="counted"}')}


@step(6, "Ingest consumer: a deed.ingest.items.v1 envelope from billing-agent lands in its push source")
def s6():
    api = S["api"]
    src = api.post("/sources", {"project": "platform", "name": "billing-agent", "provider": "push", "layer": "discovered",
                                "config": {"pulse_source": "billing-agent"}}, expect=201)
    S["push_src"] = src["id"]
    batch = os.path.join(VAL, "billing-batch.json")
    json.dump({"entities": [{"kind": "container", "name": "billing-worker-1", "external_id": "c-billing-worker-1",
                             "spec": {"image": "example/billing-worker", "state": "running"}}],
               "relations": [{"type": "subscribes_to", "from": {"kind": "container", "name": "billing-worker-1"},
                              "to": {"kind": "nats_subject", "name": "evt.porch.tenancy.all"}}]}, open(batch, "w"))
    before = metrics().get('deed_pulse_consumed_total{result="ok"}', 0)
    out = pulsepub("ingest", "billing-agent", "-deed-source", src["id"], "-file", batch).stdout.strip()
    got = wait_for(lambda: api.get(f"/entities?project=platform&source={src['id']}&kind=container")["items"], "container from the ingest envelope")
    m = metrics()
    assert m.get('deed_pulse_consumed_total{result="ok"}', 0) >= before + 1
    rs = [edge(r) for r in rels(api, source=src["id"])]
    return {"published": out, "entities": [f"{e['kind']}:{name(e)}" for e in got], "relations": rs,
            "consumed": {k: v for k, v in m.items() if "consumed" in k}}


@step(7, "Ingest consumer refuses an envelope another Pulse client publishes into billing-agent's source")
def s7():
    api = S["api"]
    batch = os.path.join(VAL, "rogue-batch.json")
    json.dump({"entities": [{"kind": "container", "name": "rogue-1", "external_id": "c-rogue-1", "spec": {"image": "example/rogue"}}]},
              open(batch, "w"))
    before = metrics().get('deed_pulse_consumed_total{result="terminal"}', 0)
    # ce-source is chosen by the publisher; Pulse authenticates only the subject (grant evt.deed.ingest.rogue-agent.>).
    spoof = pulsepub("ingest", "rogue-agent", "-deed-source", S["push_src"], "-file", batch, "-subject", "evt.deed.ingest.rogue-agent.items")
    # Make the spoof explicit: same envelope with ce-source billing-agent, published with rogue-agent's credentials.
    spoof2 = subprocess.run([os.path.join(BIN, "pulsepub"), "ingest", "-url", NATS_URL, "-creds", os.path.join(SEC, "rogue-agent.creds"), "-ca", CA,
                             "-source", "billing-agent", "-deed-source", S["push_src"], "-file", batch, "-subject", "evt.deed.ingest.rogue-agent.items"],
                            capture_output=True, text=True, timeout=60)
    assert spoof2.returncode == 0, spoof2.stderr
    wait_for(lambda: metrics().get('deed_pulse_consumed_total{result="terminal"}', 0) >= before + 2, "two terminal rejections", timeout=60)
    time.sleep(2)
    rogue = api.get(f"/entities?project=platform&q=rogue-1")["total"]
    assert rogue == 0, "rogue entity was written"
    log = [json.loads(l) for l in open(os.path.join(VAL, "deed.log")) if "ingest event rejected" in l]
    return {"published": [spoof.stdout.strip(), spoof2.stdout.strip()], "rogue_entities": rogue,
            "rejections": [{"publisher": x.get("publisher"), "error": x.get("error.message")} for x in log[-2:]]}


@step(8, "Outbox relay: Deed's own events arrive through pulsed with CloudEvents headers")
def s8():
    evs = wait_for(lambda: (lambda e: e if any(x["subject"] == "evt.deed.ingest.snapshot.committed" for x in e)
                            and any(x["subject"] == "evt.deed.catalog.project.created" for x in e) else None)(events("evt.deed.>")),
                   "evt.deed.catalog.project.created and evt.deed.ingest.snapshot.committed", timeout=60)
    deed_evs = [e for e in evs if e["headers"].get("ce-source") == "deed"]
    problems = []
    tre = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?Z$")
    for e in deed_evs:
        h = e["headers"]
        if not h.get("Nats-Msg-Id"):
            problems.append((e["subject"], "no Nats-Msg-Id"))
        t = h.get("ce-type", "")
        if not re.match(r"^deed\.[a-z_.]+\.v1$", t):
            problems.append((e["subject"], "ce-type " + t))
        if not tre.match(h.get("ce-time", "")):
            problems.append((e["subject"], "ce-time " + h.get("ce-time", "")))
        if h.get("content-type") != "application/json":
            problems.append((e["subject"], "content-type " + h.get("content-type", "")))
    ids = [e["headers"].get("Nats-Msg-Id") for e in deed_evs]
    assert not problems and len(ids) == len(set(ids)), (problems, len(ids), len(set(ids)))
    pc = next(e for e in deed_evs if e["subject"] == "evt.deed.catalog.project.created")
    sc = next(e for e in deed_evs if e["subject"] == "evt.deed.ingest.snapshot.committed")
    assert pc["headers"]["ce-type"] == "deed.catalog.project_created.v1" and sc["headers"]["ce-type"] == "deed.ingest.snapshot_committed.v1"
    assert (pc.get("data") or {}).get("project_id") == S["project"]["id"] or (pc.get("data") or {}).get("id") == S["project"]["id"], pc.get("data")
    S["deed_event"] = sc
    return {"deed_events": len(deed_evs), "subjects": sorted({e["subject"] for e in deed_evs}), "project_created": pc,
            "snapshot_committed_headers": sc["headers"], "snapshot_committed_data": sc.get("data")}


@step(9, "Dedup by message ID: republishing a Deed event with its ID is stored once; Deed skips its own ingest-subject events")
def s9():
    sc = S["deed_event"]
    before = [e for e in events("evt.deed.ingest.snapshot.committed") if e["headers"]["Nats-Msg-Id"] == sc["headers"]["Nats-Msg-Id"]]
    out = subprocess.run([os.path.join(BIN, "pulsepub"), "publish", "-url", NATS_URL, "-creds", os.path.join(SEC, "deed.creds"), "-ca", CA,
                          "-source", "deed", "-subject", sc["subject"], "-type", sc["headers"]["ce-type"], "-id", sc["headers"]["Nats-Msg-Id"]],
                         capture_output=True, text=True, timeout=60)
    assert out.returncode == 0, out.stderr
    after = [e for e in events("evt.deed.ingest.snapshot.committed") if e["headers"]["Nats-Msg-Id"] == sc["headers"]["Nats-Msg-Id"]]
    assert "duplicates 1" in out.stdout and len(before) == len(after) == 1, (out.stdout, len(before), len(after))
    m = metrics()
    log = open(os.path.join(VAL, "deed.log")).read()
    own_rejected = len(re.findall(r'"ingest event rejected"[^\n]*"publisher":"deed"', log))
    assert own_rejected == 0
    return {"republish": out.stdout.strip(), "stored_with_id": len(after), "outbox": {k: v for k, v in m.items() if k.startswith("deed_outbox")},
            "own_events_rejected_by_ingest": own_rejected}


@step(10, "pulse_admin resync after traffic: consumers, client connections and subject activity")
def s10():
    api = S["api"]
    run = api.post(f"/sources/{S['admin_src']}/sync", expect=200)
    ents = api.get(f"/entities?project=platform&source={S['admin_src']}&kind=nats_consumer&limit=50")["items"]
    svc = api.get(f"/entities?project=platform&source={S['admin_src']}&kind=service&limit=50")["items"]
    subj = api.get(f"/entities?project=platform&source={S['admin_src']}&kind=nats_subject&limit=200")["items"]
    deed_svc = next(e for e in svc if name(e) == "deed")
    obs = {name(e): {k: (e["status"].get("observed") or {}).get(k) for k in ("messages_retained", "last_type")} for e in subj if not name(e).endswith(".all")}
    consumers = sorted(name(e) for e in ents)
    conns = (deed_svc["status"].get("observed") or {}).get("pulse_connections", 0)
    assert run["run"]["result"] == "ok", run["run"]
    assert f"events-{S['ids']['deed']}_deed_ingest" in consumers and f"events-{S['ids']['notifier']}_tenants" in consumers, consumers
    assert conns >= 3, deed_svc["status"]
    assert obs.get("evt.deed.ingest.billing-agent.items", {}).get("last_type") == "deed.ingest.items.v1", obs
    assert obs.get("evt.deed.ingest.snapshot.committed", {}).get("last_type") == "deed.ingest.snapshot_committed.v1", obs
    return {"run": run_summary(run["run"]), "consumers": consumers, "deed_service_pulse_connections": conns,
            "deed_service_health": deed_svc["status"].get("health"), "subjects": obs}


def dump_admin():
    """Saves the admin API responses the pulse_admin adapter reads (viewer PAT) for comparison with
    internal/pulseint/testdata/admin.json. Public data only: the CA certificate is dropped."""
    out = {}
    for p in ("/system/info", "/clients", "/streams", "/streams/EVENTS/consumers"):
        out[p] = pulse_admin("GET", p, pat="viewer.pat")[1]
    out["/system/info"].get("connect", {}).pop("ca_cert", None)
    queue = ["evt"]
    while queue and len(out) < 60:
        prefix = queue.pop(0)
        code, tree = pulse_admin("GET", "/subjects/tree?" + urllib.parse.urlencode({"stream": "EVENTS", "prefix": prefix}), pat="viewer.pat")
        out[f"/subjects/tree?prefix={prefix}&stream=EVENTS"] = tree
        queue += [n["path"] for n in (tree or {}).get("nodes", []) if n.get("children")]
    json.dump(out, open(os.path.join(VAL, "pulse-admin-responses.json"), "w"), indent=1)


def main():
    if sys.argv[1:] == ["clients"]:
        clients()
        return
    for fn in (s1, s2, s3, s4, s5, s6, s7, s8, s9, s10):
        fn()
    dump_admin()
    json.dump({"results": results, "pulse_commit": os.environ.get("PULSE_COMMIT"),
               "finished": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}, open(os.path.join(VAL, "results.json"), "w"), indent=1)
    failed = [r["step"] for r in results if not r["ok"]]
    print(f"\n{len(results) - len(failed)}/{len(results)} steps passed" + (f"; failed: {failed}" if failed else ""))
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
