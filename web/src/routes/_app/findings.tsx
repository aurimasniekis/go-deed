import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { FindingsPage } from '@/features/findings/FindingsPage'
import { validateFindingsSearch, type FindingsSearch } from '@/lib/urlState'

export const Route = createFileRoute('/_app/findings')({
  validateSearch: (s: Record<string, unknown>): FindingsSearch => validateFindingsSearch(s),
  component: Findings,
})

function Findings() {
  const search = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })
  return <FindingsPage search={search} setSearch={(s) => void navigate({ search: s, replace: true })} />
}
