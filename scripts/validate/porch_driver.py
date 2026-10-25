#!/usr/bin/env python3
"""Drives scripts/validate/porch.sh (stdlib only; run with python3 -I).

  porch_driver.py porch   bootstrap a fresh Porch through its management API (owner, tokens, fake Google provider,
                          tenants, domain rules, groups, the Deed OIDC client, extra applications, service accounts)
  porch_driver.py deed    validate Deed against it: browser-less authorization-code SSO flows and the porch connector

Environment (exported by porch.sh): VALIDATE_DIR, P_PORT, IDP_PORT, D_PORT, D_MGMT. Secrets are written to VALIDATE_DIR
with mode 0600; state.json carries IDs between the phases. Prints PASS/FAIL per step; exits 1 on any failure.
"""
import base64, hashlib, hmac, http.client, json, os, struct, sys, time, urllib.parse

DIR = os.environ["VALIDATE_DIR"]
P_PORT, IDP_PORT, D_PORT = (int(os.environ[k]) for k in ("P_PORT", "IDP_PORT", "D_PORT"))
PORCH = f"http://127.0.0.1:{P_PORT}"
DEED = f"http://127.0.0.1:{D_PORT}"
STATE = os.path.join(DIR, "state.json")
results = []


def write_private(name, content):
    fd = os.open(os.path.join(DIR, name), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        f.write(content)


def read(name):
    with open(os.path.join(DIR, name)) as f:
        return f.read().strip()


def load_state():
    with open(STATE) as f:
        return json.load(f)


class Browser:
    """A minimal cookie-keeping user agent. All servers run on 127.0.0.1, and browsers do not isolate cookies by port,
    so one jar serves every origin (cookie names do not collide: deed_*, __Host-porch_*, porch_*, fakeidp_*)."""

    def __init__(self):
        self.cookies = {}
        self.trace = []

    def request(self, method, url, body=None, headers=None):
        u = urllib.parse.urlsplit(url)
        conn = http.client.HTTPConnection(u.hostname, u.port, timeout=30)
        h = {"User-Agent": "deed-validate/1"}
        if self.cookies:
            h["Cookie"] = "; ".join(f"{k}={v}" for k, v in self.cookies.items())
        h.update(headers or {})
        path = u.path + ("?" + u.query if u.query else "")
        conn.request(method, path or "/", body=body, headers=h)
        r = conn.getresponse()
        data = r.read()
        for k, v in r.getheaders():
            if k.lower() == "set-cookie":
                name, _, rest = v.partition("=")
                val = rest.split(";", 1)[0]
                attrs = rest.lower()
                if val == "" or "max-age=0" in attrs or "max-age=-" in attrs:
                    self.cookies.pop(name.strip(), None)
                else:
                    self.cookies[name.strip()] = val
        conn.close()
        self.trace.append(f"{method} {u.port}{u.path} -> {r.status}")
        return r.status, dict((k.lower(), v) for k, v in r.getheaders()), data

    def navigate(self, url, subject, stop_at_deed_callback=False, max_hops=25):
        """Follows a browser navigation: answers Porch's /login page by starting the Google sign-in and the fake Google
        picker by choosing subject. Stops at the first Deed page that is not /auth/oidc/* (or, with
        stop_at_deed_callback, before the Deed callback) and returns that URL."""
        method, body, headers = "GET", None, None
        for _ in range(max_hops):
            u = urllib.parse.urlsplit(url)
            if u.port == D_PORT and not u.path.startswith("/auth/oidc/"):
                return url
            if stop_at_deed_callback and u.port == D_PORT and u.path.endswith("/callback"):
                return url
            st, hdr, data = self.request(method, url, body, headers)
            method, body, headers = "GET", None, None
            if st in (301, 302, 303, 307, 308):
                url = urllib.parse.urljoin(url, hdr["location"])
                continue
            if st == 200 and u.port == P_PORT and u.path == "/login":
                q = urllib.parse.parse_qs(u.query)
                if "error" in q:
                    raise AssertionError(f"Porch sign-in failed: {u.query}")
                url = PORCH + "/login/google?" + urllib.parse.urlencode({"return_to": q.get("return_to", ["/"])[0]})
                continue
            if st == 200 and u.port == IDP_PORT and u.path == "/authorize":
                form = dict(urllib.parse.parse_qsl(u.query))
                form["subject"] = subject
                method, body = "POST", urllib.parse.urlencode(form)
                headers = {"Content-Type": "application/x-www-form-urlencoded"}
                continue
            if st == 200 and u.port == P_PORT and not stop_at_deed_callback:
                return url  # a Porch page (launcher) after a Porch-only sign-in
            raise AssertionError(f"unexpected {st} at {url}: {data[:300]!r}")
        raise AssertionError("too many redirects: " + " | ".join(self.trace[-8:]))


def api(base, method, path, body=None, token=None, cookies=None, csrf=None, csrf_header=None, headers=None, expect=None):
    u = urllib.parse.urlsplit(base)
    conn = http.client.HTTPConnection(u.hostname, u.port, timeout=60)
    h = {"Accept": "application/json", "Origin": base}
    if body is not None:
        h["Content-Type"] = "application/json"
    if token:
        h["Authorization"] = "Bearer " + token
    if cookies:
        h["Cookie"] = "; ".join(f"{k}={v}" for k, v in cookies.items())
    if csrf:
        h[csrf_header] = csrf
    h.update(headers or {})
    conn.request(method, "/api/v1" + path, body=None if body is None else json.dumps(body), headers=h)
    r = conn.getresponse()
    raw = r.read()
    hdr = r.getheaders()
    conn.close()
    out = json.loads(raw) if raw and raw[:1] in (b"{", b"[") else raw.decode(errors="replace")
    if expect is not None and r.status != expect:
        raise AssertionError(f"{method} {base}{path}: HTTP {r.status} (want {expect}): {str(out)[:500]}")
    return r.status, out, hdr


def items(x):
    return x["items"] if isinstance(x, dict) else x


def step(title):
    def deco(fn):
        t0 = time.time()
        try:
            ev = fn() or {}
            results.append({"title": title, "ok": True, "evidence": ev, "seconds": round(time.time() - t0, 1)})
            print(f"PASS {title}\n     {json.dumps(ev)[:900]}", flush=True)
        except Exception as ex:  # noqa: BLE001 — every failure is reported; later steps still run where possible
            results.append({"title": title, "ok": False, "error": str(ex)[:1500], "seconds": round(time.time() - t0, 1)})
            print(f"FAIL {title}\n     {ex}", flush=True)
        return fn
    return deco


# ---------------------------------------------------------------------------------------------------------------- porch
def porch_phase():
    def P(method, path, body=None, expect=None, token=None):
        return api(PORCH, method, path, body, token=token or S["admin"], expect=expect)[1]

    S = {}
    st, me, hdr = api(PORCH, "POST", "/setup", {"token": read("porch-setup-token"), "display_name": "Owner Example",
                                                 "email": "owner@example.com"}, expect=201)
    cookies = {}
    for k, v in hdr:
        if k.lower() == "set-cookie":
            n, _, rest = v.partition("=")
            cookies[n] = rest.split(";", 1)[0]
    for role in ("admin", "auditor"):
        tok = api(PORCH, "POST", "/installation/automation-tokens", {"name": f"deed-validate-{role}", "role": role, "expires_in_days": 1},
                  cookies=cookies, csrf=me["csrf_token"], csrf_header="X-Porch-CSRF", expect=201)[1]
        assert tok["token"].startswith("porch_at_"), tok
        S[role] = tok["token"]
    write_private("porch-auditor-token", S["auditor"])
    write_private("porch-admin-token", S["admin"])

    prov = P("POST", "/identity-providers", {"slug": "google", "kind": "google", "name": "Google (Porch test fixture)",
                                             "issuer": f"http://127.0.0.1:{IDP_PORT}", "client_id": "porch",
                                             "client_secret": read("google-secret")}, expect=201)
    for slug, name, domain in (("home", "Home (example.com)", "example.com"), ("lab", "Lab (lab.example.com)", "lab.example.com")):
        P("POST", "/tenants", {"slug": slug, "name": name}, expect=201)
        P("POST", f"/tenants/{slug}/domain-rules", {"domain": domain, "provider_id": prov["id"], "evidence_kind": "workspace_hd",
                                                    "mode": "jit"}, expect=201)
    groups = {"home": ["deed-admins", "deed-auditors", "platform", "staff"], "lab": ["deed-admins"]}
    for t, gs in groups.items():
        for g in gs:
            P("POST", f"/tenants/{t}/groups", {"slug": g, "name": g}, expect=201)
    # More tenants than one page of GET /tenants (Deed asks for limit=100), so the connector must follow next_cursor.
    for i in range(1, 106):
        P("POST", "/tenants", {"slug": f"filler-{i:03d}", "name": f"Filler {i}"}, expect=201)

    deed = P("POST", "/tenants/home/applications", {
        "slug": "deed", "name": "Deed", "kind": "oidc", "launch_url": DEED + "/", "access_mode": "all_members",
        "description": "Infrastructure graph (validation run)",
        # Porch's default allowed_scopes (openid profile email offline_access) omit groups: without it in the client's
        # allowed scopes, Porch silently drops the requested groups scope and porch.groups never appears.
        "client": {"client_type": "confidential", "redirect_uris": [DEED + "/auth/oidc/porch/callback"],
                   "grant_types": ["authorization_code"], "allowed_scopes": ["openid", "profile", "email", "groups"]},
        "disclosure": {"email": True, "groups": True, "roles": False, "entitlements": False}}, expect=201)
    write_private("oidc-client-id", deed["client"]["client_id"])
    write_private("oidc-client-secret", deed["client_secret"])
    # Users of tenant lab reach the app only through an explicit share; their tokens then name tenant lab.
    P("POST", f"/tenants/home/applications/{deed['id']}/shares", {"tenant": "lab"}, expect=204)
    grafana = P("POST", "/tenants/home/applications", {
        "slug": "grafana", "name": "Grafana", "kind": "oidc", "launch_url": "https://grafana.example.com/", "group": "ops",
        "client": {"client_type": "confidential", "redirect_uris": ["https://grafana.example.com/login/generic_oauth"],
                   "grant_types": ["authorization_code", "refresh_token"], "signing_alg": "RS256"},
        "disclosure": {"email": True, "groups": True, "roles": False, "entitlements": False}}, expect=201)
    nas = P("POST", "/tenants/home/applications", {"slug": "nas", "name": "NAS", "kind": "forward_auth",
                                                    "hosts": ["nas.example.com"], "access_mode": "all_members"}, expect=201)
    old = P("POST", "/tenants/home/applications", {"slug": "old", "name": "Old", "kind": "forward_auth", "hosts": ["old.example.com"]},
            expect=201)
    st, cur, h = api(PORCH, "GET", f"/tenants/home/applications/{old['id']}", token=S["admin"], expect=200)
    etag = dict((k.lower(), v) for k, v in h).get("etag", "")
    api(PORCH, "PATCH", f"/tenants/home/applications/{old['id']}", {"enabled": False}, token=S["admin"],
        headers={"If-Match": etag} if etag else None, expect=200)
    wiki = P("POST", "/tenants/lab/applications", {"slug": "wiki", "name": "Wiki", "kind": "forward_auth", "hosts": ["wiki.lab.example.com"]},
             expect=201)
    sa_home = P("POST", "/tenants/home/service-accounts", {"slug": "deed-reader", "name": "Deed reader", "description": "Inventory reader",
                                                          "audiences": ["https://deed.example.com"]}, expect=201)
    sa_lab = P("POST", "/tenants/lab/service-accounts", {"slug": "backup", "name": "Backup job"}, expect=201)
    # An application in the last tenant, which lands on the second page of GET /tenants?limit=100.
    status = P("POST", "/tenants/filler-105/applications", {"slug": "status", "name": "Status", "kind": "forward_auth",
                                                            "hosts": ["status.example.com"]}, expect=201)
    # The narrowest Porch credential that can read applications and service accounts: a tenant token. Porch has no
    # read-only permission for them, and installation auditor tokens cannot read inside tenants.
    tt = api(PORCH, "POST", "/tenants/home/automation-tokens", {"name": "deed-validate-home", "expires_in_days": 1,
                                                               "permissions": ["app:manage", "service_account:manage"]},
             cookies=cookies, csrf=me["csrf_token"], csrf_header="X-Porch-CSRF", expect=201)[1]
    write_private("porch-tenant-token", tt["token"])
    state = {"provider_id": prov["id"], "apps": {"home/deed": deed["id"], "home/grafana": grafana["id"], "home/nas": nas["id"],
                                                  "home/old": old["id"], "lab/wiki": wiki["id"], "filler-105/status": status["id"]},
             "service_accounts": {"home/deed-reader": sa_home["id"], "lab/backup": sa_lab["id"]},
             "client_id": deed["client"]["client_id"]}
    with open(STATE, "w") as f:
        json.dump(state, f, indent=2)
    print(json.dumps({"porch_bootstrap": "ok", "tenants": 107, "client_id": state["client_id"]}))


# ----------------------------------------------------------------------------------------------------------------- deed
def totp(secret):
    k = base64.b32decode(secret + "=" * ((8 - len(secret) % 8) % 8))
    h = hmac.new(k, struct.pack(">Q", int(time.time()) // 30), hashlib.sha1).digest()
    o = h[-1] & 15
    return "%06d" % ((struct.unpack(">I", h[o:o + 4])[0] & 0x7FFFFFFF) % 1000000)


def deed_phase():
    state = load_state()
    admin_porch = read("porch-admin-token")
    S = {}

    def D(method, path, body=None, expect=None):
        return api(DEED, method, path, body, token=S["token"], expect=expect)[1]

    def PA(method, path, body=None, expect=None):
        return api(PORCH, method, path, body, token=admin_porch, expect=expect)[1]

    browsers = {}  # one user agent per person: the Porch session is reused like a real browser's

    def browser(subject):
        b = browsers.setdefault(subject, Browser())
        for k in [k for k in b.cookies if k.startswith("deed_")]:
            del b.cookies[k]
        return b

    def sso(subject, return_to="/inventory"):
        b = browser(subject)
        final = b.navigate(DEED + "/auth/oidc/porch/start?" + urllib.parse.urlencode({"return_to": return_to}), subject)
        me = None
        if "deed_session" in b.cookies:
            me = api(DEED, "GET", "/me", cookies={"deed_session": b.cookies["deed_session"]}, expect=200)[1]
        return b, final, me

    def denials(reason):
        recs = items(D("GET", "/audit?action=auth.login&limit=100"))
        return [i for i in recs if i.get("outcome") == "denied" and i.get("reason_code") == reason]

    @step("Deed owner set up (bootstrap link, password, TOTP) with an admin API token; project infra created")
    def _():
        c = Browser()
        st, hdr, data = c.request("POST", DEED + "/api/v1/auth/setup/complete", json.dumps(
            {"token": read("deed-setup-token"), "email": "owner@example.com", "display_name": "Owner Example",
             "password": "validate porch owner passphrase"}), {"Content-Type": "application/json", "Origin": DEED})
        assert st == 200, data
        csrf = json.loads(data)["csrf_token"]
        hj = {"Content-Type": "application/json", "Origin": DEED}
        st, _, data = c.request("POST", DEED + "/api/v1/me/mfa/totp", "{}", {**hj, "X-Deed-CSRF": csrf})
        enr = json.loads(data)
        st, _, data = c.request("POST", DEED + "/api/v1/me/mfa/totp/confirm", json.dumps({"code": totp(enr["secret"])}), {**hj, "X-Deed-CSRF": csrf})
        assert st == 200, data
        st, _, data = c.request("GET", DEED + "/api/v1/me", None, hj)
        csrf = json.loads(data)["csrf_token"]
        st, _, data = c.request("POST", DEED + "/api/v1/me/tokens", json.dumps({"name": "validate", "installation_role": "admin"}),
                                {**hj, "X-Deed-CSRF": csrf})
        assert st == 201, data
        S["token"] = json.loads(data)["token"]
        D("POST", "/projects", {"name": "infra", "display_name": "Infra (validation)"}, expect=201)
        info = api(DEED, "GET", "/system/info", expect=200)[1]
        assert {"slug": "porch", "display_name": "Porch"} in info["sso_providers"], info
        return {"sso_providers": info["sso_providers"], "local_auth": info["local_auth"]}

    @step("Porch discovery advertises what Deed relies on (PKCE S256, RFC 9207 iss, groups scope, issuer = config)")
    def _():
        conn = http.client.HTTPConnection("127.0.0.1", P_PORT, timeout=10)
        conn.request("GET", "/.well-known/openid-configuration")
        d = json.loads(conn.getresponse().read())
        conn.close()
        assert d["issuer"] == PORCH, d["issuer"]
        assert "S256" in d.get("code_challenge_methods_supported", []), d
        assert "groups" in d.get("scopes_supported", []), d
        return {k: d.get(k) for k in ("issuer", "code_challenge_methods_supported", "authorization_response_iss_parameter_supported",
                                      "scopes_supported", "id_token_signing_alg_values_supported")}

    @step("Porch users enrolled by domain rule (fake Google sign-in) and placed in Porch groups")
    def _():
        for sub in ("ada", "bob", "dave", "carol"):
            browser(sub).navigate(PORCH + "/login/google?return_to=%2F", sub)
        members = {}
        for t in ("home", "lab"):
            for m in PA("GET", f"/tenants/{t}/members?limit=200", expect=200)["items"]:
                members[(t, m.get("email", ""))] = m["user_id"]
        need = {("home", "ada@example.com"), ("home", "bob@example.com"), ("home", "dave@example.com"), ("lab", "carol@lab.example.com")}
        assert need <= set(members), sorted(members)
        assert ("home", "carol@lab.example.com") not in members
        S["porch_users"] = {e.split("@")[0]: uid for (t, e), uid in members.items()}
        for t, g, who in (("home", "deed-admins", "ada"), ("home", "platform", "ada"), ("home", "deed-auditors", "bob"),
                          ("home", "staff", "dave"), ("lab", "deed-admins", "carol")):
            PA("PUT", f"/tenants/{t}/groups/{g}/members/{S['porch_users'][who]}", expect=204)
        return {"members": sorted(f"{t}:{e}" for t, e in members)}

    @step("SSO: ada (Porch groups deed-admins, platform) is JIT-provisioned as admin and infra editor")
    def _():
        b, final, me = sso("ada")
        assert final == DEED + "/inventory", (final, b.trace)
        assert me and me["method"] == "oidc" and me["provider"] == "porch", me
        assert me["installation_role"] == "admin", me
        projects = {p["name"]: p["id"] for p in items(D("GET", "/projects"))}
        assert me["project_roles"].get(projects["infra"]) == "editor", (me["project_roles"], projects)
        idents = api(DEED, "GET", "/me/identities", cookies={"deed_session": b.cookies["deed_session"]}, expect=200)[1]
        idents = items(idents)
        assert len(idents) == 1 and sorted(idents[0]["groups"]) == ["deed-admins", "platform"], idents
        assert me["email"] == "ada@example.com", me
        S["ada"] = {"user_id": me["user_id"], "subject": idents[0]["subject"]}
        S["infra"] = projects["infra"]
        names = {str(D_PORT): "deed", str(P_PORT): "porch", str(IDP_PORT): "google"}
        flow = [" ".join([t.split()[0], names.get(t.split()[1].split("/")[0], "?") + "/" + t.split()[1].split("/", 1)[1], *t.split()[2:]])
                for t in b.trace]
        return {"final": final.replace(DEED, "<deed>"), "user_id": me["user_id"], "role": me["installation_role"],
                "project_roles": me["project_roles"], "identity": {"subject": idents[0]["subject"], "groups": idents[0]["groups"]},
                "flow": flow}

    @step("SSO: bob (deed-auditors) gets auditor")
    def _():
        _, final, me = sso("bob")
        assert final == DEED + "/inventory" and me and me["installation_role"] == "auditor", (final, me)
        return {"role": me["installation_role"], "project_roles": me["project_roles"]}

    @step("SSO: dave (only unmapped group staff) is refused as not provisioned, no user created")
    def _():
        before = len(items(D("GET", "/users")))
        b, final, me = sso("dave")
        assert final == DEED + "/login?error=forbidden" and me is None, (final, me)
        after = len(items(D("GET", "/users")))
        assert before == after, (before, after)
        assert denials("not_provisioned"), "no denied auth.login with reason not_provisioned"
        return {"final": final.replace(DEED, "<deed>"), "users_before": before, "users_after": after, "audit_reason": "not_provisioned"}

    @step("SSO: carol (tenant lab via app share, mapped group) is refused by required_tenant home")
    def _():
        b, final, me = sso("carol")
        assert final == DEED + "/login?error=forbidden" and me is None, (final, me)
        assert denials("tenant_mismatch"), "no denied auth.login with reason tenant_mismatch"
        return {"final": final.replace(DEED, "<deed>"), "audit_reason": "tenant_mismatch"}

    @step("SSO: second login of ada matches (iss, sub) to the same user")
    def _():
        users_before = items(D("GET", "/users"))
        _, final, me = sso("ada")
        users_after = items(D("GET", "/users"))
        assert me and me["user_id"] == S["ada"]["user_id"] and len(users_before) == len(users_after), (me, len(users_before), len(users_after))
        return {"user_id": me["user_id"], "users": len(users_after)}

    @step("SSO: removing ada from deed-admins in Porch lowers her Deed role at the next login (mapping re-evaluated)")
    def _():
        PA("DELETE", f"/tenants/home/groups/deed-admins/members/{S['porch_users']['ada']}", expect=204)
        _, final, me = sso("ada")
        PA("PUT", f"/tenants/home/groups/deed-admins/members/{S['porch_users']['ada']}", expect=204)
        assert me and not me.get("installation_role") and me["project_roles"].get(S["infra"]) == "editor", me
        return {"installation_role": me.get("installation_role", ""), "project_roles": me["project_roles"]}

    @step("RFC 9207: a callback whose iss differs from the issuer, or lacks iss, is refused")
    def _():
        out = {}
        for case in ("wrong", "missing", "genuine"):
            b = browser("ada")
            cb = b.navigate(DEED + "/auth/oidc/porch/start?return_to=%2F", "ada", stop_at_deed_callback=True)
            u = urllib.parse.urlsplit(cb)
            q = dict(urllib.parse.parse_qsl(u.query))
            assert q.get("iss") == PORCH, q
            if case == "wrong":
                q["iss"] = "http://127.0.0.1:1"
            elif case == "missing":
                q.pop("iss")
            st, hdr, _ = b.request("GET", DEED + u.path + "?" + urllib.parse.urlencode(q))
            out[case] = (st, hdr.get("location"))
        assert out["wrong"] == (302, "/login?error=unauthorized"), out
        assert out["missing"] == (302, "/login?error=unauthorized"), out
        assert out["genuine"] == (302, "/"), out
        assert denials("issuer_mismatch"), "no denied auth.login with reason issuer_mismatch"
        return out

    @step("PKCE and nonce: the authorization request carries S256, a nonce and the configured scopes")
    def _():
        b = Browser()
        st, hdr, _ = b.request("GET", DEED + "/auth/oidc/porch/start")
        u = urllib.parse.urlsplit(hdr["location"])
        q = dict(urllib.parse.parse_qsl(u.query))
        assert u.port == P_PORT and q["code_challenge_method"] == "S256" and q.get("nonce") and q.get("code_challenge"), q
        assert set(q["scope"].split()) == {"openid", "profile", "email", "groups"}, q["scope"]
        return {"authorize": f"{u.scheme}://{u.hostname}:<porch>{u.path}", "scope": q["scope"], "code_challenge_method": "S256",
                "deed_cookie": [c for c in b.cookies]}

    # ------------------------------------------------------------------------------------------------- connector
    def porch_source(project, name, token_file, tenants=None):
        cfg = {"url": PORCH, "insecure_http": True, "allow_cidrs": ["127.0.0.1/32"]}
        if tenants:
            cfg["tenants"] = tenants
        return D("POST", "/sources", {"project": project, "name": name, "provider": "porch", "config": cfg,
                                      "secrets": {"token": read(token_file)}}, expect=201)["id"]

    def sync(src, project):
        run = D("POST", f"/sources/{src}/sync", expect=200)["run"]
        assert run["result"] == "ok", run
        ents = D("GET", f"/entities?project={project}&source={src}&limit=200")["items"]
        return run, {(e["kind"], e["metadata"]["name"]): {"id": e["metadata"]["id"], "spec": e["spec"]} for e in ents}

    def edges_to_instance(by, project):
        inst = by[("service", "porch")]["id"]
        names = {e["id"]: n for (k, n), e in by.items()}
        rels = D("GET", f"/relations?project={project}&type=authenticates_with&entity={inst}&limit=200")["items"]
        return sorted(names[x["from"]["id"]] for x in rels if x["to"]["id"] == inst and x["from"]["id"] in names)

    @step("Porch source: an installation auditor token is refused inside tenants; the test names the permission needed")
    def _():
        src = porch_source("infra", "porch-auditor", "porch-auditor-token")
        t = D("POST", f"/sources/{src}/test", expect=200)
        assert not t["ok"] and "app:manage" in t["diagnostics"][0]["message"], t
        return {"diagnostics": t["diagnostics"]}

    @step("Porch source: tenant token (app:manage, service_account:manage) for home: test and sync import apps, "
          "service accounts and authenticates_with edges")
    def _():
        src = porch_source("infra", "porch", "porch-tenant-token", ["home"])
        S["src"] = src
        t = D("POST", f"/sources/{src}/test", expect=200)
        assert t["ok"], t
        run, by = sync(src, "infra")
        want = {("service", "porch"), ("porch_app", "home-deed"), ("porch_app", "home-grafana"), ("porch_app", "home-nas"),
                ("porch_service_account", "home-deed-reader")}
        assert set(by) == want, sorted(by)  # home-old is disabled
        deed_app = by[("porch_app", "home-deed")]["spec"]
        assert deed_app["client_id"] == state["client_id"] and deed_app["app_id"] == state["apps"]["home/deed"], deed_app
        assert deed_app["redirect_uris"] == [DEED + "/auth/oidc/porch/callback"] and deed_app["require_pkce"] is True, deed_app
        assert by[("porch_app", "home-grafana")]["spec"]["signing_alg"] == "RS256", by[("porch_app", "home-grafana")]["spec"]
        assert by[("porch_app", "home-nas")]["spec"]["hosts"] == ["nas.example.com"], by[("porch_app", "home-nas")]["spec"]
        sa = by[("porch_service_account", "home-deed-reader")]["spec"]
        assert sa["service_account_id"] == state["service_accounts"]["home/deed-reader"] and sa["audiences"] == ["https://deed.example.com"], sa
        froms = edges_to_instance(by, "infra")
        assert froms == sorted(n for k, n in want if k != "service"), froms
        S["by"] = by
        return {"test": t["diagnostics"], "run": {"result": run["result"], "stats": run.get("stats")},
                "entities": sorted(f"{k}/{n}" for k, n in by), "authenticates_with_to_porch": froms}

    @step("Porch source: a catalog DNS record for a forward-auth host is linked protected_by on the next sync")
    def _():
        D("POST", "/entities", {"project": "infra", "kind": "dns_record", "name": "nas-example-com",
                                "spec": {"fqdn": "nas.example.com", "type": "A", "content": "192.0.2.10"}}, expect=201)
        sync(S["src"], "infra")
        nas = S["by"][("porch_app", "home-nas")]["id"]
        rels = D("GET", f"/relations?project=infra&type=protected_by&entity={nas}")["items"]
        assert len(rels) == 1 and rels[0]["to"]["id"] == nas and rels[0].get("inferred") is True, rels
        return {"protected_by": f"{rels[0]['from']['name']} -> {rels[0]['to']['name']}", "inferred": rels[0]["inferred"],
                "evidence_class": rels[0].get("evidence_class"), "attributes": rels[0].get("attributes")}

    @step("Porch source: installation admin token over every tenant follows next_cursor (107 tenants, limit 100)")
    def _():
        D("POST", "/projects", {"name": "porch-all", "display_name": "Porch, all tenants (validation)"}, expect=201)
        src = porch_source("porch-all", "porch", "porch-admin-token")
        run, by = sync(src, "porch-all")
        want = {("service", "porch"), ("porch_app", "home-deed"), ("porch_app", "home-grafana"), ("porch_app", "home-nas"),
                ("porch_app", "lab-wiki"), ("porch_app", "filler-105-status"), ("porch_service_account", "home-deed-reader"),
                ("porch_service_account", "lab-backup")}
        assert set(by) == want, sorted(by)
        froms = edges_to_instance(by, "porch-all")
        assert froms == sorted(n for k, n in want if k != "service"), froms
        return {"entities": sorted(f"{k}/{n}" for k, n in by), "page_two_tenant_app": "filler-105-status"}

    @step("Porch tokens stay inside their grants (auditor cannot write; the home tenant token cannot read lab)")
    def _():
        st1, _, _ = api(PORCH, "POST", "/tenants", {"slug": "nope", "name": "Nope"}, token=read("porch-auditor-token"))
        st2, _, _ = api(PORCH, "GET", "/tenants/lab/applications", token=read("porch-tenant-token"))
        st3, _, _ = api(PORCH, "GET", "/tenants", token=read("porch-tenant-token"))
        assert st1 in (401, 403) and st2 == 404 and st3 in (401, 403, 404), (st1, st2, st3)
        return {"auditor_create_tenant": st1, "tenant_token_other_tenant": st2, "tenant_token_list_tenants": st3}

    with open(os.path.join(DIR, "results.json"), "w") as f:
        json.dump(results, f, indent=2)
    failed = [r for r in results if not r["ok"]]
    print(f"\n{len(results) - len(failed)}/{len(results)} steps passed")
    return 1 if failed else 0


if __name__ == "__main__":
    if sys.argv[1:] == ["porch"]:
        porch_phase()
    elif sys.argv[1:] == ["deed"]:
        sys.exit(deed_phase())
    else:
        sys.exit("usage: porch_driver.py porch|deed")
