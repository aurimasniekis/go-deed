package authz

import (
	"slices"
	"testing"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/authn"
)

const (
	home    = "prj_home"
	parents = "prj_parents"
	lab     = "prj_lab"
)

func user(role string, projects map[string]string) *authn.Principal {
	if projects == nil {
		projects = map[string]string{}
	}
	return &authn.Principal{UserID: "usr_1", InstallationRole: role, ProjectRoles: projects}
}

func token(p *authn.Principal, cap string, projects ...string) *authn.Principal {
	p.TokenID = "tok_1"
	p.TokenProjectRole = cap
	if projects != nil {
		p.TokenProjects = projects
	}
	return p
}

func sourceToken() *authn.Principal {
	// Source tokens never carry project roles; the map is filled here to prove it is ignored.
	return &authn.Principal{TokenID: "tok_src", SourceID: "src_1", ProjectRoles: map[string]string{home: authn.ProjectAdmin}}
}

func TestLevelNames(t *testing.T) {
	for _, l := range []Level{Viewer, Editor, Admin} {
		if LevelOf(l.String()) != l {
			t.Errorf("round trip %v", l)
		}
	}
	if None.String() != "none" || LevelOf("none") != None || LevelOf("") != None || LevelOf("owner") != None || LevelOf("Admin") != None {
		t.Error("unknown roles must map to None")
	}
	if None >= Viewer || Viewer >= Editor || Editor >= Admin {
		t.Error("levels are not ordered")
	}
}

func TestProjectLevel(t *testing.T) {
	cases := []struct {
		name string
		p    *authn.Principal
		want map[string]Level // project → level; projects not listed expect None
	}{
		{"anonymous", nil, nil},
		{"source token", sourceToken(), nil},
		{"owner", user(authn.RoleOwner, nil), map[string]Level{home: Admin, parents: Admin, lab: Admin}},
		{"installation admin", user(authn.RoleAdmin, nil), map[string]Level{home: Admin, parents: Admin, lab: Admin}},
		{"auditor", user(authn.RoleAuditor, nil), map[string]Level{home: Viewer, parents: Viewer, lab: Viewer}},
		{"no role, no membership", user("", nil), nil},
		{"project viewer", user("", map[string]string{home: authn.ProjectViewer}), map[string]Level{home: Viewer}},
		{"project editor", user("", map[string]string{home: authn.ProjectEditor}), map[string]Level{home: Editor}},
		{"project admin", user("", map[string]string{home: authn.ProjectAdmin}), map[string]Level{home: Admin}},
		{"mixed memberships", user("", map[string]string{home: authn.ProjectAdmin, parents: authn.ProjectViewer}),
			map[string]Level{home: Admin, parents: Viewer}},
		{"unknown membership role", user("", map[string]string{home: "owner"}), nil},
		{"auditor raised by membership", user(authn.RoleAuditor, map[string]string{home: authn.ProjectEditor}),
			map[string]Level{home: Editor, parents: Viewer, lab: Viewer}},
		{"owner not lowered by membership", user(authn.RoleOwner, map[string]string{home: authn.ProjectViewer}),
			map[string]Level{home: Admin, parents: Admin, lab: Admin}},
		{"token capped to viewer", token(user(authn.RoleOwner, nil), authn.ProjectViewer),
			map[string]Level{home: Viewer, parents: Viewer, lab: Viewer}},
		{"token capped to editor", token(user("", map[string]string{home: authn.ProjectAdmin, parents: authn.ProjectViewer}), authn.ProjectEditor),
			map[string]Level{home: Editor, parents: Viewer}},
		{"token cap never raises", token(user("", map[string]string{home: authn.ProjectViewer}), authn.ProjectAdmin),
			map[string]Level{home: Viewer}},
		{"token restricted to projects", token(user(authn.RoleAdmin, nil), "", home, lab),
			map[string]Level{home: Admin, lab: Admin}},
		{"token restricted and capped", token(user("", map[string]string{home: authn.ProjectAdmin, parents: authn.ProjectAdmin}), authn.ProjectEditor, home),
			map[string]Level{home: Editor}},
		{"token restricted to an empty list", token(user(authn.RoleOwner, nil), "", []string{}...), nil},
	}
	for _, tc := range cases {
		for _, pid := range []string{home, parents, lab} {
			want := tc.want[pid]
			if got := ProjectLevel(tc.p, pid); got != want {
				t.Errorf("%s: %s level = %v, want %v", tc.name, pid, got, want)
			}
			for _, need := range []Level{Viewer, Editor, Admin} {
				if got := Can(tc.p, pid, need); got != (want >= need) {
					t.Errorf("%s: Can(%s, %v) = %v", tc.name, pid, need, got)
				}
				err := Require(tc.p, pid, need)
				switch {
				case want >= need && err != nil:
					t.Errorf("%s: Require(%s, %v): %v", tc.name, pid, need, err)
				case want == None && apperr.CodeOf(err) != apperr.CodeNotFound:
					t.Errorf("%s: inaccessible %s must look missing, got %v", tc.name, pid, err)
				case want != None && want < need && apperr.CodeOf(err) != apperr.CodeForbidden:
					t.Errorf("%s: Require(%s, %v) with %v: %v", tc.name, pid, need, want, err)
				}
			}
		}
	}
}

func TestReadable(t *testing.T) {
	known := []string{home, parents, lab}
	cases := []struct {
		name string
		p    *authn.Principal
		all  bool
		ids  []string
	}{
		{"anonymous", nil, false, []string{}},
		{"source token", sourceToken(), false, []string{}},
		{"owner", user(authn.RoleOwner, nil), true, nil},
		{"installation admin", user(authn.RoleAdmin, nil), true, nil},
		{"auditor", user(authn.RoleAuditor, nil), true, nil},
		{"member", user("", map[string]string{parents: authn.ProjectViewer, lab: authn.ProjectEditor}), false, []string{parents, lab}},
		{"no access", user("", nil), false, []string{}},
		{"owner token capped", token(user(authn.RoleOwner, nil), authn.ProjectViewer), true, nil},
		{"owner token restricted", token(user(authn.RoleOwner, nil), "", lab), false, []string{lab}},
		{"member token restricted", token(user("", map[string]string{home: authn.ProjectViewer, parents: authn.ProjectViewer}), "", parents, lab),
			false, []string{parents}},
	}
	for _, tc := range cases {
		all, ids := Readable(tc.p, known)
		if all != tc.all || !slices.Equal(ids, tc.ids) {
			t.Errorf("%s: Readable = %v %v, want %v %v", tc.name, all, ids, tc.all, tc.ids)
		}
		if tc.ids != nil && ids == nil {
			t.Errorf("%s: an empty result must be a non-nil slice", tc.name)
		}
		// Readable agrees with ProjectLevel.
		for _, pid := range known {
			readable := all || slices.Contains(ids, pid)
			if readable != Can(tc.p, pid, Viewer) {
				t.Errorf("%s: Readable and ProjectLevel disagree on %s", tc.name, pid)
			}
		}
	}
}

func TestInstallationRoles(t *testing.T) {
	cases := []struct {
		name      string
		p         *authn.Principal
		admin     bool
		audit     bool
		owner     bool
		userCheck bool
	}{
		{"owner", user(authn.RoleOwner, nil), true, true, true, true},
		{"admin", user(authn.RoleAdmin, nil), true, true, false, true},
		{"auditor", user(authn.RoleAuditor, nil), false, true, false, true},
		{"plain user", user("", map[string]string{home: authn.ProjectAdmin}), false, false, false, true},
		{"admin token", token(user(authn.RoleAdmin, nil), ""), true, true, false, true},
		{"source token", &authn.Principal{TokenID: "tok_src", SourceID: "src_1", InstallationRole: authn.RoleOwner}, false, false, false, false},
		{"anonymous", nil, false, false, false, false},
		{"no user", &authn.Principal{InstallationRole: authn.RoleOwner}, true, true, true, false},
	}
	for _, tc := range cases {
		if got := CanAdminister(tc.p); got != tc.admin {
			t.Errorf("%s: CanAdminister = %v", tc.name, got)
		}
		if got := CanReadAllAudit(tc.p); got != tc.audit {
			t.Errorf("%s: CanReadAllAudit = %v", tc.name, got)
		}
		if got := IsInstallation(tc.p, authn.RoleOwner); got != tc.owner {
			t.Errorf("%s: IsInstallation(owner) = %v", tc.name, got)
		}
		err := RequireInstallation(tc.p, authn.RoleOwner, authn.RoleAdmin)
		if (err == nil) != tc.admin || (err != nil && apperr.CodeOf(err) != apperr.CodeForbidden) {
			t.Errorf("%s: RequireInstallation = %v", tc.name, err)
		}
		err = RequireUser(tc.p)
		if (err == nil) != tc.userCheck || (err != nil && apperr.CodeOf(err) != apperr.CodeForbidden) {
			t.Errorf("%s: RequireUser = %v", tc.name, err)
		}
	}
	if IsInstallation(user("", nil), "") {
		t.Error("an empty installation role matched")
	}
	if IsInstallation(user(authn.RoleOwner, nil)) {
		t.Error("IsInstallation without roles matched")
	}
}
