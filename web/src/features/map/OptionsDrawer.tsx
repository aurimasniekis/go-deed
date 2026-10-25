import {
  Button,
  ColorInput,
  Divider,
  Drawer,
  Group,
  MultiSelect,
  SegmentedControl,
  Select,
  Slider,
  Stack,
  Switch,
  Text,
  Title,
} from '@mantine/core'
import { useReducedMotion } from '@mantine/hooks'
import { notifications } from '@mantine/notifications'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api, errorMessage, unwrap, type RelType } from '@/api/client'
import { useKinds, useProjects, useSources } from '@/api/queries'
import { EVIDENCE, FRESHNESS, HEALTH } from '@/lib/status'
import { decodeList, encodeList, MAP_CLUSTERING, type MapSearch } from '@/lib/urlState'
import type { ColorMode, Routing } from './edgeStyle'
import { setStyle, type MapStyle } from './mapStore'

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <Stack gap={6}>
      <Title order={3} fz={13}>
        {title}
      </Title>
      {children}
    </Stack>
  )
}

/**
 * The map options drawer (ADR-011c): filters, grouping, layout, edge colour mode and palette, routing, animation modes,
 * labels, icon size and node cards. Filters and grouping go to the URL; styles to the map store (saved with views).
 */
export function OptionsDrawer({
  opened,
  onClose,
  search,
  patch,
  style,
  relTypes,
  presentTypes,
  envOptions,
  ownerOptions,
  canEditTypes,
}: {
  opened: boolean
  onClose: () => void
  search: MapSearch
  patch: (p: Partial<MapSearch>) => void
  style: MapStyle
  relTypes: RelType[]
  presentTypes: string[]
  envOptions: string[]
  ownerOptions: string[]
  canEditTypes: boolean
}) {
  const reduced = useReducedMotion()
  const kinds = useKinds()
  const projects = useProjects()
  const sources = useSources()
  const qc = useQueryClient()
  // Built-in types keep their definition; the colour becomes an installation override (reset on the
  // relationship-types page).
  const saveTypeColor = useMutation({
    mutationFn: async ({ t, color }: { t: RelType; color: string }) =>
      unwrap(
        await api.PUT('/relationship-types/{name}/color', {
          params: { path: { name: t.name }, header: { 'If-Match': String(t.resource_version) } },
          body: { color },
        }),
      ),
    onSuccess: (t) => {
      void qc.invalidateQueries({ queryKey: ['relationship-types'] })
      setStyle({ typeColors: Object.fromEntries(Object.entries(style.typeColors).filter(([k]) => k !== t.name)) })
      notifications.show({ color: 'green', message: `Default colour of “${t.title}” saved for everyone` })
    },
    onError: (e) => notifications.show({ color: 'red', title: 'Could not save the colour', message: errorMessage(e) }),
  })
  const list = (k: keyof MapSearch) => decodeList(search[k])
  const set = (k: keyof MapSearch) => (v: string[]) => patch({ [k]: encodeList(v) })
  const typeTitle = new Map(relTypes.map((t) => [t.name, t]))
  const shownTypes = presentTypes.length ? presentTypes : relTypes.map((t) => t.name)

  return (
    <Drawer
      opened={opened}
      onClose={onClose}
      title="Map filters & options"
      position="right"
      size={380}
      withOverlay={false}
      data-testid="map-options"
    >
      <Stack gap="lg">
        <Section title="Show only">
          <MultiSelect
            label="Kinds"
            data={(kinds.data ?? []).map((k) => ({ value: k.name, label: k.title }))}
            value={list('kind')}
            onChange={set('kind')}
            searchable
            clearable
            data-testid="map-filter-kind"
          />
          <MultiSelect
            label="Relationship types"
            data={relTypes.map((t) => ({ value: t.name, label: t.title }))}
            value={list('type')}
            onChange={set('type')}
            searchable
            clearable
          />
          <MultiSelect
            label="Projects"
            data={(projects.data ?? []).map((p) => ({ value: p.id, label: p.display_name || p.name }))}
            value={list('project')}
            onChange={set('project')}
            clearable
          />
          <MultiSelect
            label="Sources"
            description="Entities with a live fact from these sources"
            data={(sources.data ?? []).map((s) => ({ value: s.id, label: s.display_name || s.name }))}
            value={list('source')}
            onChange={set('source')}
            clearable
            searchable
          />
          <MultiSelect
            label="Environments"
            data={[...new Set([...envOptions, ...list('environment')])]}
            value={list('environment')}
            onChange={set('environment')}
            clearable
            searchable
          />
          <MultiSelect
            label="Owners"
            data={[...new Set([...ownerOptions, ...list('owner')])]}
            value={list('owner')}
            onChange={set('owner')}
            clearable
            searchable
          />
          <MultiSelect
            label="Health"
            data={Object.entries(HEALTH).map(([v, m]) => ({ value: v, label: m.label }))}
            value={list('health')}
            onChange={set('health')}
            clearable
          />
          <MultiSelect
            label="Freshness"
            data={Object.entries(FRESHNESS).map(([v, m]) => ({ value: v, label: m.label }))}
            value={list('freshness')}
            onChange={set('freshness')}
            clearable
          />
          <MultiSelect
            label="Evidence"
            data={Object.entries(EVIDENCE).map(([v, m]) => ({ value: v, label: m.label }))}
            value={list('evidence')}
            onChange={set('evidence')}
            clearable
          />
          <Switch
            label="Show stale relationships (dotted)"
            checked={search.stale !== false}
            onChange={(e) => patch({ stale: e.currentTarget.checked ? undefined : false })}
          />
          <Text size="xs" c="dimmed">
            Focus and cluster nodes always stay visible. Kind, type and evidence filters also narrow the server query.
          </Text>
        </Section>

        <Section title="Grouping">
          <Select
            label="Cluster loaded nodes by"
            data={MAP_CLUSTERING.map((c) => ({
              value: c,
              label:
                c === 'none' ? 'No grouping' : c === 'host' ? 'Host (runs on)' : c.charAt(0).toUpperCase() + c.slice(1),
            }))}
            value={search.cluster ?? 'none'}
            onChange={(v) => patch({ cluster: v && v !== 'none' ? (v as MapSearch['cluster']) : undefined })}
            allowDeselect={false}
            data-testid="map-cluster-select"
          />
          <Text size="xs" c="dimmed">
            Groups are drawn as containers; collapse one with its chevron to replace it with a single node.
          </Text>
        </Section>

        <Section title="Layout">
          <SegmentedControl
            value={search.layout ?? 'layered'}
            onChange={(v) => patch({ layout: v === 'layered' ? undefined : (v as MapSearch['layout']) })}
            data={[
              { value: 'layered', label: 'Layered' },
              { value: 'force', label: 'Force' },
              { value: 'radial', label: 'Radial' },
            ]}
            fullWidth
          />
          {(search.layout ?? 'layered') === 'layered' && (
            <SegmentedControl
              value={search.rankdir ?? 'RIGHT'}
              onChange={(v) => patch({ rankdir: v === 'RIGHT' ? undefined : (v as MapSearch['rankdir']) })}
              data={[
                { value: 'RIGHT', label: '→' },
                { value: 'DOWN', label: '↓' },
                { value: 'LEFT', label: '←' },
                { value: 'UP', label: '↑' },
              ]}
              fullWidth
              aria-label="Layout direction"
            />
          )}
          <Text size="xs" c="dimmed">
            Layout runs in a Web Worker and only when the graph’s shape changes; health and freshness updates repaint in
            place. Dragged nodes keep their position (saved with map views).
          </Text>
        </Section>

        <Section title="Edges">
          <Select
            label="Colour by"
            data={[
              { value: 'type', label: 'Relationship type' },
              { value: 'health', label: 'Health (worst endpoint)' },
              { value: 'freshness', label: 'Freshness (last seen)' },
              { value: 'evidence', label: 'Evidence class' },
              { value: 'source', label: 'Source (primary asserting source)' },
            ]}
            value={style.colorMode}
            onChange={(v) => setStyle({ colorMode: (v ?? 'type') as ColorMode })}
            allowDeselect={false}
            data-testid="map-color-mode"
          />
          {style.colorMode === 'source' && (
            <Text size="xs" c="dimmed">
              Each relationship takes the colour of its primary source: the highest-precedence layer, then the oldest
              claim. Every source keeps its colour across reloads. The legend names the colours on screen; hover or
              select a relationship for all the sources asserting it.
            </Text>
          )}
          <SegmentedControl
            value={style.routing}
            onChange={(v) => setStyle({ routing: v as Routing })}
            data={[
              { value: 'bezier', label: 'Bezier' },
              { value: 'smoothstep', label: 'Smooth step' },
              { value: 'straight', label: 'Straight' },
            ]}
            fullWidth
            aria-label="Edge routing"
          />
          <Switch
            label="Mid-edge type labels"
            checked={style.labels}
            onChange={(e) => setStyle({ labels: e.currentTarget.checked })}
          />
          <Divider label="Animation" labelPosition="left" />
          <Switch
            label="Traffic overlay (measured rates)"
            checked={style.traffic}
            onChange={(e) => setStyle({ traffic: e.currentTarget.checked })}
            data-testid="traffic-toggle"
          />
          <Text size="xs" c="dimmed">
            Rates come from edge-rate telemetry bindings and Pulse. Only <b>measured</b> rates widen and animate an edge
            (sampled, 15-minute window). Inferred rates are labelled “~ … inferred” and never animate; edges without a
            measurement are unchanged.{reduced ? ' Animation is off (reduced motion); widths and labels remain.' : ''}
          </Text>
          <Switch
            label="Direction hint (marching dashes — not traffic)"
            checked={style.directionHint && !reduced}
            disabled={reduced}
            onChange={(e) => setStyle({ directionHint: e.currentTarget.checked })}
          />
          {reduced && (
            <Text size="xs" c="dimmed">
              Off because your system asks for reduced motion.
            </Text>
          )}
        </Section>

        <Section title="Relationship type colours">
          <Text size="xs" c="dimmed">
            Changes apply to this browser and are saved with map views.
            {canEditTypes ? ' Admins can make a colour the default for everyone.' : ''}
          </Text>
          {shownTypes.map((name) => {
            const t = typeTitle.get(name)
            const current = style.typeColors[name] ?? t?.color ?? '#868e96'
            const custom = name in style.typeColors
            return (
              <Group key={name} gap={6} wrap="nowrap" align="flex-end">
                <ColorInput
                  label={t?.title ?? name}
                  value={current}
                  onChange={(c) => setStyle({ typeColors: { ...style.typeColors, [name]: c } })}
                  format="hex"
                  size="xs"
                  style={{ flex: 1 }}
                  withEyeDropper={false}
                />
                {custom && (
                  <Button
                    size="xs"
                    variant="subtle"
                    onClick={() => {
                      setStyle({
                        typeColors: Object.fromEntries(Object.entries(style.typeColors).filter(([k]) => k !== name)),
                      })
                    }}
                  >
                    Reset
                  </Button>
                )}
                {custom && canEditTypes && t && (
                  <Button
                    size="xs"
                    variant="default"
                    loading={saveTypeColor.isPending}
                    onClick={() => saveTypeColor.mutate({ t, color: current })}
                  >
                    Make default
                  </Button>
                )}
              </Group>
            )
          })}
        </Section>

        <Section title="Nodes">
          <SegmentedControl
            value={style.nodeMode}
            onChange={(v) => setStyle({ nodeMode: v as MapStyle['nodeMode'] })}
            data={[
              { value: 'detailed', label: 'Detailed cards' },
              { value: 'compact', label: 'Compact' },
            ]}
            fullWidth
          />
          <Text size="sm">Icon size</Text>
          <Slider
            min={12}
            max={28}
            step={2}
            value={style.iconSize}
            onChange={(v) => setStyle({ iconSize: v })}
            label={(v) => `${v}px`}
            aria-label="Icon size"
          />
          <Switch
            label="Minimap"
            checked={style.minimap}
            onChange={(e) => setStyle({ minimap: e.currentTarget.checked })}
          />
        </Section>
      </Stack>
    </Drawer>
  )
}
