import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { ENTITY_TABS, EntityPage, type EntityTab } from '@/features/entity/EntityPage'
import { compact, decodeEnum, decodeString } from '@/lib/urlState'

interface EntitySearch {
  tab?: EntityTab
  ptr?: string
}

export const Route = createFileRoute('/_app/entities/$id')({
  validateSearch: (s: Record<string, unknown>): EntitySearch =>
    compact({ tab: decodeEnum(s.tab, ENTITY_TABS), ptr: decodeString(s.ptr, 512) }),
  component: Entity,
})

function Entity() {
  const { id } = Route.useParams()
  const { tab, ptr } = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })
  return (
    <EntityPage
      key={id}
      id={id}
      tab={tab ?? 'overview'}
      ptr={ptr}
      setTab={(t) => void navigate({ search: compact({ tab: t === 'overview' ? undefined : t }), replace: true })}
    />
  )
}
