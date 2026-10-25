import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { AuditPage, validateAuditSearch, type AuditSearch } from '@/features/settings/AuditPage'

export const Route = createFileRoute('/_app/settings/audit')({
  validateSearch: (s: Record<string, unknown>): AuditSearch => validateAuditSearch(s),
  component: Audit,
})

function Audit() {
  const search = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })
  return <AuditPage search={search} setSearch={(s) => void navigate({ search: s, replace: true })} />
}
