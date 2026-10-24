import { Badge, Group, Loader, Text } from '@mantine/core'
import { useDebouncedValue } from '@mantine/hooks'
import { Spotlight } from '@mantine/spotlight'
import { IconSearch } from '@tabler/icons-react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useNavigate, useRouterState } from '@tanstack/react-router'
import { useState } from 'react'
import { api, unwrap } from '@/api/client'
import { keys } from '@/api/queries'
import { useProjectFilter } from '@/lib/appStore'
import { entityHealth } from '@/lib/status'
import { HealthBadge } from './StatusBadge'
import { KindIcon } from './common'

const pages: { label: string; to: string; keywords: string }[] = [
  { label: 'Dashboard', to: '/', keywords: 'home overview summary' },
  { label: 'Inventory', to: '/inventory', keywords: 'entities table list' },
  { label: 'Map', to: '/map', keywords: 'graph topology relationships' },
  { label: 'Relationships (table)', to: '/relations', keywords: 'edges dependencies accessible' },
  { label: 'Findings', to: '/findings', keywords: 'problems drift conflicts expiring' },
  { label: 'Alerts', to: '/alerts', keywords: 'rules silences firing' },
  { label: 'Sources', to: '/sources', keywords: 'integrations providers sync tokens' },
  { label: 'Kinds', to: '/kinds', keywords: 'schema custom types' },
  { label: 'Relationship types', to: '/relationship-types', keywords: 'edges colors' },
  { label: 'Projects', to: '/settings/projects', keywords: 'members roles' },
  { label: 'Account & security', to: '/settings/account', keywords: 'password mfa totp sessions tokens' },
  { label: 'Audit log', to: '/settings/audit', keywords: 'history security verify' },
]

/**
 * Spotlight search (/ or ⌘K): full-text entity search through GET /entities?q= plus page navigation. On the map,
 * choosing an entity focuses it on the map instead of opening its detail page.
 */
export function GlobalSearch() {
  const [query, setQuery] = useState('')
  const [debounced] = useDebouncedValue(query.trim(), 200)
  const projects = useProjectFilter()
  const navigate = useNavigate()
  const onMap = useRouterState({ select: (s) => s.location.pathname === '/map' })
  const results = useQuery({
    queryKey: keys.entities({ q: debounced, limit: 8, project: projects, spotlight: true }),
    queryFn: async () =>
      unwrap(
        await api.GET('/entities', {
          params: { query: { q: debounced, limit: 8, ...(projects.length ? { project: projects } : {}) } },
        }),
      ),
    enabled: debounced.length >= 2,
    placeholderData: keepPreviousData,
    staleTime: 15_000,
  })
  const q = query.trim().toLowerCase()
  const pageHits = pages
    .filter((p) => q === '' || `${p.label} ${p.keywords}`.toLowerCase().includes(q))
    .slice(0, q ? 4 : 6)
  const entities = debounced.length >= 2 ? (results.data?.items ?? []) : []

  return (
    <Spotlight.Root query={query} onQueryChange={setQuery} shortcut={null} scrollable maxHeight={480}>
      <Spotlight.Search
        placeholder={onMap ? 'Find an entity to focus on the map…' : 'Search entities, or jump to a page…'}
        leftSection={results.isFetching ? <Loader size={14} /> : <IconSearch size={16} stroke={1.6} />}
        aria-label="Search"
      />
      <Spotlight.ActionsList>
        {entities.length > 0 && (
          <Spotlight.ActionsGroup label={onMap ? 'Focus on map' : 'Entities'}>
            {entities.map((e) => {
              const h = entityHealth(e.status)
              return (
                <Spotlight.Action
                  key={e.metadata.id}
                  onClick={() => {
                    if (onMap) void navigate({ to: '/map', search: { focus: e.metadata.id } })
                    else void navigate({ to: '/entities/$id', params: { id: e.metadata.id } })
                  }}
                >
                  <Group wrap="nowrap" gap="sm" w="100%">
                    <KindIcon kind={e.kind} size={18} />
                    <div style={{ flex: 1, minWidth: 0 }}>
                      <Text size="sm" fw={500} truncate>
                        {e.metadata.displayName || e.metadata.name}
                      </Text>
                      <Text size="xs" c="dimmed" truncate>
                        {e.kind} · {e.metadata.project}
                        {e.metadata.environment ? ` · ${e.metadata.environment}` : ''}
                      </Text>
                    </div>
                    <HealthBadge state={h.state} size="xs" />
                  </Group>
                </Spotlight.Action>
              )
            })}
          </Spotlight.ActionsGroup>
        )}
        {debounced.length >= 2 && results.isSuccess && entities.length === 0 && (
          <Spotlight.Empty>No entities match “{debounced}”.</Spotlight.Empty>
        )}
        {pageHits.length > 0 && (
          <Spotlight.ActionsGroup label="Pages">
            {pageHits.map((p) => (
              <Spotlight.Action key={p.to} onClick={() => void navigate({ to: p.to })}>
                <Group justify="space-between" w="100%">
                  <Text size="sm">{p.label}</Text>
                  <Badge size="xs" variant="outline" color="gray">
                    page
                  </Badge>
                </Group>
              </Spotlight.Action>
            ))}
          </Spotlight.ActionsGroup>
        )}
        {q.length > 0 && q.length < 2 && (
          <Spotlight.Empty>Type at least two characters to search entities.</Spotlight.Empty>
        )}
      </Spotlight.ActionsList>
    </Spotlight.Root>
  )
}
