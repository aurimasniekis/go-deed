import { ActionIcon, Button, Group, NumberInput, Stack, Text } from '@mantine/core'
import { IconArrowsExchange, IconX } from '@tabler/icons-react'
import { useState } from 'react'
import { EntityPicker } from '@/components/EntityPicker'
import type { MapSearch } from '@/lib/urlState'

/** Path finder (GET /graph/path): the shortest relationship path between two entities. Remounted (keyed) when the URL's endpoints change. */
export function PathFinder({
  search,
  onFind,
  onClose,
}: {
  search: MapSearch
  onFind: (from: string, to: string, depth: number, directed: boolean) => void
  onClose: () => void
}) {
  const [from, setFrom] = useState<string | null>(search.from ?? null)
  const [to, setTo] = useState<string | null>(search.to ?? null)
  const [depth, setDepth] = useState<number>(search.depth ?? 6)
  return (
    <div className="deed-panel" style={{ width: 320 }} data-testid="path-finder" role="region" aria-label="Path finder">
      <Group justify="space-between" px="sm" pt={6}>
        <Text size="sm" fw={600}>
          Find a path
        </Text>
        <ActionIcon size="sm" onClick={onClose} aria-label="Close path finder">
          <IconX size={12} />
        </ActionIcon>
      </Group>
      <Stack gap="xs" p="sm">
        <EntityPicker
          label="From"
          value={from}
          onChange={(v) => setFrom(v)}
          initialLabel={from ?? undefined}
          comboboxProps={{ withinPortal: true }}
          data-testid="path-from"
        />
        <Group justify="center">
          <ActionIcon
            variant="default"
            aria-label="Swap endpoints"
            onClick={() => {
              setFrom(to)
              setTo(from)
            }}
          >
            <IconArrowsExchange size={14} />
          </ActionIcon>
        </Group>
        <EntityPicker
          label="To"
          value={to}
          onChange={(v) => setTo(v)}
          initialLabel={to ?? undefined}
          comboboxProps={{ withinPortal: true }}
          data-testid="path-to"
        />
        <NumberInput
          label="Maximum depth"
          min={1}
          max={10}
          value={depth}
          onChange={(v) => setDepth(typeof v === 'number' ? v : 6)}
        />
        <Button
          disabled={!from || !to || from === to}
          onClick={() => from && to && onFind(from, to, depth, false)}
          data-testid="path-find"
        >
          Find path
        </Button>
        <Text size="xs" c="dimmed">
          Follows relationships in either direction; type filters from the options drawer apply.
        </Text>
      </Stack>
    </div>
  )
}
