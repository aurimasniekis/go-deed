// Package authz decides what a principal may do (ADR-004). Installation roles grant implicit project access
// (owner/admin → admin, auditor → viewer); project memberships grant explicit access; API tokens cap both. Source
// tokens have no project access at all: they may only use the ingest endpoints of their own source.
package authz

import (
	"slices"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/authn"
)

// Level is a project access level.
type Level int

// Levels in increasing order.
const (
	None Level = iota
	Viewer
	Editor
	Admin
)

// String returns the role name.
func (l Level) String() string {
	switch l {
	case Viewer:
		return authn.ProjectViewer
	case Editor:
		return authn.ProjectEditor
	case Admin:
		return authn.ProjectAdmin
	}
	return "none"
}

// LevelOf parses a project role name.
func LevelOf(role string) Level {
	switch role {
	case authn.ProjectViewer:
		return Viewer
	case authn.ProjectEditor:
		return Editor
	case authn.ProjectAdmin:
		return Admin
	}
	return None
}

func implicit(p *authn.Principal) Level {
	switch p.InstallationRole {
	case authn.RoleOwner, authn.RoleAdmin:
		return Admin
	case authn.RoleAuditor:
		return Viewer
	}
	return None
}

// ProjectLevel returns p's access level in a project.
func ProjectLevel(p *authn.Principal, projectID string) Level {
	if p == nil || p.SourceID != "" {
		return None
	}
	if p.TokenProjects != nil && !slices.Contains(p.TokenProjects, projectID) {
		return None
	}
	l := max(implicit(p), LevelOf(p.ProjectRoles[projectID]))
	if p.TokenProjectRole != "" {
		l = min(l, LevelOf(p.TokenProjectRole))
	}
	return l
}

// Can reports whether p has at least need in the project.
func Can(p *authn.Principal, projectID string, need Level) bool {
	return ProjectLevel(p, projectID) >= need
}

// Require returns a forbidden error unless p has at least need in the project. Unreadable projects look like
// missing ones (not found) so their existence is not disclosed.
func Require(p *authn.Principal, projectID string, need Level) error {
	l := ProjectLevel(p, projectID)
	if l >= need {
		return nil
	}
	if l == None {
		return apperr.New(apperr.CodeNotFound, "project not found")
	}
	return apperr.New(apperr.CodeForbidden, "project role %s required", need)
}

// Readable returns the project IDs p can read, or all=true when every project is readable. known lists every
// project ID (needed to evaluate token restrictions).
func Readable(p *authn.Principal, known []string) (all bool, ids []string) {
	if p == nil || p.SourceID != "" {
		return false, []string{}
	}
	if implicit(p) >= Viewer && p.TokenProjects == nil && (p.TokenProjectRole == "" || LevelOf(p.TokenProjectRole) >= Viewer) {
		return true, nil
	}
	ids = []string{}
	for _, id := range known {
		if ProjectLevel(p, id) >= Viewer {
			ids = append(ids, id)
		}
	}
	return false, ids
}

// IsInstallation reports whether p holds one of the installation roles.
func IsInstallation(p *authn.Principal, roles ...string) bool {
	return p != nil && p.SourceID == "" && p.InstallationRole != "" && slices.Contains(roles, p.InstallationRole)
}

// RequireInstallation returns a forbidden error unless p holds one of the roles.
func RequireInstallation(p *authn.Principal, roles ...string) error {
	if IsInstallation(p, roles...) {
		return nil
	}
	return apperr.New(apperr.CodeForbidden, "installation role required")
}

// CanAdminister reports whether p may manage installation settings (owner or admin).
func CanAdminister(p *authn.Principal) bool {
	return IsInstallation(p, authn.RoleOwner, authn.RoleAdmin)
}

// CanReadAllAudit reports whether p may read the whole audit log.
func CanReadAllAudit(p *authn.Principal) bool {
	return IsInstallation(p, authn.RoleOwner, authn.RoleAdmin, authn.RoleAuditor)
}

// RequireUser refuses source tokens and anonymous callers.
func RequireUser(p *authn.Principal) error {
	if p == nil || p.SourceID != "" || p.UserID == "" {
		return apperr.New(apperr.CodeForbidden, "a user credential is required")
	}
	return nil
}
