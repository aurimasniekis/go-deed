import { Badge, Box, Button, Group, Menu, Stack, Tabs, Text, Title, Tooltip } from '@mantine/core'
import {
  IconArrowDownRight,
  IconArrowUpLeft,
  IconChevronDown,
  IconEdit,
  IconFileCode,
  IconGitMerge,
  IconPencil,
  IconTopologyStar3,
} from '@tabler/icons-react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useMemo, useState } from 'react'
import { projectAtLeast } from '@/api/auth'
import { useProjects } from '@/api/queries'
import { Page } from '@/components/AppShell'
import { KindIcon, useKindMap } from '@/components/common'
import { RefIcon } from '@/components/icons'
import { ErrorState, LoadingRows } from '@/components/States'
import { FreshnessBadge, HealthBadge } from '@/components/StatusBadge'
import { useTitle } from '@/lib/appStore'
import { entityHealth } from '@/lib/status'
import { AuditTab, HistoryTab } from './ActivityTabs'
import { OperationalTab } from './OperationalTab'
import { AnnotateModal, DeclareModal, MergeModal, RenameModal } from './Actions'
import { CompareTab, ConflictsTab, ProvenanceTab, SourcesTab } from './DataTabs'
import { OverviewTab } from './OverviewTab'
import { useEntity, useProvenance } from './queries'
import { RelationsTab } from './RelationsTab'
import { ProvenanceContext } from './Why'

export const ENTITY_TABS = [
  'overview',
  'compare',
  'provenance',
  'conflicts',
  'relations',
  'operational',
  'history',
  'audit',
  'sources',
] as const
export type EntityTab = (typeof ENTITY_TABS)[number]

export function EntityPage({
  id,
  tab,
  ptr,
  setTab,
}: {
  id: string
  tab: EntityTab
  ptr?: string
  setTab: (t: EntityTab) => void
}) {
  const q = useEntity(id)
  const prov = useProvenance(id)
  const projects = useProjects()
  const kinds = useKindMap()
  const navigate = useNavigate()
  const [modal, setModal] = useState<'annotate' | 'declare' | 'rename' | 'merge' | null>(null)
  const e = q.data
  useTitle(e ? e.metadata.displayName || e.metadata.name : 'Entity')
  const ctx = useMemo(() => ({ entityId: id, prov: prov.data, effective: e }), [id, prov.data, e])

  if (q.isPending) {
    return (
      <Page>
        <LoadingRows rows={8} label="Loading entity" />
      </Page>
    )
  }
  if (!e) {
    return (
      <Page>
        <ErrorState error={q.error} what="this entity" onRetry={() => void q.refetch()} />
      </Page>
    )
  }
  const m = e.metadata
  const project = projects.data?.find((p) => p.id === m.projectId)
  const editor = projectAtLeast(project, 'editor')
  const admin = projectAtLeast(project, 'admin')
  const h = entityHealth(e.status)
  const kind = kinds.get(e.kind)

  return (
    <ProvenanceContext.Provider value={ctx}>
      <Page>
        <Stack gap="sm" mb="md">
          <Text size="xs" c="dimmed">
            <Link to="/inventory" style={{ color: 'inherit' }}>
              Inventory
            </Link>{' '}
            ›{' '}
            <Link to="/inventory" search={{ kind: e.kind }} style={{ color: 'inherit' }}>
              {kind?.plural || kind?.title || e.kind}
            </Link>{' '}
            › {m.name}
          </Text>
          <Group justify="space-between" align="flex-start" wrap="wrap" gap="sm">
            <Group gap="sm" wrap="nowrap" align="flex-start">
              <Box mt={2}>{m.icon ? <RefIcon iconRef={m.icon} size={28} /> : <KindIcon kind={e.kind} size={28} />}</Box>
              <div>
                <Title order={1} data-testid="entity-title">
                  {m.displayName || m.name}
                </Title>
                <Group gap={6} mt={4}>
                  <Text size="sm" c="dimmed">
                    {kind?.title ?? e.kind} · {project?.display_name || m.project}
                    {m.displayName && m.displayName !== m.name ? ` · ${m.name}` : ''}
                  </Text>
                  <HealthBadge state={h.state} reason={h.reason} />
                  <FreshnessBadge state={e.status.freshness.state} />
                  {m.lifecycle && (
                    <Badge
                      variant="outline"
                      color={m.lifecycle === 'deprecated' || m.lifecycle === 'retired' ? 'orange' : 'gray'}
                    >
                      {m.lifecycle}
                    </Badge>
                  )}
                  {!e.status.declared && (
                    <Tooltip label="No person, Terraform or API declares this entity; it exists because a source observed it.">
                      <Badge variant="light" color="blue">
                        observed only
                      </Badge>
                    </Tooltip>
                  )}
                  {!e.status.valid && <Badge color="red">invalid schema</Badge>}
                  {project?.synthetic && (
                    <Badge color="yellow" variant="light">
                      synthetic
                    </Badge>
                  )}
                </Group>
              </div>
            </Group>
            <Group gap="xs">
              <Button
                leftSection={<IconTopologyStar3 size={14} />}
                onClick={() => void navigate({ to: '/map', search: { focus: id, mode: 'neighborhood', depth: 2 } })}
                data-testid="open-in-map"
              >
                Open in map
              </Button>
              <Menu position="bottom-end" withinPortal>
                <Menu.Target>
                  <Button variant="default" rightSection={<IconChevronDown size={14} />}>
                    Impact
                  </Button>
                </Menu.Target>
                <Menu.Dropdown>
                  <Menu.Label>Potential impact (evidence-based)</Menu.Label>
                  <Menu.Item
                    leftSection={<IconArrowDownRight size={14} />}
                    onClick={() =>
                      void navigate({ to: '/map', search: { focus: id, mode: 'impact', impact: 'downstream' } })
                    }
                  >
                    Downstream: what may break if this fails
                  </Menu.Item>
                  <Menu.Item
                    leftSection={<IconArrowUpLeft size={14} />}
                    onClick={() =>
                      void navigate({ to: '/map', search: { focus: id, mode: 'impact', impact: 'upstream' } })
                    }
                  >
                    Upstream: what this depends on
                  </Menu.Item>
                </Menu.Dropdown>
              </Menu>
              {editor && (
                <Menu position="bottom-end" withinPortal>
                  <Menu.Target>
                    <Button variant="default" rightSection={<IconChevronDown size={14} />} data-testid="entity-actions">
                      Edit
                    </Button>
                  </Menu.Target>
                  <Menu.Dropdown>
                    <Menu.Item leftSection={<IconEdit size={14} />} onClick={() => setModal('annotate')}>
                      Edit annotations…
                    </Menu.Item>
                    <Menu.Item leftSection={<IconFileCode size={14} />} onClick={() => setModal('declare')}>
                      Declare via API…
                    </Menu.Item>
                    <Menu.Item leftSection={<IconPencil size={14} />} onClick={() => setModal('rename')}>
                      Rename…
                    </Menu.Item>
                    {admin && (
                      <>
                        <Menu.Divider />
                        <Menu.Item
                          leftSection={<IconGitMerge size={14} />}
                          color="red"
                          onClick={() => setModal('merge')}
                        >
                          Merge a duplicate…
                        </Menu.Item>
                      </>
                    )}
                  </Menu.Dropdown>
                </Menu>
              )}
            </Group>
          </Group>
        </Stack>

        <Tabs value={tab} onChange={(v) => setTab((v ?? 'overview') as EntityTab)} keepMounted={false}>
          <Tabs.List mb="md">
            <Tabs.Tab value="overview">Overview</Tabs.Tab>
            <Tabs.Tab value="compare">Declared vs observed</Tabs.Tab>
            <Tabs.Tab value="provenance">Provenance</Tabs.Tab>
            <Tabs.Tab
              value="conflicts"
              rightSection={
                e.status.conflicts ? (
                  <Badge size="xs" color="orange">
                    {e.status.conflicts}
                  </Badge>
                ) : null
              }
            >
              Conflicts
            </Tabs.Tab>
            <Tabs.Tab value="relations">Relations</Tabs.Tab>
            <Tabs.Tab value="operational">Operational</Tabs.Tab>
            <Tabs.Tab value="history">History</Tabs.Tab>
            <Tabs.Tab value="audit">Audit</Tabs.Tab>
            <Tabs.Tab
              value="sources"
              rightSection={
                <Badge size="xs" variant="outline" color="gray">
                  {e.status.facets}
                </Badge>
              }
            >
              Sources
            </Tabs.Tab>
          </Tabs.List>
          <Tabs.Panel value="overview">
            <OverviewTab e={e} project={project} />
          </Tabs.Panel>
          <Tabs.Panel value="compare">
            <CompareTab e={e} />
          </Tabs.Panel>
          <Tabs.Panel value="provenance">
            <ProvenanceTab e={e} highlight={ptr} />
          </Tabs.Panel>
          <Tabs.Panel value="conflicts">
            <ConflictsTab e={e} />
          </Tabs.Panel>
          <Tabs.Panel value="relations">
            <RelationsTab e={e} />
          </Tabs.Panel>
          <Tabs.Panel value="operational">
            <OperationalTab e={e} />
          </Tabs.Panel>
          <Tabs.Panel value="history">
            <HistoryTab e={e} />
          </Tabs.Panel>
          <Tabs.Panel value="audit">
            <AuditTab e={e} />
          </Tabs.Panel>
          <Tabs.Panel value="sources">
            <SourcesTab e={e} />
          </Tabs.Panel>
        </Tabs>
        {modal === 'annotate' && (
          <AnnotateModal e={e} opened onClose={() => setModal(null)} allowHttp={project?.allow_http_links ?? false} />
        )}
        {modal === 'declare' && <DeclareModal e={e} opened onClose={() => setModal(null)} />}
        {modal === 'rename' && <RenameModal e={e} opened onClose={() => setModal(null)} />}
        {modal === 'merge' && <MergeModal e={e} opened onClose={() => setModal(null)} />}
      </Page>
    </ProvenanceContext.Provider>
  )
}
