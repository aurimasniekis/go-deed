# ADR-004: Projects, roles and bridges

## Decision
- Every entity, relation, source, saved view and finding belongs to exactly one **project** (`prj_…`, slug name).
- **Installation roles**: `owner` (everything, including role grants and backups), `admin` (everything except
  owner grants), `auditor` (read everything incl. audit, no writes).
- **Project roles**: `viewer` (read), `editor` (declare entities/relations, annotations, overrides, saved views),
  `admin` (sources, credentials, kinds scoped to the project, member grants, bridges).
- Custom kinds and relationship types are installation-wide; creating them needs installation `admin` or a project
  `admin` (scoped to the kind group `custom.<project>`), and are audited.
- **Cross-project relations** are allowed only when one endpoint is a `bridge`-kind entity or when the relation
  references a declared bridge (`bridges` table: from_project, to_project, allowed relationship types). Otherwise 422
  `cross_project_relation`.
- **Redaction**: graph traversal that reaches an entity in a project the caller cannot read returns a stub
  `{id: "redacted:<hash>", kind: "external", project: "restricted"}`; edges keep their type but not attributes.
- Authorization is enforced in the API layer, in graph queries (filtering by readable projects in SQL), in search
  (FTS joined with readable projects) and in SSE subscriptions (events filtered per subscriber).
- Machine tokens carry a role ≤ the creating user's role, optionally restricted to a project list.
