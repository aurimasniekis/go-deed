#!/usr/bin/env python3
"""Spec §18 end-to-end acceptance scenario driver (stdlib only). Invoked by scripts/e2e.sh, which builds the binaries,
starts the dev stack and a fresh deed, and exports E2E_DIR, PORT, MGMT, RPORT, RMGMT, PROVIDER_DIR.

Every step prints PASS/FAIL with evidence; results.json in E2E_DIR records them. Fixtures are synthetic
(example.com, RFC 5737 addresses); the Pulse traffic is Pulse-compatible (go-pulse against local NATS JetStream),
not a real pulsed deployment."""
import base64, hashlib, hmac, http.cookiejar, json, os, struct, subprocess, sys, textwrap, time, urllib.error, urllib.request

E2E = os.environ["E2E_DIR"]
PORT, MGMT, RPORT, RMGMT = (os.environ[k] for k in ("PORT", "MGMT", "RPORT", "RMGMT"))
BIN = os.path.join(E2E, "bin")
ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
results = []


class Client:
    def __init__(self, port, bearer=None):
        self.base = f"http://127.0.0.1:{port}"
        self.bearer = bearer
        self.csrf = ""
        self.op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def call(self, method, path, body=None, headers=None, expect=None, raw=False, base=None):
        h = {"Content-Type": "application/json", "Origin": self.base}
        if self.csrf:
            h["X-Deed-CSRF"] = self.csrf
        if self.bearer:
            h["Authorization"] = "Bearer " + self.bearer
        h.update(headers or {})
        url = (base or self.base + "/api/v1") + path
        req = urllib.request.Request(url, method=method, data=json.dumps(body).encode() if body is not None else None, headers=h)
        try:
            resp = self.op.open(req, timeout=60)
            code, data, hdr = resp.status, resp.read(), resp.headers
        except urllib.error.HTTPError as e:
            code, data, hdr = e.code, e.read(), e.headers
        js = None if raw else (json.loads(data) if data and data[:1] in (b"{", b"[") else data.decode(errors="replace"))
        if expect is not None and code != expect:
            raise AssertionError(f"{method} {path}: {code} (want {expect}): {str(js)[:400]}")
        return (code, data.decode(errors="replace") if raw else js, hdr)

    def get(self, path, **kw):
        return self.call("GET", path, **kw)[1]

    def post(self, path, body=None, expect=None, **kw):
        return self.call("POST", path, body if body is not None else {}, expect=expect, **kw)[1]


def totp(secret):
    k = base64.b32decode(secret + "=" * ((8 - len(secret) % 8) % 8))
    h = hmac.new(k, struct.pack(">Q", int(time.time()) // 30), hashlib.sha1).digest()
    o = h[-1] & 15
    return "%06d" % ((struct.unpack(">I", h[o:o + 4])[0] & 0x7FFFFFFF) % 1000000)


def run(cmd, env=None, check=True, cwd=None, inp=None):
    e = dict(os.environ)
    e.update(env or {})
    p = subprocess.run(cmd, env=e, cwd=cwd, input=inp, capture_output=True, text=True)
    if check and p.returncode != 0:
        raise AssertionError(f"{' '.join(cmd[:4])}… exit {p.returncode}: {p.stderr[-800:]}{p.stdout[-400:]}")
    return p


def wait_for(fn, what, timeout=90, every=1.0):
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        try:
            v = fn()
            if v:
                return v
        except Exception as ex:  # noqa: BLE001 — retried until the deadline
            last = ex
        time.sleep(every)
    raise AssertionError(f"timed out waiting for {what}" + (f" ({last})" if last else ""))


def step(n, title):
    def deco(fn):
        def wrapped(*a, **kw):
            t0 = time.time()
            try:
                ev = fn(*a, **kw) or {}
                results.append({"step": n, "title": title, "ok": True, "evidence": ev, "seconds": round(time.time() - t0, 1)})
                print(f"PASS {n:>2}. {title}\n        {json.dumps(ev)[:600]}")
                return ev
            except Exception as ex:  # noqa: BLE001 — every failure is reported, then the run continues where possible
                results.append({"step": n, "title": title, "ok": False, "error": str(ex)[:1500], "seconds": round(time.time() - t0, 1)})
                print(f"FAIL {n:>2}. {title}\n        {ex}")
                return None
        return wrapped
    return deco


S = {}  # shared state between steps
owner = Client(PORT)


@step(1, "Deploy Deed locally with SQLite")
def s1():
    info = owner.get("/system/info")
    code, _, _ = owner.call("GET", "/readyz", base=f"http://127.0.0.1:{MGMT}", raw=True)
    assert info["api_version"] == "v1" and code == 200
    return {"version": info["version"], "api_version": info["api_version"], "readyz": code, "db": os.path.join(E2E, "data", "deed.db")}


@step(2, "Authenticate (bootstrap link, local password + TOTP; API token)")
def s2():
    tok = open(os.path.join(E2E, "setup-token")).read().strip()
    me = owner.post("/auth/setup/complete", {"token": tok, "email": "owner@example.com", "display_name": "E2E Owner",
                                              "password": "correct horse battery staple e2e"}, expect=200)
    owner.csrf = me["csrf_token"]
    blocked = owner.call("GET", "/projects")[0]
    enr = owner.post("/me/mfa/totp", expect=200)
    rc = owner.post("/me/mfa/totp/confirm", {"code": totp(enr["secret"])}, expect=200)
    me = owner.get("/me")
    owner.csrf = me["csrf_token"]
    owner.post("/projects", {"name": "home", "display_name": "Home lab (synthetic)"}, expect=201)
    t = owner.post("/me/tokens", {"name": "e2e-admin", "installation_role": "admin"}, expect=201)
    S["token"] = t["token"]
    S["api"] = Client(PORT, bearer=t["token"])
    # A wrong TOTP code on a fresh login is refused.
    c2 = Client(PORT)
    c2.post("/auth/login", {"email": "owner@example.com", "password": "correct horse battery staple e2e"}, expect=200)
    bad = c2.call("POST", "/auth/mfa", {"code": "000000"})[0]
    return {"before_mfa_enrollment_projects_status": blocked, "recovery_codes": len(rc["recovery_codes"]), "mfa_enrolled": me["mfa_enrolled"],
            "wrong_totp_status": bad, "api_token_prefix": t["token"][:8]}


@step(3, "Register a custom kind")
def s3():
    api = S["api"]
    k = api.post("/kinds", {"name": "example.com/ups", "title": "UPS", "description": "Uninterruptible power supply (synthetic)",
                            "icon": "tabler:battery-charging", "identity_keys": [["/spec/serial"]],
                            "schema": {"type": "object", "required": ["capacity_va"], "properties": {"capacity_va": {"type": "integer", "minimum": 0},
                                                                                                    "serial": {"type": "string"}}}}, expect=201)
    bad = api.call("POST", "/entities", {"project": "home", "kind": "example.com/ups", "name": "bad-ups", "spec": {"capacity_va": "lots"}})
    return {"kind": k["name"], "invalid_spec_status": bad[0], "invalid_fields": [f["field"] for f in bad[1].get("fields", [])]}


@step(4, "Import a sanitized Terraform state (push source, CLI)")
def s4():
    api = S["api"]
    src = api.post("/sources", {"project": "home", "name": "tfstate", "provider": "terraform_state", "config": {"backend": "push"}}, expect=201)
    st = api.post(f"/sources/{src['id']}/tokens", {"name": "ci"}, expect=201)
    S["tf_src"], S["tf_tok"] = src["id"], st["token"]
    fixture = os.path.join(ROOT, "internal/tfimport/testdata/state-v4.json")
    env = {"DEED_ENDPOINT": f"http://127.0.0.1:{PORT}", "DEED_SOURCE_TOKEN": st["token"]}
    prev = json.loads(run([f"{BIN}/deed", "import", "terraform", "-json", "-source", src["id"], "-file", fixture, "-dry-run"], env=env).stdout)["result"]
    out = json.loads(run([f"{BIN}/deed", "import", "terraform", "-json", "-source", src["id"], "-file", fixture], env=env).stdout)
    diags, out = out.get("diagnostics") or [], out["result"]
    servers = api.get("/entities?project=home&kind=vm&limit=50")["items"]  # hcloud_server maps to vm
    S["server"] = sorted(e["metadata"]["name"] for e in servers)[0]
    leak = api.get("/entities?project=home&q=PLANTED")["total"]
    assert leak == 0 and out["summary"]["errors"] == 0
    return {"dry_run_created": prev["summary"]["created"], "created": out["summary"]["created"], "servers": [e["metadata"]["name"] for e in servers],
            "planted_secret_hits": leak, "diagnostics": [d["code"] for d in diags][:6]}


TF_BASE = textwrap.dedent('''
    terraform {
      required_providers {
        deed = { source = "aurimasniekis/deed" }
      }
    }
    provider "deed" {}
    variable "server" { type = string }

    resource "deed_entity" "platform" {
      project      = "home"
      kind         = "group"
      name         = "platform"
      display_name = "Platform team"
    }
    resource "deed_entity" "ups" {
      project = "home"
      kind    = "example.com/ups"
      name    = "ups-01"
      spec    = jsonencode({ capacity_va = 1500, serial = "UPS-SYNTHETIC-01" })
    }
    ''')
TF_PORCH = textwrap.dedent('''
    resource "deed_entity" "porch" {
      project      = "home"
      kind         = "service"
      name         = "porch"
      display_name = "%s"
      owner        = deed_entity.platform.name
      links = [
        { type = "repository", title = "Source", url = "https://github.com/example/porch" },
        { type = "dashboard", title = "Grafana", url = "https://grafana.example.com/d/deed-overview" },
      ]
      spec = jsonencode({ pulse_source = "porch", url = "https://id.example.com" })
    }
    resource "deed_relationship" "porch_owner" {
      type = "owned_by"
      from = { id = deed_entity.porch.id }
      to   = { id = deed_entity.platform.id }
    }
    resource "deed_relationship" "porch_runs_on" {
      type = "runs_on"
      from = { id = deed_entity.porch.id }
      to   = { kind = "vm", name = var.server }
    }
    resource "deed_relationship" "ups_powers" {
      type    = "depends_on"
      project = "home"
      from    = { kind = "vm", name = var.server }
      to      = { kind = deed_entity.ups.kind, name = deed_entity.ups.name }
    }
    ''')
TF_IMPORT = textwrap.dedent('''
    resource "deed_entity" "notifier" {
      project = "home"
      kind    = "service"
      name    = "notifier"
    }
    ''')


def tf_env():
    rc = os.path.join(E2E, "terraformrc")
    with open(rc, "w") as f:
        f.write('provider_installation {\n  dev_overrides { "aurimasniekis/deed" = "%s" }\n  direct {}\n}\n' % BIN)
    return {"TF_CLI_CONFIG_FILE": rc, "DEED_ENDPOINT": f"http://127.0.0.1:{PORT}", "DEED_TOKEN": S["token"], "TF_IN_AUTOMATION": "1",
            "TF_VAR_server": S["server"]}


def tf(*args, check=True):
    d = os.path.join(E2E, "tf")
    return run(["terraform", f"-chdir={d}", *args, "-no-color"], env=tf_env(), check=check)


def write_tf(*parts):
    d = os.path.join(E2E, "tf")
    os.makedirs(d, exist_ok=True)
    with open(os.path.join(d, "main.tf"), "w") as f:
        f.write("".join(parts))


@step(5, "Install the provider built from terraform-provider-deed (dev_overrides)")
def s5():
    write_tf(TF_BASE, TF_PORCH % "Porch identity")
    v = run(["terraform", "version", "-json"]).stdout
    p = tf("validate", check=False)
    assert p.returncode == 0, p.stderr
    return {"terraform": json.loads(v)["terraform_version"], "provider_binary": os.path.join(BIN, "terraform-provider-deed"),
            "validate": "ok", "dev_override_warning": "Provider development overrides are in effect" in (p.stdout + p.stderr)}


@step(6, "Declare entities and relationships through Terraform; apply")
def s6():
    out = tf("apply", "-auto-approve", "-input=false").stdout
    api = S["api"]
    porch = api.get("/entities/lookup?project=home&kind=service&name=porch")
    S["porch"] = porch["metadata"]["id"]
    added = [l for l in out.splitlines() if "Apply complete" in l]
    return {"apply": added[-1] if added else out[-200:], "porch_id": S["porch"], "declared": porch["status"]["declared"]}


@step(7, "A subsequent plan is a no-op (-detailed-exitcode = 0)")
def s7():
    p = tf("plan", "-detailed-exitcode", "-input=false", check=False)
    assert p.returncode == 0, p.stdout[-1500:]
    return {"plan_exit_code": p.returncode}


@step(8, "Update and import provider-managed resources")
def s8():
    write_tf(TF_BASE, TF_PORCH % "Porch identity provider")
    tf("apply", "-auto-approve", "-input=false")
    api = S["api"]
    dn = api.get(f"/entities/{S['porch']}")["metadata"]["displayName"]
    # A declaration made outside this configuration (same terraform source), then imported.
    api.post("/entities", {"project": "home", "source": "terraform", "kind": "service", "name": "notifier",
                           "metadata": {"displayName": "Notifier"}}, expect=201)
    write_tf(TF_BASE, TF_PORCH % "Porch identity provider", TF_IMPORT)
    tf("import", "-input=false", "deed_entity.notifier", "home/service/notifier")
    p = tf("plan", "-detailed-exitcode", "-input=false", check=False)
    # The imported resource's display name is not in the configuration, so one in-place update (removing it) is expected.
    tf("apply", "-auto-approve", "-input=false")
    p2 = tf("plan", "-detailed-exitcode", "-input=false", check=False)
    assert dn == "Porch identity provider" and p2.returncode == 0, p2.stdout[-800:]
    return {"updated_display_name": dn, "import": "deed_entity.notifier <- home/service/notifier", "plan_after_import_exit": p.returncode,
            "plan_after_reconcile_exit": p2.returncode}


@step(9, "Ownership, repository and Grafana links (incl. a Grafana API link binding)")
def s9():
    api = S["api"]
    e = api.get(f"/entities/{S['porch']}")
    rels = api.get(f"/relations?entity={S['porch']}&type=owned_by")["items"]
    # Grafana: service-account token from the dev Grafana, a grafana integration and a link binding.
    g = Client(0)
    g.base = "http://127.0.0.1:33000"
    auth = {"Authorization": "Basic " + base64.b64encode(b"admin:deed-dev-admin").decode()}
    sa = g.call("POST", "/serviceaccounts", {"name": f"deed-e2e-{int(time.time())}", "role": "Viewer"}, headers=auth,
                base="http://127.0.0.1:33000/api", expect=201)[1]
    key = g.call("POST", f"/serviceaccounts/{sa['id']}/tokens", {"name": f"deed-{int(time.time())}"}, headers=auth, base="http://127.0.0.1:33000/api",
                 expect=200)[1]["key"]
    api.post("/integrations", {"name": "grafana", "adapter": "grafana", "config": {"url": "http://127.0.0.1:33000", "insecure_http": True,
                                                                                    "allow_cidrs": ["127.0.0.1/32"]}, "secrets": {"token": key}}, expect=201)
    t = api.post("/integrations/grafana/test", expect=200)
    api.post("/bindings", {"name": "grafana-dashboards", "title": "Dashboards", "integration": "grafana", "kinds": ["service"],
                           "query": "search Deed", "shape": "link"}, expect=201)
    res = api.get(f"/entities/{S['porch']}/telemetry?binding=grafana-dashboards")["results"][0]
    links = [l["url"] for l in (res.get("data") or {}).get("links", [])]
    assert e["metadata"]["owner"] and rels and links, (e["metadata"], rels, res)
    return {"owner": e["metadata"]["owner"], "links": [l["type"] for l in e["metadata"]["links"]], "owned_by_relations": len(rels),
            "grafana_test": t["message"], "grafana_link_status": res["status"], "grafana_links": links[:2]}


def pulsepub(*args):
    return run([f"{BIN}/pulsepub", *args, "-url", "nats://127.0.0.1:34222"]).stdout.strip()


@step(10, "Ingest Pulse-compatible publisher/subscriber observations (tail + ingest consumer)")
def s10():
    api = S["api"]
    tail = api.post("/sources", {"project": "home", "name": "pulse-tail", "provider": "pulse_tail", "config": {"window_seconds": 15}}, expect=201)
    S["tail"] = tail["id"]
    agent = api.post("/sources", {"project": "home", "name": "agent", "provider": "push", "layer": "discovered",
                                  "config": {"pulse_source": "agent-1"}}, expect=201)
    S["agent"] = agent["id"]
    pub = pulsepub("publish", "-source", "porch", "-subject", "evt.porch.tenancy.tenant.updated.ten_01jb9w2v3x4y5z6a7b8c9d0%03d", "-type", "porch.tenancy.tenant_updated.v1", "-n", "60")
    batch = os.path.join(E2E, "agent-batch.json")
    with open(batch, "w") as f:
        json.dump({"entities": [{"kind": "container", "name": "notifier-1", "external_id": "c-notifier",
                                 "spec": {"image": "example/notifier", "state": "running"}}],
                   "relations": [{"type": "subscribes_to", "from": {"kind": "service", "name": "notifier"},
                                  "to": {"kind": "nats_subject", "name": "evt.porch.tenancy.tenant.updated.star"}}]}, f)
    ing = pulsepub("ingest", "-source", "agent-1", "-deed-source", agent["id"], "-file", batch)

    def edge():
        # The tail picks new sources up within 30 s and only sees messages published after it subscribed (read-only,
        # deliver-new), so traffic keeps flowing like a live publisher would.
        pulsepub("publish", "-source", "porch", "-subject", "evt.porch.tenancy.tenant.updated.ten_01jb9w2v3x4y5z6a7b8c9d0%03d", "-type", "porch.tenancy.tenant_updated.v1", "-n", "20")
        items = api.get(f"/relations?project=home&type=publishes&entity={S['porch']}")["items"]
        return items or None
    rels = wait_for(edge, "observed publishes edge from the tail", timeout=150, every=5)
    S["pub_rel"] = rels[0]["id"]
    c = wait_for(lambda: api.get("/entities?project=home&kind=container&q=notifier")["total"], "agent container via the ingest consumer")
    return {"published": pub, "ingest": ing, "publishes_edge": f"{rels[0]['from']['name']} -> {rels[0]['to']['name']}",
            "evidence_class": rels[0]["evidence_class"], "agent_containers": c}


@step(11, "Merge with the declared entity without losing declared ownership")
def s11():
    api = S["api"]
    e = api.get(f"/entities/{S['porch']}")
    facets = api.get(f"/entities/{S['porch']}/facets")
    prov = api.get(f"/entities/{S['porch']}/provenance")
    layers = sorted({f["layer"] for f in (facets if isinstance(facets, list) else facets.get("items", []))})
    owner_prov = prov.get("/metadata/owner") or prov.get("fields", {}).get("/metadata/owner")
    assert e["metadata"]["owner"] and e["status"]["declared"] and len(layers) >= 2, (layers, e["metadata"]["owner"])
    return {"owner": e["metadata"]["owner"], "declared": e["status"]["declared"], "facet_layers": layers,
            "owner_provenance": owner_prov, "last_seen_or_observed": bool(e["status"].get("observed")) or e["status"]["freshness"]}


@step(12, "Dashboard finding -> inventory -> detail -> focused graph (API path the UI uses)")
def s12():
    api = S["api"]
    # A certificate expiring in three days produces a critical finding.
    cert = api.post("/entities", {"project": "home", "kind": "certificate", "name": "id-example-com",
                                  "spec": {"common_name": "id.example.com", "not_after": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() + 3 * 86400))}},
                    expect=201)
    api.post("/relations", {"type": "secured_by_certificate", "from": {"id": S["porch"]}, "to": {"id": cert["entity_id"]}}, expect=201)
    api.call("POST", "/findings/evaluate", {}, expect=204)
    dash = api.get("/dashboard")
    fs = api.get("/findings?state=open&type=certificate_expiring")  # a list
    items = fs["items"] if isinstance(fs, dict) else fs
    f = next(x for x in items if x.get("entity_id") == cert["entity_id"])
    inv = api.get("/entities?project=home&kind=certificate")
    det = api.get(f"/entities/{f['entity_id']}")
    g = api.get(f"/graph/neighborhood?root={f['entity_id']}&depth=2")
    S["cert"] = cert["entity_id"]
    return {"dashboard_keys": sorted(dash.keys())[:8], "finding": f"{f['severity']} {f['type']}: {f['title']}", "inventory_total": inv["total"],
            "detail": det["metadata"]["name"], "graph_nodes": len(g["nodes"]), "graph_edges": len(g["edges"])}


@step(13, "Filter and group the graph; inspect provenance")
def s13():
    api = S["api"]
    filt = api.get(f"/graph/neighborhood?root={S['porch']}&depth=2&type=runs_on&type=owned_by")
    ov = api.get("/graph/overview?project=home&group_by=kind")
    prov = api.get(f"/entities/{S['porch']}/provenance")
    types = sorted({e["type"] for e in filt["edges"]})
    assert set(types) <= {"runs_on", "owned_by"}
    return {"filtered_edge_types": types, "overview_groups": len(ov.get("nodes", [])), "provenance_fields": len(prov if isinstance(prov, dict) else [])}


@step(14, "Operational summaries with freshness and failure states (telemetry, edge rates, sources)")
def s14():
    api = S["api"]
    cfg = lambda u: {"url": u, "insecure_http": True, "allow_cidrs": ["127.0.0.1/32"]}  # noqa: E731
    api.post("/integrations", {"name": "prometheus", "adapter": "prometheus", "config": cfg("http://127.0.0.1:39190")}, expect=201)
    api.post("/integrations", {"name": "loki", "adapter": "loki", "config": cfg("http://127.0.0.1:33100")}, expect=201)
    api.post("/integrations", {"name": "broken", "adapter": "prometheus", "config": cfg("http://127.0.0.1:1")}, expect=201)
    tests = {n: api.post(f"/integrations/{n}/test", expect=200) for n in ("prometheus", "loki", "broken")}
    api.post("/bindings", {"name": "prom-up", "title": "Prometheus up", "integration": "prometheus", "kinds": ["service"],
                           "query": 'max(up{job="prometheus"})', "role": "health", "thresholds": {"critical": 1, "direction": "below"}}, expect=201)
    api.post("/bindings", {"name": "error-logs", "title": "Error log lines", "integration": "loki", "kinds": ["service"],
                           "query": 'sum(count_over_time({app="{{name}}"} |= "error" [{{window}}]))',
                           "params": [{"name": "name", "type": "label", "from": "/metadata/name"}]}, expect=201)
    api.post("/bindings", {"name": "broken-up", "title": "Unreachable", "integration": "broken", "kinds": ["service"], "query": "up"}, expect=201)
    ns = str(time.time_ns())
    push = urllib.request.Request("http://127.0.0.1:33100/loki/api/v1/push", method="POST", headers={"Content-Type": "application/json"},
                                  data=json.dumps({"streams": [{"stream": {"app": "porch"}, "values": [[ns, "level=error msg=synthetic-e2e"],
                                                                                                         [ns[:-3] + "001", "level=error msg=synthetic-e2e"]]}]}).encode())
    urllib.request.urlopen(push, timeout=10)

    def tele():
        r = {x["binding"]: x for x in api.get(f"/entities/{S['porch']}/telemetry?window=15m")["results"]}
        return r if r.get("error-logs", {}).get("status") == "measured" and r.get("prom-up", {}).get("status") == "measured" else None
    r = wait_for(tele, "measured telemetry", timeout=60, every=3)
    rates = api.post("/telemetry/edge-rates", {"relation_ids": [S["pub_rel"]], "window": "15m"}, expect=200)["rates"]
    srcs = {s["name"]: s.get("freshness", {}).get("state") if isinstance(s.get("freshness"), dict) else s.get("freshness") for s in api.get("/sources?project=home")}
    api.call("POST", "/findings/evaluate", {}, expect=204)
    fl = api.get("/findings?state=open&type=integration_unreachable")
    unreachable = fl["items"] if isinstance(fl, dict) else fl
    assert r["broken-up"]["status"] == "unavailable" and rates and rates[0]["status"] in ("measured", "inferred")
    return {"integration_tests": {k: v["ok"] for k, v in tests.items()},
            "telemetry": {k: (v["status"], (v.get("data") or {}).get("value"), (v.get("health") or {}).get("state")) for k, v in r.items()},
            "edge_rate": {k: rates[0].get(k) for k in ("status", "rate", "unit", "basis")}, "source_freshness": srcs,
            "integration_unreachable_findings": len(unreachable)}


@step(15, "Conflict handling, interrupted snapshot and partial import safety")
def s15():
    api = S["api"]
    a = api.post("/sources", {"project": "home", "name": "inv-a", "provider": "push", "layer": "imported"}, expect=201)
    b = api.post("/sources", {"project": "home", "name": "inv-b", "provider": "push", "layer": "imported"}, expect=201)
    for src, os_ in ((a, "Debian 12"), (b, "Ubuntu 24.04")):
        api.post(f"/sources/{src['id']}/items", {"entities": [{"kind": "server", "name": "conflict-host", "spec": {"hostname": "conflict-host.example.com", "os": os_}}]},
                 expect=200)
    ent = api.get("/entities/lookup?project=home&kind=server&name=conflict-host")
    conflicts = api.get(f"/entities/{ent['metadata']['id']}/conflicts")
    nconf = len(conflicts if isinstance(conflicts, list) else conflicts.get("items", []))
    before = api.get(f"/entities?project=home&source={S['tf_src']}")["total"]
    snap = api.post(f"/sources/{S['tf_src']}/snapshots", {"complete": True}, expect=201)
    api.call("PUT", f"/sources/{S['tf_src']}/snapshots/{snap['id']}/batches/1",
             {"entities": [{"kind": "server", "name": "only-one", "external_id": "only-one"}]}, expect=200)
    api.post(f"/sources/{S['tf_src']}/snapshots/{snap['id']}/abort", expect=204)
    after_abort = api.get(f"/entities?project=home&source={S['tf_src']}")["total"]
    # A partial import of a reduced state never removes the resources it does not mention.
    state = json.load(open(os.path.join(ROOT, "internal/tfimport/testdata/state-v4.json")))
    state["resources"] = state["resources"][:2]
    reduced = os.path.join(E2E, "reduced.tfstate")
    json.dump(state, open(reduced, "w"))
    env = {"DEED_ENDPOINT": f"http://127.0.0.1:{PORT}", "DEED_SOURCE_TOKEN": S["tf_tok"]}
    part = json.loads(run([f"{BIN}/deed", "import", "terraform", "-json", "-source", S["tf_src"], "-file", reduced, "-partial"], env=env).stdout)["result"]
    # A complete import of the reduced state trips the mass-removal guard.
    guard = run([f"{BIN}/deed", "import", "terraform", "-source", S["tf_src"], "-file", reduced], env=env, check=False)
    after = api.get(f"/entities?project=home&source={S['tf_src']}")["total"]
    assert nconf >= 1 and after_abort == before == after and part["summary"]["removed"] == 0 and guard.returncode != 0
    return {"conflicts_on_os": nconf, "entities_before": before, "after_aborted_snapshot": after_abort, "partial_import_removed": part["summary"]["removed"],
            "complete_reduced_import": (guard.stdout + guard.stderr).strip()[-160:], "after": after}


@step(16, "Remove a Terraform declaration without deleting independent observations")
def s16():
    write_tf(TF_BASE, TF_IMPORT)
    tf("apply", "-auto-approve", "-input=false")
    api = S["api"]
    e = api.get(f"/entities/{S['porch']}")
    assert not e["status"].get("tombstonedAt") and not e["status"]["declared"]
    still = api.get(f"/relations?project=home&type=publishes&entity={S['porch']}")["items"]
    return {"entity_live": not e["status"].get("tombstonedAt"), "declared": e["status"]["declared"], "facets": e["status"]["facets"],
            "observed_publish_edges": len(still), "owner_after_removal": e["metadata"].get("owner")}


@step(17, "Audit records, metrics and alert delivery (Pulse event)")
def s17():
    api = S["api"]
    actions = [r["action"] for r in api.get("/audit?limit=200")]
    ver = api.post("/audit/verify", expect=200)
    _, metrics, _ = owner.call("GET", "/metrics", base=f"http://127.0.0.1:{MGMT}", raw=True)
    names = sorted({l.split("{")[0].split(" ")[0] for l in metrics.splitlines() if l.startswith("deed_")})
    alerts = api.get("/alerts?state=firing")
    items = alerts["items"] if isinstance(alerts, dict) else alerts
    got = pulsepub("wait", "-subject", "evt.deed.alert.alert.raised", "-timeout", "90s")
    assert ver["ok"] and items and "received" in got
    return {"audit_actions_sample": sorted(set(actions))[:12], "audit_chain": {"ok": ver["ok"], "events": ver["events"]},
            "metric_families": len(names), "metrics_sample": [n for n in names if n.startswith(("deed_reconcile", "deed_http_requests", "deed_alerts"))][:5],
            "firing_alerts": [f"{a['severity']} {a.get('title', '')}" for a in items][:3], "pulse_delivery": got}


@step(18, "Create an encrypted S3 backup (MinIO)")
def s18():
    api = S["api"]
    S["counts"] = {"entities": api.get("/entities?project=home&limit=1")["total"], "relations": api.get("/relations?project=home&limit=1")["total"]}
    api.post("/backups", expect=202)
    run_ = wait_for(lambda: next((r for r in api.get("/backups")["runs"] if r["result"] != "running"), None), "backup run", timeout=120)
    assert run_["result"] == "ok", run_
    b = api.get("/backups?remote=true")
    S["verify_key"] = b["verify_key"]
    rem = next(r for r in b["remote"] if r["id"] == run_["id"])
    return {"backup_id": run_["id"], "size_bytes": run_["size_bytes"], "remote_verified": rem["verified"], "complete": rem["complete"],
            "counts_at_backup": S["counts"]}


@step(19, "Restore into a clean instance and verify catalog integrity")
def s19():
    ident = os.path.join(E2E, "age-identity.txt")
    ver = json.loads(run([f"{BIN}/deed", "backup", "verify", "-config", os.path.join(E2E, "restore.yaml"), "-identity", ident,
                          "-verify-key", S["verify_key"], "-id", "latest"]).stdout)
    out = json.loads(run([f"{BIN}/deed", "restore", "-config", os.path.join(E2E, "restore.yaml"), "-identity", ident, "-verify-key", S["verify_key"],
                          "-id", "latest", "-kek-out", os.path.join(E2E, "kek", "restored"), "-yes"]).stdout)
    log = open(os.path.join(E2E, "restored.log"), "w")
    proc = subprocess.Popen([f"{BIN}/deed", "serve", "-config", os.path.join(E2E, "restore.yaml")], stdout=log, stderr=log)
    try:
        wait_for(lambda: owner.call("GET", "/readyz", base=f"http://127.0.0.1:{RMGMT}", raw=True)[0] == 200, "restored instance ready", timeout=60)
        r = Client(RPORT, bearer=S["token"])
        counts = {"entities": r.get("/entities?project=home&limit=1")["total"], "relations": r.get("/relations?project=home&limit=1")["total"]}
        chain = r.post("/audit/verify", expect=200)
        old_session = Client(RPORT)
        old_session.op = owner.op  # the owner's session cookie from the primary instance
        sess = old_session.call("GET", "/me")[0]
        restored = [x["action"] for x in r.get("/audit?limit=20")]
    finally:
        proc.terminate()
        proc.wait(timeout=30)
    assert counts == S["counts"] and chain["ok"] and "backup.restored" in restored
    return {"verify": {"ok": ver["ok"], "counts": ver["counts"]}, "restore_validation": out["validation"], "counts_restored": counts,
            "counts_at_backup": S["counts"], "audit_chain_restored": chain["ok"], "old_session_status": sess, "restore_audited": "backup.restored" in restored}


def secret_scan():
    hits = 0
    for root, _, files in os.walk(E2E):
        for f in files:
            p = os.path.join(root, f)
            if f.endswith((".db", ".db-wal", ".log", ".json", ".tfstate")) and "reduced" not in f:
                try:
                    if b"PLANTED-SECRET" in open(p, "rb").read():
                        hits += 1
                        print("    planted secret found in", p)
                except OSError:
                    pass
    return hits


for fn in (s1, s2, s3, s4, s5, s6, s7, s8, s9, s10, s11, s12, s13, s14, s15, s16, s17, s18, s19):
    fn()
leaks = secret_scan()
print(f"planted-secret scan of databases and logs: {leaks} hit(s)")
json.dump({"results": results, "planted_secret_hits": leaks, "finished": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())},
          open(os.path.join(E2E, "results.json"), "w"), indent=1)
failed = [r["step"] for r in results if not r["ok"]]
print(f"\n{len(results) - len(failed)}/{len(results)} steps passed" + (f"; failed: {failed}" if failed else ""))
sys.exit(1 if failed or leaks else 0)
