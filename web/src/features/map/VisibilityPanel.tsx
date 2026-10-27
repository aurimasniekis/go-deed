import { ActionIcon, Button, Checkbox, Group, ScrollArea, Select, Stack, Text, TextInput, Tooltip } from '@mantine/core'
import { IconSearch, IconX } from '@tabler/icons-react'
import { useMemo, useState } from 'react'
import { RefIcon } from '@/components/icons'
import type { GraphModel } from './types'

export interface VisibilityItem {
  key: string
  label: string
  count: number
  hidden: boolean
  icon?: string
  color?: string
}

/** Kinds and relationship types on the loaded map with their counts: what is there to hide and what is hidden. */
export function visibilityItems(
  model: GraphModel,
  hideKind: string[],
  hideType: string[],
  kindInfo: (kind: string) => { title: string; icon?: string },
  typeInfo: (type: string) => { title: string; color?: string },
): { kinds: VisibilityItem[]; types: VisibilityItem[] } {
  const kinds = new Map<string, number>()
  for (const n of model.nodes.values()) {
    if (n.cluster) {
      if (n.cluster.groupBy === 'kind') kinds.set(n.cluster.value, (kinds.get(n.cluster.value) ?? 0) + n.cluster.count)
    } else if (!n.redacted) {
      kinds.set(n.kind, (kinds.get(n.kind) ?? 0) + 1)
    }
  }
  const types = new Map<string, number>()
  for (const e of model.edges.values()) types.set(e.type, (types.get(e.type) ?? 0) + e.count)
  // Hidden entries stay listed (count 0 when nothing of them is loaded) so they can be switched back on.
  for (const k of hideKind) if (!kinds.has(k)) kinds.set(k, 0)
  for (const t of hideType) if (!types.has(t)) types.set(t, 0)
  const byLabel = (a: VisibilityItem, b: VisibilityItem) => a.label.localeCompare(b.label)
  return {
    kinds: [...kinds]
      .map(([key, count]) => {
        const k = kindInfo(key)
        return { key, count, label: k.title, hidden: hideKind.includes(key), ...(k.icon ? { icon: k.icon } : {}) }
      })
      .sort(byLabel),
    types: [...types]
      .map(([key, count]) => {
        const t = typeInfo(key)
        return { key, count, label: t.title, hidden: hideType.includes(key), ...(t.color ? { color: t.color } : {}) }
      })
      .sort(byLabel),
  }
}

/**
 * The values of one label key on the loaded map with their counts (entities, or the members of overview groups by that
 * label), keyed `key:value` like the hideLabel filter. Entities without the key are not listed: hiding never touches
 * them.
 */
export function labelItems(model: GraphModel, key: string, hideLabel: string[]): VisibilityItem[] {
  const values = new Map<string, number>()
  for (const n of model.nodes.values()) {
    if (n.cluster) {
      if (n.cluster.groupBy === `label:${key}` && n.cluster.value !== '')
        values.set(n.cluster.value, (values.get(n.cluster.value) ?? 0) + n.cluster.count)
    } else if (!n.redacted) {
      const v = n.labels?.[key]
      if (v !== undefined) values.set(v, (values.get(v) ?? 0) + 1)
    }
  }
  const prefix = `${key}:`
  for (const kv of hideLabel) {
    const v = kv.startsWith(prefix) ? kv.slice(prefix.length) : undefined
    if (v !== undefined && !values.has(v)) values.set(v, 0)
  }
  return [...values]
    .map(([v, count]) => ({ key: prefix + v, label: v, count, hidden: hideLabel.includes(prefix + v) }))
    .sort((a, b) => a.label.localeCompare(b.label))
}

function Section({
  title,
  items,
  filter,
  onToggle,
  onOnly,
  onShowAll,
  testid,
}: {
  title: string
  items: VisibilityItem[]
  filter: string
  onToggle: (key: string) => void
  onOnly: (key: string) => void
  onShowAll: () => void
  testid: string
}) {
  const shown = items.filter((i) => !filter || i.label.toLowerCase().includes(filter) || i.key.includes(filter))
  const hidden = items.filter((i) => i.hidden).length
  if (items.length === 0) return null
  return (
    <Stack gap={2} data-testid={testid}>
      <Group justify="space-between" wrap="nowrap">
        <Text size="xs" fw={600}>
          {title}
          {hidden > 0 && (
            <Text span size="xs" c="dimmed" fw={400}>
              {' '}
              · {hidden} hidden
            </Text>
          )}
        </Text>
        {hidden > 0 && (
          <Button size="compact-xs" variant="subtle" onClick={onShowAll}>
            Show all
          </Button>
        )}
      </Group>
      {shown.map((i) => (
        <Group key={i.key} gap={6} wrap="nowrap" className="deed-visibility-row" data-hidden={i.hidden || undefined}>
          <Checkbox
            size="xs"
            checked={!i.hidden}
            onChange={() => {
              onToggle(i.key)
            }}
            aria-label={`${i.hidden ? 'Show' : 'Hide'} ${i.label}`}
            data-key={i.key}
          />
          {i.color !== undefined && (
            <span style={{ width: 14, height: 4, borderRadius: 2, background: i.color, flexShrink: 0 }} aria-hidden />
          )}
          {i.icon !== undefined && <RefIcon iconRef={i.icon} size={14} />}
          <Text size="xs" style={{ flex: 1, minWidth: 0 }} truncate title={i.label}>
            {i.label}
          </Text>
          <Text size="xs" c="dimmed" style={{ fontVariantNumeric: 'tabular-nums' }}>
            {i.count}
          </Text>
          <Button
            size="compact-xs"
            variant="subtle"
            className="deed-visibility-only"
            onClick={() => {
              onOnly(i.key)
            }}
            aria-label={`Show only ${i.label}`}
          >
            only
          </Button>
        </Group>
      ))}
    </Stack>
  )
}

/**
 * Show or hide kinds, label values and relationship types on the map (the hidden ones stay listed). Hiding is a filter
 * like the others: it goes to the URL and is saved with views; the focus entity always stays.
 */
export function VisibilityPanel({
  kinds,
  types,
  labelKeys,
  labelKey,
  onLabelKey,
  labels,
  hideLabel,
  onChange,
  onClose,
}: {
  kinds: VisibilityItem[]
  types: VisibilityItem[]
  /** Label keys to choose from, and the chosen one with its values. */
  labelKeys: string[]
  labelKey: string | null
  onLabelKey: (key: string) => void
  labels: VisibilityItem[]
  /** Every hidden label value (all keys), kept when the values of the chosen key change. */
  hideLabel: string[]
  onChange: (hide: { hideKind?: string[]; hideType?: string[]; hideLabel?: string[] }) => void
  onClose: () => void
}) {
  const otherKeys = hideLabel.filter((kv) => !labels.some((i) => i.key === kv))
  const [q, setQ] = useState('')
  const filter = q.trim().toLowerCase()
  const hiddenOf = (items: VisibilityItem[]) => items.filter((i) => i.hidden).map((i) => i.key)
  const toggle = (items: VisibilityItem[], key: string) => {
    const h = hiddenOf(items)
    return h.includes(key) ? h.filter((k) => k !== key) : [...h, key]
  }
  const only = (items: VisibilityItem[], key: string) => items.map((i) => i.key).filter((k) => k !== key)
  const many = useMemo(() => kinds.length + types.length > 12, [kinds.length, types.length])
  return (
    <div
      className="deed-panel"
      style={{ width: 260 }}
      data-testid="map-visibility"
      role="region"
      aria-label="Show and hide kinds, labels and relationships"
    >
      <Group justify="space-between" px="sm" pt={6}>
        <Text size="xs" fw={700} tt="uppercase" c="dimmed" style={{ letterSpacing: '0.04em' }}>
          Show / hide
        </Text>
        <Tooltip label="Close (v)">
          <ActionIcon size="sm" onClick={onClose} aria-label="Close show / hide">
            <IconX size={12} />
          </ActionIcon>
        </Tooltip>
      </Group>
      {many && (
        <TextInput
          size="xs"
          mx="sm"
          mt={4}
          placeholder="Filter"
          leftSection={<IconSearch size={12} />}
          value={q}
          onChange={(e) => {
            setQ(e.currentTarget.value)
          }}
          aria-label="Filter kinds, labels and relationship types"
        />
      )}
      <ScrollArea.Autosize mah={320} type="auto" px="sm" pb="sm" pt={6} viewportProps={{ tabIndex: 0 }}>
        <Stack gap={10}>
          <Section
            title="Kinds"
            items={kinds}
            filter={filter}
            testid="map-visibility-kinds"
            onToggle={(k) => {
              onChange({ hideKind: toggle(kinds, k) })
            }}
            onOnly={(k) => {
              onChange({ hideKind: only(kinds, k) })
            }}
            onShowAll={() => {
              onChange({ hideKind: [] })
            }}
          />
          {labelKeys.length > 0 && (
            <Stack gap={4}>
              <Select
                size="xs"
                label="Labels"
                data={labelKeys}
                value={labelKey}
                onChange={(k) => {
                  if (k) onLabelKey(k)
                }}
                allowDeselect={false}
                searchable
                aria-label="Label key"
                data-testid="map-visibility-label-key"
                comboboxProps={{ withinPortal: true }}
              />
              {labelKey && labels.length === 0 && (
                <Text size="xs" c="dimmed">
                  No entity on the map has the label {labelKey}.
                </Text>
              )}
              <Section
                title={labelKey ? `Values of ${labelKey}` : 'Values'}
                items={labels}
                filter={filter}
                testid="map-visibility-labels"
                onToggle={(kv) => {
                  onChange({ hideLabel: [...otherKeys, ...toggle(labels, kv)] })
                }}
                onOnly={(kv) => {
                  onChange({ hideLabel: [...otherKeys, ...only(labels, kv)] })
                }}
                onShowAll={() => {
                  onChange({ hideLabel: otherKeys })
                }}
              />
            </Stack>
          )}
          <Section
            title="Relationships"
            items={types}
            filter={filter}
            testid="map-visibility-types"
            onToggle={(t) => {
              onChange({ hideType: toggle(types, t) })
            }}
            onOnly={(t) => {
              onChange({ hideType: only(types, t) })
            }}
            onShowAll={() => {
              onChange({ hideType: [] })
            }}
          />
        </Stack>
      </ScrollArea.Autosize>
    </div>
  )
}
