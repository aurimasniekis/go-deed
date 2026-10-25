#!/usr/bin/env python3
"""Driver for scripts/validate/github.sh (stdlib only; run with python3 -I).

Syncs a Deed github source against the live GitHub REST API for a few public repositories, anonymously (or with
GITHUB_TOKEN when set), and compares the catalog with what the API returns: repositories, latest releases, latest
deployment per environment, open and recently closed pull requests and issues. Every step prints PASS/FAIL with
evidence; results.json in VALIDATE_DIR records them."""
import base64
import hashlib
import hmac
import http.cookiejar
import json
import os
import re
import struct
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime, timedelta, timezone

WORK = os.environ["VALIDATE_DIR"]
DEED_URL = os.environ["DEED_URL_"]
REPOS = os.environ["GH_REPOS"].split()
GH_TOKEN = os.environ.get("GITHUB_TOKEN", "")
RELEASES, MAX_ITEMS, RETENTION_DAYS = 5, 30, 14
results = []
S = {}


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
            resp = self.op.open(req, timeout=300)
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


def gh(path, **params):
    q = "&".join(f"{k}={urllib.request.quote(str(v))}" for k, v in params.items())
    h = {"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28", "User-Agent": "deed-validate"}
    if GH_TOKEN:
        h["Authorization"] = "Bearer " + GH_TOKEN
    with urllib.request.urlopen(urllib.request.Request("https://api.github.com" + path + ("?" + q if q else ""), headers=h), timeout=60) as r:
        S["rate_remaining"] = int(r.headers.get("X-RateLimit-Remaining", "-1"))
        return json.loads(r.read())


def totp(secret):
    k = base64.b32decode(secret.upper() + "=" * (-len(secret) % 8))
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
                print(f"PASS {n:>2}. {title}\n        {json.dumps(ev, sort_keys=True)[:1500]}", flush=True)
                return ev
            except Exception as ex:  # noqa: BLE001 — every failure is reported
                results.append({"step": n, "title": title, "ok": False, "error": str(ex)[:2000], "seconds": round(time.time() - t0, 1)})
                print(f"FAIL {n:>2}. {title}\n        {ex}", flush=True)
                return None
        return wrapped
    return deco


def slug(s):
    """Mirrors internal/connectors slug()."""
    out = re.sub(r"[^a-z0-9._-]", "-", s.lower()).strip("-._")
    while "--" in out:
        out = out.replace("--", "-")
    return out[:200]


def parse(t):
    return datetime.fromisoformat(t.replace("Z", "+00:00")) if t else None


@step(1, "Deed: first owner (bootstrap link, password, TOTP), project and admin API token")
def deed_setup():
    owner = Client(DEED_URL)
    tok = open(os.path.join(WORK, "deed-setup-token")).read().strip()
    me = owner.post("/auth/setup/complete", {"token": tok, "email": "owner@example.com", "display_name": "Validation Owner",
                                             "password": base64.b32encode(os.urandom(15)).decode()}, expect=200)
    owner.csrf = me["csrf_token"]
    enr = owner.post("/me/mfa/totp", expect=200)
    owner.post("/me/mfa/totp/confirm", {"code": totp(enr["secret"])}, expect=200)
    owner.csrf = owner.get("/me")["csrf_token"]
    owner.post("/projects", {"name": "home", "display_name": "Home (validation)"}, expect=201)
    t = owner.post("/me/tokens", {"name": "validate", "installation_role": "admin"}, expect=201)
    S["deed"] = Client(DEED_URL, bearer=t["token"])
    return {"project": "home"}


@step(2, "Deed: create the github source (anonymous unless GITHUB_TOKEN) and run its test against api.github.com")
def deed_source():
    cfg = {"repositories": REPOS, "releases": RELEASES, "max_items_per_repo": MAX_ITEMS, "closed_retention_days": RETENTION_DAYS,
           "min_rate_remaining": 0}
    body = {"project": "home", "name": "github", "provider": "github", "config": cfg, "schedule_seconds": 0}
    if GH_TOKEN:
        body["secrets"] = {"token": GH_TOKEN}
    src = S["deed"].post("/sources", body, expect=201)
    S["src"] = src["id"]
    test = S["deed"].post(f"/sources/{src['id']}/test", expect=200)
    assert test["ok"], f"test not ok: {test}"
    return {"anonymous": not GH_TOKEN, "test": [(d["level"], d["code"]) for d in test["diagnostics"]]}


@step(3, "Deed: sync the repositories from the live API")
def sync():
    out = S["deed"].post(f"/sources/{S['src']}/sync", expect=200)
    run = out["run"]
    assert run.get("result") == "ok", f"run: {run}"
    S["synced_at"] = datetime.now(timezone.utc)
    return {"run": run.get("result"), "summary": (out.get("result") or {}).get("summary")}


def expected():
    cutoff = S["synced_at"] - timedelta(days=RETENTION_DAYS)
    want = set()
    for full in REPOS:
        r = gh(f"/repos/{full}")
        want.add("repository/" + slug(r["full_name"]))
        for rl in gh(f"/repos/{full}/releases", per_page=RELEASES)[:RELEASES]:
            if not rl["draft"]:
                want.add("release/" + slug(r["full_name"] + "-" + rl["tag_name"]))
        envs = {d["environment"] for d in gh(f"/repos/{full}/deployments", per_page=100) if d.get("environment")}
        want |= {"deployment/" + slug(r["full_name"] + "-" + e) for e in envs}
        for kind, path, prefix in (("pull_request", "pulls", "pr-"), ("issue", "issues", "issue-")):
            items = gh(f"/repos/{full}/{path}", state="open", sort="updated", direction="desc", per_page=MAX_ITEMS)[:MAX_ITEMS]
            extra = {"since": cutoff.strftime("%Y-%m-%dT%H:%M:%SZ")} if kind == "issue" else {}
            items += gh(f"/repos/{full}/{path}", state="closed", sort="updated", direction="desc", per_page=MAX_ITEMS, **extra)[:MAX_ITEMS]
            for it in items:
                if kind == "issue" and it.get("pull_request"):
                    continue
                if it["state"] == "closed" and (not it.get("closed_at") or parse(it["closed_at"]) < cutoff):
                    continue
                want.add(f"{kind}/" + slug(r["full_name"] + "-" + prefix + str(it["number"])))
    return want


def deed_all(path):
    items, cursor = [], None
    while True:
        page = S["deed"].get(path + "&limit=500" + (f"&cursor={urllib.request.quote(cursor)}" if cursor else ""))
        items += page["items"]
        cursor = page.get("next_cursor")
        if not cursor:
            return items


@step(4, "Deed: the catalog matches what the GitHub API returns (repositories, releases, deployments, PRs, issues)")
def compare():
    want = expected()
    ents = deed_all(f"/entities?project=home&source={S['src']}")
    got = {e["kind"] + "/" + e["metadata"]["name"] for e in ents}
    missing, unexpected = sorted(want - got), sorted(got - want)
    # Items that changed state between the sync and the comparison are the only acceptable difference.
    assert not missing and not unexpected, f"missing {missing[:20]}, unexpected {unexpected[:20]}"
    rels = deed_all(f"/relations?project=home&source={S['src']}")
    by_kind = {}
    for k in got:
        by_kind[k.split("/")[0]] = by_kind.get(k.split("/")[0], 0) + 1
    repo_edges = sum(1 for r in rels if r["to"]["kind"] == "repository" or r["from"]["kind"] == "repository")
    assert repo_edges >= len(got) - len(REPOS), f"only {repo_edges} relations touch a repository for {len(got)} entities"
    S["entities"] = len(got)
    return {"entities_by_kind": by_kind, "relations": len(rels), "relation_types": sorted({r["type"] for r in rels}),
            "rate_remaining_after": S.get("rate_remaining")}


@step(5, "Deed: a second sync is idempotent (no entity created or removed)")
def resync():
    out = S["deed"].post(f"/sources/{S['src']}/sync", expect=200)
    summ = (out.get("result") or {}).get("summary") or {}
    ents = deed_all(f"/entities?project=home&source={S['src']}")
    assert len(ents) == S["entities"], f"{len(ents)} entities after resync, {S['entities']} before"
    assert not summ.get("created") and not summ.get("removed"), f"summary {summ}"
    return {"summary": summ}


def main():
    deed_setup()
    if results[-1]["ok"]:
        deed_source()
        sync()
        compare()
        resync()
    with open(os.path.join(WORK, "results.json"), "w") as f:
        json.dump({"repositories": REPOS, "anonymous": not GH_TOKEN, "steps": results}, f, indent=2)
    failed = [r["step"] for r in results if not r["ok"]]
    print(("FAILED steps: " + ", ".join(map(str, failed))) if failed else f"ALL {len(results)} STEPS PASSED", flush=True)
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
