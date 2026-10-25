#!/usr/bin/env python3
"""Driver for scripts/validate/telemetry.sh (stdlib only; run with python3 -I).

Feeds a multi-tenant Grafana Mimir (remote-write push, two tenants) and a secured OpenSearch (HTTPS with the demo CA,
basic auth, a least-privilege reader) with deterministic data, configures Deed integrations and bindings of every
supported shape, and compares /entities/{id}/telemetry with the same queries sent directly to the backends. Then
checks the failure states: wrong or missing tenant, wrong credentials, a forbidden index, a query the backend rejects,
an unreachable endpoint, and a stale value after the backend goes away; no upstream response body may leak into a
Deed answer. Every step prints PASS/FAIL with evidence; results.json in VALIDATE_DIR records them."""
import base64
import hashlib
import hmac
import http.cookiejar
import json
import os
import re
import ssl
import struct
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone

WORK = os.environ["VALIDATE_DIR"]
DEED_URL = os.environ["DEED_URL_"]
DEED_MGMT = os.environ["DEED_MGMT_"]
MIMIR = "http://127.0.0.1:" + os.environ["MIMIR_PORT"]
OS_URL = "https://127.0.0.1:" + os.environ["OS_PORT"]
DEAD_PORT = os.environ["DEAD_PORT"]
MIMIR_CTR = os.environ["MIMIR_CTR"]
SECRETS = os.path.join(WORK, "secrets")
OS_CTX = ssl.create_default_context(cafile=os.path.join(SECRETS, "opensearch-root-ca.pem"))
TENANT_A, TENANT_B = "team-a", "team-b"
FRESH = 15  # binding freshness (the minimum Deed allows), so stale checks wait only seconds
results = []
S = {"errors": []}


def secret(name):
    with open(os.path.join(SECRETS, name)) as f:
        return f.read().strip()


class Client:
    def __init__(self, base, bearer=None):
        self.base, self.bearer, self.csrf = base, bearer, ""
        self.op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def call(self, method, path, body=None, expect=None):
        h = {"Accept": "application/json", "Origin": self.base}
        data = None
        if body is not None:
            data, h["Content-Type"] = json.dumps(body).encode(), "application/json"
        if self.csrf:
            h["X-Deed-CSRF"] = self.csrf
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


def raw_http(method, url, body=None, headers=None, ctx=None, auth=None):
    h = dict(headers or {})
    if auth:
        h["Authorization"] = "Basic " + base64.b64encode(f"{auth[0]}:{auth[1]}".encode()).decode()
    data = body
    if isinstance(body, (dict, list)):
        data, h["Content-Type"] = json.dumps(body).encode(), "application/json"
    req = urllib.request.Request(url, data=data, method=method, headers=h)
    try:
        with urllib.request.urlopen(req, timeout=60, context=ctx) as r:
            code, raw = r.status, r.read()
    except urllib.error.HTTPError as e:
        code, raw = e.code, e.read()
    try:
        return code, json.loads(raw)
    except ValueError:
        return code, raw.decode(errors="replace")


def mimir_query(path, tenant, **params):
    h = {"X-Scope-OrgID": tenant} if tenant else {}
    return raw_http("GET", MIMIR + "/prometheus/api/v1/" + path + "?" + urllib.parse.urlencode(params), headers=h)


def os_admin(method, path, body=None):
    if isinstance(body, str):
        return raw_http(method, OS_URL + path, body.encode(), {"Content-Type": "application/x-ndjson"}, OS_CTX, ("admin", secret("opensearch-admin")))
    return raw_http(method, OS_URL + path, body, None, OS_CTX, ("admin", secret("opensearch-admin")))


def os_reader(method, path, body=None):
    return raw_http(method, OS_URL + path, body, None, OS_CTX, ("deed-reader", secret("opensearch-reader")))


# --- Prometheus remote write 1.0: a protobuf WriteRequest compressed with snappy (block format, literals only). ---

def _varint(n):
    out = bytearray()
    while True:
        b, n = n & 0x7F, n >> 7
        out.append(b | (0x80 if n else 0))
        if not n:
            return bytes(out)


def _field(num, payload):
    """Length-delimited field (wire type 2)."""
    return _varint(num << 3 | 2) + _varint(len(payload)) + payload


def write_request(series):
    out = b""
    for labels, samples in series:
        ts = b"".join(_field(1, _field(1, k.encode()) + _field(2, labels[k].encode())) for k in sorted(labels))
        for t_ms, v in samples:
            # Sample{double value = 1; int64 timestamp = 2}
            ts += _field(2, _varint(1 << 3 | 1) + struct.pack("<d", v) + _varint(2 << 3) + _varint(t_ms))
        out += _field(1, ts)
    return out


def snappy_block(data):
    """Valid snappy block-format stream made of literal elements only (no compression, no dependency)."""
    out = bytearray(_varint(len(data)))
    for i in range(0, len(data), 65536):
        chunk = data[i:i + 65536]
        n = len(chunk) - 1
        if n < 60:
            out.append(n << 2)
        else:
            out.append(61 << 2)
            out += struct.pack("<H", n)
        out += chunk
    return bytes(out)


def remote_write(tenant, series):
    code, body = raw_http("POST", MIMIR + "/api/v1/push", snappy_block(write_request(series)), {
        "Content-Encoding": "snappy", "Content-Type": "application/x-protobuf", "X-Prometheus-Remote-Write-Version": "0.1.0",
        "X-Scope-OrgID": tenant})
    assert code in (200, 204), f"push for {tenant}: HTTP {code} {str(body)[:300]}"


def totp(sec):
    k = base64.b32decode(sec.upper() + "=" * (-len(sec) % 8))
    m = hmac.new(k, struct.pack(">Q", int(time.time() // 30)), hashlib.sha1).digest()
    o = m[-1] & 15
    return "%06d" % ((struct.unpack(">I", m[o:o + 4])[0] & 0x7FFFFFFF) % 1000000)


def step(n, title):
    def deco(fn):
        def wrapped(*a, **kw):
            t0 = time.time()
            try:
                ev = fn(*a, **kw) or {}
                results.append({"step": n, "title": title, "ok": True, "evidence": ev, "seconds": round(time.time() - t0, 1)})
                print(f"PASS {n:>2}. {title}\n        {json.dumps(ev, sort_keys=True)[:1800]}", flush=True)
                return ev
            except Exception as ex:  # noqa: BLE001 — every failure is reported
                results.append({"step": n, "title": title, "ok": False, "error": str(ex)[:3000], "seconds": round(time.time() - t0, 1)})
                print(f"FAIL {n:>2}. {title}\n        {ex}", flush=True)
                return None
        return wrapped
    return deco


def iso_s(t):
    return datetime.fromtimestamp(t, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def parse_ts(s):
    return datetime.fromisoformat(s.replace("Z", "+00:00")).timestamp()


def close(a, b):
    return a is not None and b is not None and abs(float(a) - float(b)) <= 1e-9 * max(1.0, abs(float(a)), abs(float(b)))


def tele(binding, window="1h"):
    """One binding's result for the checkout entity; records the error text for the leak check."""
    r = S["deed"].get(f"/entities/{S['entity']}/telemetry?window={window}&binding={binding}")["results"]
    assert len(r) == 1, f"{binding}: {len(r)} results"
    if r[0].get("error"):
        S["errors"].append(r[0]["error"])
    return r[0]


def brief(r):
    d = r.get("data") or {}
    out = {"status": r["status"]}
    for k in ("error", "cached"):
        if r.get(k):
            out[k] = r[k]
    if "value" in d:
        out["value"] = d["value"]
    if d.get("series"):
        out["series"] = [{"labels": s["labels"], "points": len(s["points"])} for s in d["series"]]
    if d.get("rows"):
        out["rows"] = d["rows"]
    if r.get("health"):
        out["health"] = r["health"]
    return out


# ------------------------------------------------------------------------------------------------------------------

@step(1, "Deed: first owner (bootstrap link, password, TOTP), project, admin API token and a service entity")
def deed_setup():
    owner = Client(DEED_URL)
    tok = secret("deed-setup-token")
    me = owner.post("/auth/setup/complete", {"token": tok, "email": "owner@example.com", "display_name": "Validation Owner",
                                             "password": base64.b32encode(os.urandom(15)).decode()}, expect=200)
    owner.csrf = me["csrf_token"]
    enr = owner.post("/me/mfa/totp", expect=200)
    owner.post("/me/mfa/totp/confirm", {"code": totp(enr["secret"])}, expect=200)
    owner.csrf = owner.get("/me")["csrf_token"]
    owner.post("/projects", {"name": "home", "display_name": "Home (validation)"}, expect=201)
    t = owner.post("/me/tokens", {"name": "validate", "installation_role": "admin"}, expect=201)
    S["deed"] = Client(DEED_URL, bearer=t["token"])
    e = S["deed"].post("/entities", {"project": "home", "kind": "service", "name": "checkout",
                                     "metadata": {"displayName": "Checkout (example.com)"}}, expect=201)
    S["entity"] = e.get("entity_id")
    assert S["entity"], f"entity id missing: {str(e)[:300]}"
    return {"project": "home", "entity": "service/checkout"}


@step(2, "Mimir: remote-write deterministic samples for two tenants; tenant isolation and the missing-tenant 401 hold directly")
def mimir_feed():
    end = int(time.time()) // 15 * 15
    ts = [end - i * 15 for i in range(400, -1, -1)]  # 100 minutes, oldest first
    ms = [t * 1000 for t in ts]
    a = [
        ({"__name__": "deed_validate_http_requests_total", "service": "checkout", "region": "eu-west"}, [(m, 30.0 * i) for i, m in enumerate(ms)]),
        ({"__name__": "deed_validate_http_requests_total", "service": "billing", "region": "us-east"}, [(m, 15.0 * i) for i, m in enumerate(ms)]),
        ({"__name__": "deed_validate_capacity", "service": "checkout", "region": "eu-west"}, [(m, 42.0) for m in ms]),
        ({"__name__": "deed_validate_capacity", "service": "billing", "region": "us-east"}, [(m, 17.0) for m in ms]),
        ({"__name__": "deed_validate_queue_depth", "service": "checkout"}, [(m, (t // 15) % 10 + 0.5) for t, m in zip(ts, ms)]),
    ]
    b = [({"__name__": "deed_validate_capacity", "service": "checkout", "region": "ap-south"}, [(m, 7.0) for m in ms])]
    remote_write(TENANT_A, a)
    remote_write(TENANT_B, b)
    S["pushed_until"] = end
    q = 'max(deed_validate_capacity{service="checkout"})'
    for _ in range(30):
        ca, ra = mimir_query("query", TENANT_A, query=q)
        if ca == 200 and ra["data"]["result"]:
            break
        time.sleep(1)
    _, rb = mimir_query("query", TENANT_B, query=q)
    _, rn = mimir_query("query", "unknown-tenant", query=q)
    cn, bn = mimir_query("query", None, query=q)
    va, vb = float(ra["data"]["result"][0]["value"][1]), float(rb["data"]["result"][0]["value"][1])
    assert va == 42 and vb == 7 and rn["data"]["result"] == [] and cn == 401, (va, vb, rn, cn)
    return {"series_team_a": len(a), "series_team_b": len(b), "samples_per_series": len(ms), "team_a_capacity": va, "team_b_capacity": vb,
            "unknown_tenant_result": rn["data"]["result"], "no_tenant": [cn, str(bn).strip()[:40]]}


@step(3, "OpenSearch: least-privilege reader (read on logs-deed-*, metrics-deed-*) and timestamped log documents")
def os_feed():
    role = {"cluster_permissions": [], "index_permissions": [{"index_patterns": ["logs-deed-*", "metrics-deed-*"], "allowed_actions": ["read"]}]}
    for path, body in (("roles/deed_telemetry_reader", role), ("internalusers/deed-reader", {"password": secret("opensearch-reader")}),
                       ("rolesmapping/deed_telemetry_reader", {"users": ["deed-reader"]})):
        c, r = os_admin("PUT", "/_plugins/_security/api/" + path, body)
        assert c in (200, 201), f"{path}: {c} {r}"
    for idx, props in (("logs-deed-app", {"@timestamp": {"type": "date"}, "service": {"type": "keyword"}, "level": {"type": "keyword"},
                                          "message": {"type": "text"}}),
                       ("metrics-deed-epoch", {"ts": {"type": "date", "format": "epoch_millis"}, "service": {"type": "keyword"}}),
                       ("logs-other", {"@timestamp": {"type": "date"}, "service": {"type": "keyword"}})):
        c, r = os_admin("PUT", "/" + idx, {"settings": {"number_of_replicas": 0}, "mappings": {"properties": props}})
        assert c == 200, f"create {idx}: {c} {r}"
    now = int(time.time())
    lines, want = [], {"errors": 0, "logs": 0, "epoch": 0}
    for i in range(96):  # every 30 s between now-2m and now-49.5m: inside any 1h window Deed aligns to the minute
        t = now - 120 - i * 30
        svc = "billing" if i % 3 == 0 else "checkout"
        lvl = "error" if i % 4 == 0 else "info"
        lines += [{"index": {"_index": "logs-deed-app"}}, {"@timestamp": iso_s(t), "service": svc, "level": lvl, "message": f"request {i} for example.com"}]
        want["logs"] += svc == "checkout"
        want["errors"] += svc == "checkout" and lvl == "error"
    for i in range(10):  # outside the window
        lines += [{"index": {"_index": "logs-deed-app"}}, {"@timestamp": iso_s(now - 3 * 3600 - i), "service": "checkout", "level": "error", "message": "old"}]
    for i in range(12):
        lines += [{"index": {"_index": "metrics-deed-epoch"}}, {"ts": (now - 300 - i * 120) * 1000, "service": "checkout"}]
        want["epoch"] += 1
    for i in range(5):
        lines += [{"index": {"_index": "logs-other"}}, {"@timestamp": iso_s(now - 600), "service": "checkout"}]
    c, r = os_admin("POST", "/_bulk?refresh=true", "".join(json.dumps(x) + "\n" for x in lines))
    assert c == 200 and not r["errors"], f"bulk: {c} {str(r)[:400]}"
    S["os_want"] = want
    rc, _ = os_reader("POST", "/logs-deed-*/_count", {"query": {"term": {"service": "checkout"}}})
    root_c, root_b = os_reader("GET", "/")
    oc, ob = os_reader("POST", "/logs-other/_count", {})
    bc, _ = raw_http("GET", OS_URL + "/", ctx=OS_CTX, auth=("deed-reader", "wrong-" + secret("opensearch-reader")))
    assert rc == 200 and root_c == 403 and oc == 403 and bc == 401, (rc, root_c, oc, bc)
    return {"documents": len(lines) // 2, "expected_in_window": want, "reader": {"count_logs": rc, "GET /": root_c, "logs-other": oc,
                                                                                 "wrong_password": bc},
            "reader_GET_root_reason": str(root_b)[:120]}


def local(url, **extra):
    cfg = {"url": url, "allow_cidrs": ["127.0.0.1/32"]}
    if url.startswith("http:"):
        cfg["insecure_http"] = True
    cfg.update(extra)
    return cfg


@step(4, "Deed: integrations for Mimir (tenant header) and OpenSearch (HTTPS, basic auth) and their connectivity tests")
def integrations():
    api = S["deed"]
    reader = {"username": "deed-reader", "password": secret("opensearch-reader")}
    defs = [
        ("mimir", "prometheus", local(MIMIR + "/prometheus", tenant=TENANT_A), None),
        ("mimir-team-b", "prometheus", local(MIMIR + "/prometheus", tenant=TENANT_B), None),
        ("mimir-unknown-tenant", "prometheus", local(MIMIR + "/prometheus", tenant="unknown-tenant"), None),
        ("mimir-no-tenant", "prometheus", local(MIMIR + "/prometheus"), None),
        ("mimir-unreachable", "prometheus", local(f"http://127.0.0.1:{DEAD_PORT}/prometheus", tenant=TENANT_A), None),
        ("opensearch", "opensearch", local(OS_URL), reader),
        ("opensearch-epoch", "opensearch", local(OS_URL, time_field="ts"), reader),
        ("opensearch-bad-password", "opensearch", local(OS_URL), {"username": "deed-reader", "password": "wrong-" + secret("opensearch-reader")}),
        ("opensearch-unreachable", "opensearch", local(f"https://127.0.0.1:{DEAD_PORT}"), reader),
    ]
    for name, adapter, cfg, sec in defs:
        body = {"name": name, "adapter": adapter, "config": cfg}
        if sec:
            body["secrets"] = sec
        api.post("/integrations", body, expect=201)
    tests = {}
    for name, *_ in defs:
        t = api.post(f"/integrations/{name}/test", expect=200)
        tests[name] = [t["ok"], t["message"]]
        S["errors"].append(t["message"])
    want = {"mimir": True, "mimir-team-b": True, "mimir-unknown-tenant": True, "mimir-no-tenant": False, "mimir-unreachable": False,
            "opensearch": True, "opensearch-epoch": True, "opensearch-bad-password": False, "opensearch-unreachable": False}
    bad = {k: tests[k] for k, v in want.items() if tests[k][0] != v}
    assert not bad, f"unexpected test outcomes: {bad}"
    assert "(version " in tests["mimir"][1], tests["mimir"]
    # The least-privilege reader cannot read the version (cluster:monitor/main) but its credentials are accepted.
    assert "credentials were accepted" in tests["opensearch"][1], tests["opensearch"]
    assert "tenant (X-Scope-OrgID)" in tests["mimir-no-tenant"][1], tests["mimir-no-tenant"]
    assert "(401)" in tests["opensearch-bad-password"][1], tests["opensearch-bad-password"]
    listed = json.dumps(api.get("/integrations"))
    assert secret("opensearch-reader") not in listed, "a secret came back from GET /integrations"
    return {"tests": tests}


def binding(name, integration, query, shape="scalar", **extra):
    body = {"name": name, "title": name, "integration": integration, "kinds": ["service"], "query": query, "shape": shape,
            "params": [{"name": "svc", "type": "label", "from": "/metadata/name"}] if "{{svc}}" in query else [], "freshness_seconds": FRESH}
    body.update(extra)
    return S["deed"].call("POST", "/bindings", body)


def mk(name, integration, query, shape="scalar", **extra):
    code, js = binding(name, integration, query, shape, **extra)
    assert code == 201, f"binding {name}: HTTP {code} {str(js)[:300]}"


@step(5, "Deed: bindings of each shape the adapters support (Mimir scalar/health/timeseries/table, OpenSearch scalar/timeseries)")
def bindings():
    svc = 'service="{{svc}}"'
    mk("mimir-rps", "mimir", f"sum(rate(deed_validate_http_requests_total{{{svc}}}[5m]))", unit="req/s")
    mk("mimir-requests-window", "mimir", f"sum(increase(deed_validate_http_requests_total{{{svc}}}[{{{{window}}}}]))", unit="requests")
    mk("mimir-capacity", "mimir", f"max(deed_validate_capacity{{{svc}}})", role="health", thresholds={"warning": 50, "critical": 20, "direction": "below"})
    mk("mimir-queue", "mimir", f"max(deed_validate_queue_depth{{{svc}}})", "timeseries")
    mk("mimir-capacity-table", "mimir", "max by (service, region) (deed_validate_capacity)", "table")
    mk("mimir-team-b-capacity", "mimir-team-b", f"max(deed_validate_capacity{{{svc}}})")
    mk("mimir-unknown-tenant", "mimir-unknown-tenant", f"max(deed_validate_capacity{{{svc}}})")
    mk("mimir-no-tenant", "mimir-no-tenant", f"max(deed_validate_capacity{{{svc}}})")
    mk("mimir-rejected", "mimir", f"sum(deed_validate_capacity{{{svc}}}) bogus_token")
    mk("mimir-multi", "mimir", "deed_validate_capacity")
    mk("mimir-unreachable", "mimir-unreachable", f"max(deed_validate_capacity{{{svc}}})")
    flt = '{"bool": {"filter": [{"term": {"service": "{{svc}}"}}, {"term": {"level": "error"}}]}}'
    mk("os-errors", "opensearch", "logs-deed-* " + flt, unit="errors")
    mk("os-logs", "opensearch", 'logs-deed-* {"term": {"service": "{{svc}}"}}', "timeseries", unit="docs")
    mk("os-epoch", "opensearch-epoch", 'metrics-deed-epoch {"term": {"service": "{{svc}}"}}')
    mk("os-epoch-series", "opensearch-epoch", 'metrics-deed-epoch {"term": {"service": "{{svc}}"}}', "timeseries")
    mk("os-bad-password", "opensearch-bad-password", "logs-deed-* " + flt)
    mk("os-forbidden-index", "opensearch", 'logs-other {"match_all": {}}')
    mk("os-rejected", "opensearch", 'logs-deed-* {"no_such_query": {"x": 1}}')
    mk("os-no-index", "opensearch", 'nomatch-deed-* {"match_all": {}}', "timeseries")
    mk("os-unreachable", "opensearch-unreachable", "logs-deed-* " + flt)
    refused = {}
    for name, integ, q, shape in (("os-table", "opensearch", 'logs-deed-* {"match_all": {}}', "table"),
                                  ("os-script", "opensearch", 'logs-deed-* {"script": {"script": "1"}}', "scalar"),
                                  ("mimir-link", "mimir", "up", "link")):
        code, js = binding(name, integ, q, shape)
        refused[name] = code
        assert code in (400, 422), f"{name} ({shape}) was accepted: {code}"
    return {"bindings": len(S["deed"].get("/bindings")), "refused_on_save": refused}


@step(6, "Mimir scalar, health and {{window}} bindings match direct instant queries for tenant team-a")
def mimir_scalars():
    out = {}
    for name, q in (("mimir-rps", 'sum(rate(deed_validate_http_requests_total{service="checkout"}[5m]))'),
                    ("mimir-requests-window", 'sum(increase(deed_validate_http_requests_total{service="checkout"}[1h]))'),
                    ("mimir-capacity", 'max(deed_validate_capacity{service="checkout"})')):
        r = tele(name)
        at = int(time.time()) // 60 * 60  # Deed's instant time: now truncated to the 1h window's 60 s step
        _, d = mimir_query("query", TENANT_A, query=q, time=at)
        direct = float(d["data"]["result"][0]["value"][1])
        assert r["status"] == "measured" and close(r["data"]["value"], direct), f"{name}: deed {brief(r)} direct {direct}"
        out[name] = {"deed": r["data"]["value"], "direct": direct, "status": r["status"]}
    h = tele("mimir-capacity").get("health") or {}
    # 30 per 15 s is 2/s (PromQL rate arithmetic leaves the last bit off); 1h × 2/s = 7200.
    assert abs(out["mimir-rps"]["deed"] - 2) < 1e-9 and close(out["mimir-requests-window"]["deed"], 7200), out
    assert out["mimir-capacity"]["deed"] == 42 and h.get("state") == "warning", (out, h)
    out["mimir-capacity"]["health"] = h
    return out


@step(7, "Mimir timeseries binding matches a direct query_range point for point; table binding matches the direct vector")
def mimir_series_table():
    r = tele("mimir-queue")
    assert r["status"] == "measured", brief(r)
    series = r["data"]["series"]
    assert len(series) == 1, brief(r)
    pts = series[0]["points"]
    t0, t1 = parse_ts(pts[0]["t"]), parse_ts(pts[-1]["t"])
    _, d = mimir_query("query_range", TENANT_A, query='max(deed_validate_queue_depth{service="checkout"})', start=int(t0), end=int(t1), step=60)
    direct = [(float(t), float(v)) for t, v in d["data"]["result"][0]["values"]]
    got = [(parse_ts(p["t"]), p["v"]) for p in pts]
    assert len(got) == len(direct) and all(a[0] == b[0] and close(a[1], b[1]) for a, b in zip(got, direct)), \
        f"points differ: deed {got[:3]}…{len(got)} direct {direct[:3]}…{len(direct)}"
    assert len({v for _, v in got}) > 1, "the series is flat; the comparison would prove little"
    tb = tele("mimir-capacity-table")
    at = int(time.time()) // 60 * 60
    _, dv = mimir_query("query", TENANT_A, query="max by (service, region) (deed_validate_capacity)", time=at)
    want = sorted(({**s["metric"], "value": "%g" % float(s["value"][1])} for s in dv["data"]["result"]), key=lambda x: x["service"])
    rows = sorted(tb["data"]["rows"], key=lambda x: x["service"])
    assert tb["status"] == "measured" and rows == want, f"table: deed {rows} direct {want}"
    return {"timeseries": {"points": len(got), "first": pts[0], "last": pts[-1], "step_s": got[1][0] - got[0][0], "equal_to_direct": True},
            "table_rows": rows}


@step(8, "Mimir tenancy and failures: team-b sees its own value, unknown tenant → no data, no tenant → 401, rejected and multi-series queries")
def mimir_failures():
    b, u, n = tele("mimir-team-b-capacity"), tele("mimir-unknown-tenant"), tele("mimir-no-tenant")
    rej, multi, unr = tele("mimir-rejected"), tele("mimir-multi"), tele("mimir-unreachable")
    assert b["status"] == "measured" and b["data"]["value"] == 7, brief(b)
    assert u["status"] == "unavailable" and u["error"] == "no data", brief(u)
    assert n["status"] == "unavailable" and "(401)" in n["error"] and "tenant (X-Scope-OrgID)" in n["error"], brief(n)
    assert rej["status"] == "unavailable" and "rejected the query (400)" in rej["error"], brief(rej)
    assert multi["status"] == "unavailable" and "aggregate" in multi["error"], brief(multi)
    assert unr["status"] == "unavailable" and unr["error"], brief(unr)
    return {k: brief(v) for k, v in (("team-b", b), ("unknown-tenant", u), ("no-tenant", n), ("rejected", rej), ("multi-series", multi),
                                     ("unreachable", unr))}


def os_body(dsl, start, end, field="@timestamp"):
    return {"bool": {"filter": [dsl, {"range": {field: {"gte": iso_s(start), "lte": iso_s(end), "format": "strict_date_optional_time"}}}]}}


@step(9, "OpenSearch scalar and timeseries bindings match direct _count/_search (date_histogram) answers")
def os_compare():
    want = S["os_want"]
    r = tele("os-errors")
    end = int(time.time()) // 60 * 60
    flt = {"bool": {"filter": [{"term": {"service": "checkout"}}, {"term": {"level": "error"}}]}}
    _, d = os_reader("POST", "/logs-deed-*/_count", {"query": os_body(flt, end - 3600, end)})
    assert r["status"] == "measured" and r["data"]["value"] == d["count"] == want["errors"], f"deed {brief(r)} direct {d} want {want['errors']}"
    ts = tele("os-logs")
    assert ts["status"] == "measured", brief(ts)
    pts = ts["data"]["series"][0]["points"]
    t0, t1 = int(parse_ts(pts[0]["t"])), int(parse_ts(pts[-1]["t"]))
    body = {"size": 0, "track_total_hits": False, "query": os_body({"term": {"service": "checkout"}}, t0, t1),
            "aggs": {"t": {"date_histogram": {"field": "@timestamp", "fixed_interval": "60s", "min_doc_count": 0,
                                              "extended_bounds": {"min": t0 * 1000, "max": t1 * 1000}}}}}
    _, sd = os_reader("POST", "/logs-deed-*/_search", body)
    direct = [(b["key"] / 1000, float(b["doc_count"])) for b in sd["aggregations"]["t"]["buckets"]]
    got = [(parse_ts(p["t"]), p["v"]) for p in pts]
    assert got == direct, f"buckets differ: deed {got[:3]}…{len(got)} direct {direct[:3]}…{len(direct)}"
    assert sum(v for _, v in got) == want["logs"], (sum(v for _, v in got), want["logs"])
    return {"scalar": {"deed": r["data"]["value"], "direct": d["count"], "expected": want["errors"]},
            "timeseries": {"buckets": len(got), "docs": sum(v for _, v in got), "expected_docs": want["logs"], "equal_to_direct": True,
                           "labels": ts["data"]["series"][0]["labels"]}}


@step(10, "OpenSearch time_field with a non-default date format (epoch_millis): scalar and timeseries are measured")
def os_epoch():
    r, s = tele("os-epoch"), tele("os-epoch-series")
    assert r["status"] == "measured" and r["data"]["value"] == S["os_want"]["epoch"], brief(r)
    assert s["status"] == "measured" and sum(p["v"] for p in s["data"]["series"][0]["points"]) == S["os_want"]["epoch"], brief(s)
    return {"scalar": brief(r), "timeseries": brief(s)}


@step(11, "OpenSearch failures: wrong password → 401, index outside the reader's role → 403, rejected DSL, no matching index, unreachable")
def os_failures():
    bp, fb, rej, ni, unr = tele("os-bad-password"), tele("os-forbidden-index"), tele("os-rejected"), tele("os-no-index"), tele("os-unreachable")
    assert bp["status"] == "unavailable" and "401" in bp["error"], brief(bp)
    assert fb["status"] == "unavailable" and "403" in fb["error"], brief(fb)
    assert rej["status"] == "unavailable" and "rejected the query (400)" in rej["error"], brief(rej)
    assert ni["status"] == "unavailable" and ni["error"] == "no data", brief(ni)
    assert unr["status"] == "unavailable" and unr["error"], brief(unr)
    return {k: brief(v) for k, v in (("bad-password", bp), ("forbidden-index", fb), ("rejected", rej), ("no-index", ni), ("unreachable", unr))}


@step(12, "Stale: rotate the OpenSearch reader's password and stop Mimir; the last good values come back as stale with a safe error")
def stale():
    before_os, before_m = tele("os-errors"), tele("mimir-rps")
    assert before_os["status"] == before_m["status"] == "measured", (brief(before_os), brief(before_m))
    c, r = os_admin("PUT", "/_plugins/_security/api/internalusers/deed-reader", {"password": secret("opensearch-reader-rotated")})
    assert c == 200, (c, r)
    subprocess.run(["docker", "stop", "-t", "5", MIMIR_CTR], check=True, capture_output=True)
    time.sleep(FRESH + 2)
    after_os, after_m = tele("os-errors"), tele("mimir-rps")
    assert after_os["status"] == "stale" and after_os["data"]["value"] == before_os["data"]["value"] and "401" in after_os["error"], brief(after_os)
    assert after_m["status"] == "stale" and after_m["data"]["value"] == before_m["data"]["value"] and after_m["error"], brief(after_m)
    tests = {n: S["deed"].post(f"/integrations/{n}/test", expect=200) for n in ("mimir", "opensearch")}
    for t in tests.values():
        S["errors"].append(t["message"])
    assert not any(t["ok"] for t in tests.values()), tests
    return {"opensearch": brief(after_os), "mimir": brief(after_m), "measured_at_kept": after_m.get("measured_at") == before_m.get("measured_at"),
            "retest": {n: [t["ok"], t["message"]] for n, t in tests.items()}}


LEAK_MARKERS = ["no org id", "security_exception", "parsing_exception", "parse error", "bad_data", "Unauthorized", "unknown query",
                "no_such_query", "bogus_token", "no permissions", "x_content", "root_cause", "{", "Exception"]


@step(13, "No upstream body, credential or tenant detail leaks into Deed errors, the integration list, or Deed's log")
def leaks():
    secrets_ = [secret(n) for n in ("opensearch-admin", "opensearch-reader", "opensearch-reader-rotated")]
    hits = [(e, m) for e in S["errors"] for m in LEAK_MARKERS + secrets_ if m in e]
    assert not hits, f"leaked: {hits[:5]}"
    with open(os.path.join(WORK, "deed.log")) as f:
        log = f.read()
    in_log = [i for i, s in enumerate(secrets_) if s in log]
    assert not in_log, f"secret #{in_log} found in deed.log"
    its = S["deed"].get("/integrations")
    last = {i["name"]: i.get("last_error", "") for i in its}
    hits = [(n, e) for n, e in last.items() for m in LEAK_MARKERS + secrets_ if e and m in e]
    assert not hits, f"leaked via last_error: {hits}"
    return {"distinct_errors": sorted(set(S["errors"])), "integration_last_errors": {k: v for k, v in last.items() if v}}


@step(14, "Deed metrics: deed_telemetry_integration_up and deed_telemetry_queries_total for both adapters")
def metrics():
    with urllib.request.urlopen(DEED_MGMT + "/metrics", timeout=30) as r:
        text = r.read().decode()
    up = sorted(ln for ln in text.splitlines() if ln.startswith("deed_telemetry_integration_up{"))
    q = sorted(ln for ln in text.splitlines() if ln.startswith("deed_telemetry_queries_total{"))
    assert any('adapter="opensearch"' in ln for ln in q) and any('adapter="prometheus"' in ln for ln in q), q
    assert any(re.search(r'integration="opensearch-bad-password"\} 0', ln) for ln in up), up
    return {"integration_up": up, "queries_total": q}


def main():
    deed_setup()
    if results[-1]["ok"]:
        for fn in (mimir_feed, os_feed, integrations, bindings, mimir_scalars, mimir_series_table, mimir_failures, os_compare, os_epoch,
                   os_failures, stale, leaks, metrics):
            fn()
    with open(os.path.join(WORK, "results.json"), "w") as f:
        json.dump({"mimir_image": os.environ.get("MIMIR_IMAGE"), "opensearch_image": os.environ.get("OPENSEARCH_IMAGE"), "steps": results}, f, indent=2)
    failed = [r["step"] for r in results if not r["ok"]]
    print(("FAILED steps: " + ", ".join(map(str, failed))) if failed else f"ALL {len(results)} STEPS PASSED", flush=True)
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
