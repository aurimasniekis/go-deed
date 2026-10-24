import { ButtonLink } from '@/components/links'
import {
  ActionIcon,
  Badge,
  Box,
  Button,
  Checkbox,
  CloseButton,
  Group,
  Menu,
  MultiSelect,
  Popover,
  SegmentedControl,
  Select,
  Stack,
  TagsInput,
  Text,
  TextInput,
  Tooltip,
} from '@mantine/core'
import { useDebouncedCallback, useHotkeys } from '@mantine/hooks'
import {
  IconAdjustmentsHorizontal,
  IconArrowDown,
  IconArrowUp,
  IconArrowsSort,
  IconColumns3,
  IconDownload,
  IconEdit,
  IconFilterOff,
  IconSearch,
  IconTopologyStar3,
} from '@tabler/icons-react'
import { keepPreviousData, useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { flexRender, getCoreRowModel, useReactTable, type RowSelectionState } from '@tanstack/react-table'
import { useVirtualizer } from '@tanstack/react-virtual'
import { useEffect, useMemo, useRef, useState } from 'react'
import { api, unwrap, type View } from '@/api/client'
import { keys, useKinds, useProjects, useSources } from '@/api/queries'
import { Page } from '@/components/AppShell'
import { PageHeader } from '@/components/common'
import { EmptyState, ErrorState, LoadingRows, StaleNotice } from '@/components/States'
import { SavedViewsMenu } from '@/features/views/SavedViews'
import { useProjectFilter, useTitle } from '@/lib/appStore'
import { downloadText, toCsv } from '@/lib/csv'
import { count } from '@/lib/format'
import { readPref, writePref } from '@/lib/prefs'
import { FRESHNESS, HEALTH } from '@/lib/status'
import {
  compact,
  decodeLabels,
  decodeList,
  encodeLabels,
  encodeList,
  inventoryQuery,
  validateInventorySearch,
  type InventorySearch,
} from '@/lib/urlState'
import { BulkActionsModal } from './BulkActions'
import { COLUMN_LABELS, DEFAULT_COLUMNS, entityColumns, entityCsvRow, SORT_FIELD } from './columns'

const ROW_HEIGHT = 44
const LIFECYCLES = ['planned', 'active', 'deprecated', 'retired']

/** Filters stored in saved views (everything except presentation-only and paging state). */
function viewState(s: InventorySearch): Record<string, unknown> {
  const { view: _v, cursor: _c, ...rest } = s
  return rest
}

export function InventoryPage({
  search,
  setSearch,
}: {
  search: InventorySearch
  setSearch: (s: InventorySearch) => void
}) {
  useTitle('Inventory')
  const navigate = useNavigate()
  const globalProjects = useProjectFilter()
  const kinds = useKinds()
  const projects = useProjects()
  const sources = useSources()
  const facets = useQuery({
    queryKey: keys.dashboard(globalProjects),
    queryFn: async () =>
      unwrap(
        await api.GET('/dashboard', { params: { query: globalProjects.length ? { project: globalProjects } : {} } }),
      ),
    staleTime: 60_000,
  })
  const query = inventoryQuery(search, globalProjects)
  const list = useInfiniteQuery({
    queryKey: keys.entities(query),
    queryFn: async ({ pageParam }) =>
      unwrap(
        await api.GET('/entities', { params: { query: { ...query, ...(pageParam ? { cursor: pageParam } : {}) } } }),
      ),
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor || undefined,
    placeholderData: keepPreviousData,
  })
  const rows = useMemo(() => list.data?.pages.flatMap((p) => p.items) ?? [], [list.data])
  const total = list.data?.pages[0]?.total ?? 0

  const defaultCols = useMemo(() => readPref<string[]>('inventory.cols', DEFAULT_COLUMNS), [])
  const visible = useMemo(() => {
    const fromUrl = decodeList(search.cols)
    return fromUrl.length ? fromUrl : defaultCols
  }, [search.cols, defaultCols])

  const [selection, setSelection] = useState<RowSelectionState>({})
  const [bulkOpen, setBulkOpen] = useState(false)
  const columns = useMemo(() => entityColumns(), [])
  // TanStack Table returns new functions every render; the React Compiler skips this component, which is expected.
  // eslint-disable-next-line react-hooks/incompatible-library
  const table = useReactTable({
    data: rows,
    columns,
    getCoreRowModel: getCoreRowModel(),
    getRowId: (e) => e.metadata.id,
    state: {
      rowSelection: selection,
      columnVisibility: Object.fromEntries(Object.keys(COLUMN_LABELS).map((c) => [c, visible.includes(c)])),
    },
    onRowSelectionChange: setSelection,
    enableRowSelection: true,
    manualSorting: true,
  })
  const selected = rows.filter((e) => selection[e.metadata.id])

  // Selection survives refetches but not filter changes.
  const filterKey = JSON.stringify(query)
  useEffect(() => {
    setSelection({})
  }, [filterKey])

  const scrollRef = useRef<HTMLDivElement>(null)
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 12,
  })
  const items = virtualizer.getVirtualItems()
  const padTop = items[0]?.start ?? 0
  const padBottom = items.length ? virtualizer.getTotalSize() - (items[items.length - 1]?.end ?? 0) : 0

  // Infinite scroll: fetch the next page when the last rows come into view.
  const lastIndex = items[items.length - 1]?.index ?? 0
  useEffect(() => {
    if (lastIndex >= rows.length - 10 && list.hasNextPage && !list.isFetchingNextPage) void list.fetchNextPage()
  }, [lastIndex, rows.length, list])

  const searchBox = useRef<HTMLInputElement>(null)
  const [q, setQ] = useState(search.q ?? '')
  useEffect(() => setQ(search.q ?? ''), [search.q])
  const pushQ = useDebouncedCallback((v: string) => setSearch({ ...search, q: v || undefined }), 250)
  useHotkeys([['/', () => searchBox.current?.focus(), { preventDefault: true }]], ['INPUT', 'TEXTAREA'])

  const patch = (p: Partial<InventorySearch>) => setSearch(compact({ ...search, ...p, view: p.view ?? search.view }))
  const sortField = search.sort?.replace(/^-/, '')
  const sortDesc = search.sort?.startsWith('-') ?? false

  const activeFilters: { key: keyof InventorySearch; label: string }[] = []
  for (const [k, label] of [
    ['kind', 'Kind'],
    ['project', 'Project'],
    ['environment', 'Environment'],
    ['owner', 'Owner'],
    ['health', 'Health'],
    ['freshness', 'Freshness'],
    ['lifecycle', 'Lifecycle'],
    ['label', 'Labels'],
    ['source', 'Source'],
  ] as const) {
    const v = search[k]
    if (v)
      activeFilters.push({
        key: k,
        label: `${label}: ${k === 'source' ? (sources.data?.find((s) => s.id === v)?.name ?? v) : v.replace(/[,;]/g, ', ')}`,
      })
  }
  if (search.declared !== undefined)
    activeFilters.push({ key: 'declared', label: search.declared ? 'Declared' : 'Observed only' })
  if (search.valid !== undefined) activeFilters.push({ key: 'valid', label: search.valid ? 'Valid' : 'Invalid schema' })

  const exportCsv = () => {
    const cols = visible.filter((c) => c !== 'select')
    downloadText(
      `deed-inventory-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')}.csv`,
      toCsv(
        ['id', ...cols.map((c) => COLUMN_LABELS[c] ?? c)],
        rows.map((e) => entityCsvRow(e, cols)),
      ),
    )
  }

  const applyView = (v: View | null) => {
    if (!v) {
      setSearch(compact({ ...search, view: undefined }))
      return
    }
    const state = (v.state && typeof v.state === 'object' ? v.state : {}) as Record<string, unknown>
    setSearch({ ...validateInventorySearch(state), view: v.id })
  }

  const envOptions = Object.keys(facets.data?.counts.by_environment ?? {}).filter(Boolean)
  const ownerOptions = Object.keys(facets.data?.counts.by_owner ?? {}).filter(Boolean)

  return (
    <Page wide>
      <PageHeader
        title="Inventory"
        description="Every entity you can read, merged from all sources. Filters, sort and columns live in the URL — share the link."
        actions={
          <>
            <SavedViewsMenu
              kind="inventory"
              activeId={search.view}
              currentState={viewState(search)}
              onApply={applyView}
            />
            <Button
              variant="default"
              leftSection={<IconTopologyStar3 size={14} />}
              disabled={rows.length === 0}
              onClick={() =>
                void navigate({
                  to: '/map',
                  search: {
                    focus: encodeList(rows.slice(0, 20).map((e) => e.metadata.id)),
                    mode: 'neighborhood',
                    depth: 1,
                  },
                })
              }
            >
              Open in map
            </Button>
            <Tooltip label="Download the loaded rows as CSV">
              <Button
                variant="default"
                leftSection={<IconDownload size={14} />}
                onClick={exportCsv}
                disabled={rows.length === 0}
                data-testid="export-csv"
              >
                CSV
              </Button>
            </Tooltip>
          </>
        }
      />

      <Group gap="xs" mb="xs" wrap="wrap" align="flex-end">
        <TextInput
          ref={searchBox}
          aria-label="Search entities"
          placeholder="Search names, descriptions, labels, spec values…  ( / )"
          leftSection={<IconSearch size={14} />}
          value={q}
          onChange={(e) => {
            setQ(e.currentTarget.value)
            pushQ(e.currentTarget.value)
          }}
          rightSection={
            q ? (
              <CloseButton
                size="sm"
                aria-label="Clear search"
                onClick={() => {
                  setQ('')
                  patch({ q: undefined })
                }}
              />
            ) : null
          }
          w={340}
          data-testid="inventory-search"
        />
        <MultiSelect
          aria-label="Kind"
          placeholder="Kind"
          data={(kinds.data ?? []).map((k) => ({ value: k.name, label: k.title }))}
          value={decodeList(search.kind)}
          onChange={(v) => patch({ kind: encodeList(v) })}
          searchable
          clearable
          w={200}
          data-testid="filter-kind"
        />
        <MultiSelect
          aria-label="Health"
          placeholder="Health"
          data={Object.entries(HEALTH).map(([value, m]) => ({ value, label: m.label }))}
          value={decodeList(search.health)}
          onChange={(v) => patch({ health: encodeList(v) })}
          clearable
          w={160}
        />
        <MultiSelect
          aria-label="Freshness"
          placeholder="Freshness"
          data={Object.entries(FRESHNESS).map(([value, m]) => ({ value, label: m.label }))}
          value={decodeList(search.freshness)}
          onChange={(v) => patch({ freshness: encodeList(v) })}
          clearable
          w={160}
        />
        <MultiSelect
          aria-label="Environment"
          placeholder="Environment"
          data={[...new Set([...envOptions, ...decodeList(search.environment)])]}
          value={decodeList(search.environment)}
          onChange={(v) => patch({ environment: encodeList(v) })}
          clearable
          searchable
          w={170}
        />
        <Popover position="bottom-start" width={340} shadow="md" withinPortal trapFocus>
          <Popover.Target>
            <Button variant="default" leftSection={<IconAdjustmentsHorizontal size={14} />}>
              More filters
            </Button>
          </Popover.Target>
          <Popover.Dropdown>
            <Stack gap="xs">
              <MultiSelect
                label="Project"
                data={(projects.data ?? []).map((p) => ({ value: p.name, label: p.display_name || p.name }))}
                value={decodeList(search.project)}
                onChange={(v) => patch({ project: encodeList(v) })}
                clearable
                comboboxProps={{ withinPortal: false }}
              />
              <Select
                label="Owner"
                data={[...new Set([...ownerOptions, ...(search.owner ? [search.owner] : [])])]}
                value={search.owner ?? null}
                onChange={(v) => patch({ owner: v ?? undefined })}
                clearable
                searchable
                comboboxProps={{ withinPortal: false }}
              />
              <MultiSelect
                label="Lifecycle"
                data={LIFECYCLES}
                value={decodeList(search.lifecycle)}
                onChange={(v) => patch({ lifecycle: encodeList(v) })}
                clearable
                comboboxProps={{ withinPortal: false }}
              />
              <TagsInput
                label="Labels"
                description="key=value; all must match"
                value={decodeLabels(search.label)}
                onChange={(v) => patch({ label: encodeLabels(v) })}
                splitChars={[';']}
                clearable
              />
              <Select
                label="Source"
                description="Entities with a live fact from this source"
                data={(sources.data ?? []).map((s) => ({ value: s.id, label: s.display_name || s.name }))}
                value={search.source ?? null}
                onChange={(v) => patch({ source: v ?? undefined })}
                clearable
                searchable
                comboboxProps={{ withinPortal: false }}
              />
              <Text size="sm" fw={500}>
                Declaration
              </Text>
              <SegmentedControl
                value={search.declared === undefined ? 'any' : search.declared ? 'yes' : 'no'}
                onChange={(v) => patch({ declared: v === 'any' ? undefined : v === 'yes' })}
                data={[
                  { value: 'any', label: 'Any' },
                  { value: 'yes', label: 'Declared' },
                  { value: 'no', label: 'Observed only' },
                ]}
              />
              <Text size="sm" fw={500}>
                Schema
              </Text>
              <SegmentedControl
                value={search.valid === undefined ? 'any' : search.valid ? 'yes' : 'no'}
                onChange={(v) => patch({ valid: v === 'any' ? undefined : v === 'yes' })}
                data={[
                  { value: 'any', label: 'Any' },
                  { value: 'yes', label: 'Valid' },
                  { value: 'no', label: 'Invalid' },
                ]}
              />
            </Stack>
          </Popover.Dropdown>
        </Popover>
        <Menu closeOnItemClick={false} position="bottom-end" withinPortal>
          <Menu.Target>
            <Button variant="default" leftSection={<IconColumns3 size={14} />} data-testid="column-chooser">
              Columns
            </Button>
          </Menu.Target>
          <Menu.Dropdown>
            <Menu.Label>Visible columns</Menu.Label>
            {Object.entries(COLUMN_LABELS).map(([id, label]) => (
              <Menu.Item key={id} py={4}>
                <Checkbox
                  label={label}
                  checked={visible.includes(id)}
                  disabled={id === 'name'}
                  onChange={(e) => {
                    const next = e.currentTarget.checked ? [...visible, id] : visible.filter((c) => c !== id)
                    const ordered = Object.keys(COLUMN_LABELS).filter((c) => next.includes(c))
                    writePref('inventory.cols', ordered)
                    patch({ cols: encodeList(ordered) })
                  }}
                />
              </Menu.Item>
            ))}
            <Menu.Divider />
            <Menu.Item
              onClick={() => {
                writePref('inventory.cols', DEFAULT_COLUMNS)
                patch({ cols: undefined })
              }}
            >
              Reset to default
            </Menu.Item>
          </Menu.Dropdown>
        </Menu>
      </Group>

      <Group gap="xs" mb="xs" mih={26}>
        <Text size="sm" c="dimmed" data-testid="inventory-count">
          {list.isPending
            ? 'Loading…'
            : `${count(total)} ${total === 1 ? 'entity' : 'entities'}${rows.length < total ? ` · ${count(rows.length)} loaded` : ''}`}
          {globalProjects.length > 0 && !search.project ? ' · scoped by the project filter' : ''}
        </Text>
        {activeFilters.map((f) => (
          <Badge
            key={f.key}
            variant="light"
            color="gray"
            rightSection={
              <CloseButton
                size="xs"
                aria-label={`Remove filter ${f.label}`}
                onClick={() => patch({ [f.key]: undefined })}
              />
            }
            styles={{ label: { textTransform: 'none' } }}
          >
            {f.label}
          </Badge>
        ))}
        {(activeFilters.length > 0 || search.q) && (
          <Button
            size="compact-xs"
            variant="subtle"
            leftSection={<IconFilterOff size={12} />}
            onClick={() => setSearch(compact({ cols: search.cols, sort: search.sort }))}
          >
            Clear all
          </Button>
        )}
        {selected.length > 0 && (
          <Group gap="xs" ml="auto" data-testid="bulk-bar">
            <Text size="sm" fw={500}>
              {selected.length} selected
            </Text>
            <Button size="compact-sm" leftSection={<IconEdit size={14} />} onClick={() => setBulkOpen(true)}>
              Bulk edit
            </Button>
            <Button size="compact-sm" variant="default" onClick={() => setSelection({})}>
              Clear selection
            </Button>
          </Group>
        )}
      </Group>

      {list.isError && list.data && <StaleNotice updatedAt={list.dataUpdatedAt} onRetry={() => void list.refetch()} />}
      {list.isPending ? (
        <LoadingRows rows={10} height={36} label="Loading entities" />
      ) : list.isError && !list.data ? (
        <ErrorState error={list.error} what="the inventory" onRetry={() => void list.refetch()} />
      ) : rows.length === 0 ? (
        activeFilters.length > 0 || search.q ? (
          <EmptyState
            title="No entities match"
            action={
              <Button variant="default" onClick={() => setSearch(compact({ cols: search.cols }))}>
                Clear filters
              </Button>
            }
          >
            Try a broader search or remove a filter. Full-text search covers names, descriptions, labels, spec values
            and external IDs.
          </EmptyState>
        ) : (
          <EmptyState
            title="The catalog is empty"
            action={
              <Group>
                <ButtonLink to="/sources">Add a source</ButtonLink>
              </Group>
            }
          >
            Declare entities with the Terraform provider or the API, or connect a source to import and discover them.
          </EmptyState>
        )
      ) : (
        <Box
          ref={scrollRef}
          style={{
            height: 'calc(100vh - 290px)',
            minHeight: 320,
            overflow: 'auto',
            border: '1px solid var(--deed-border)',
            borderRadius: 4,
            background: 'var(--deed-surface)',
          }}
          data-testid="inventory-table"
          tabIndex={0}
          aria-label="Inventory table (scrollable)"
        >
          <table
            style={{ width: '100%', borderCollapse: 'separate', borderSpacing: 0, tableLayout: 'fixed', fontSize: 13 }}
            aria-rowcount={total + 1}
          >
            <thead style={{ position: 'sticky', top: 0, zIndex: 1, background: 'var(--deed-surface-2)' }}>
              {table.getHeaderGroups().map((hg) => (
                <tr key={hg.id}>
                  {hg.headers.map((h) => {
                    const field = SORT_FIELD[h.column.id]
                    const active = field && sortField === field
                    return (
                      <th
                        key={h.id}
                        style={{
                          width: h.column.getSize(),
                          textAlign: 'left',
                          padding: '6px 8px',
                          fontWeight: 600,
                          borderBottom: '1px solid var(--deed-border)',
                          whiteSpace: 'nowrap',
                        }}
                        aria-sort={active ? (sortDesc ? 'descending' : 'ascending') : undefined}
                        scope="col"
                      >
                        {field ? (
                          <Group gap={4} wrap="nowrap">
                            {flexRender(h.column.columnDef.header, h.getContext())}
                            <ActionIcon
                              size="xs"
                              aria-label={`Sort by ${COLUMN_LABELS[h.column.id] ?? h.column.id}`}
                              onClick={() =>
                                patch({
                                  sort: active && !sortDesc ? `-${field}` : active && sortDesc ? undefined : field,
                                })
                              }
                            >
                              {active ? (
                                sortDesc ? (
                                  <IconArrowDown size={12} />
                                ) : (
                                  <IconArrowUp size={12} />
                                )
                              ) : (
                                <IconArrowsSort size={12} />
                              )}
                            </ActionIcon>
                          </Group>
                        ) : (
                          flexRender(h.column.columnDef.header, h.getContext())
                        )}
                      </th>
                    )
                  })}
                </tr>
              ))}
            </thead>
            <tbody>
              {padTop > 0 && (
                <tr aria-hidden>
                  <td style={{ height: padTop, padding: 0 }} colSpan={table.getVisibleLeafColumns().length} />
                </tr>
              )}
              {items.map((vi) => {
                const row = table.getRowModel().rows[vi.index]
                if (!row) return null
                return (
                  <tr
                    key={row.id}
                    data-index={vi.index}
                    aria-rowindex={vi.index + 2}
                    aria-selected={row.getIsSelected()}
                    style={{
                      height: ROW_HEIGHT,
                      background: row.getIsSelected() ? 'var(--mantine-primary-color-light)' : undefined,
                    }}
                    data-testid="inventory-row"
                  >
                    {row.getVisibleCells().map((cell) => (
                      <td
                        key={cell.id}
                        style={{
                          padding: '4px 8px',
                          borderBottom: '1px solid var(--deed-border)',
                          overflow: 'hidden',
                          whiteSpace: 'nowrap',
                          textOverflow: 'ellipsis',
                        }}
                      >
                        {flexRender(cell.column.columnDef.cell, cell.getContext())}
                      </td>
                    ))}
                  </tr>
                )
              })}
              {padBottom > 0 && (
                <tr aria-hidden>
                  <td style={{ height: padBottom, padding: 0 }} colSpan={table.getVisibleLeafColumns().length} />
                </tr>
              )}
            </tbody>
          </table>
          {list.isFetchingNextPage && (
            <Text size="xs" c="dimmed" ta="center" py="xs">
              Loading more…
            </Text>
          )}
        </Box>
      )}
      <BulkActionsModal
        opened={bulkOpen}
        entities={selected}
        onClose={() => setBulkOpen(false)}
        onDone={() => setSelection({})}
      />
    </Page>
  )
}
