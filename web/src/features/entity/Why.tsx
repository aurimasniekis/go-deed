import { AnchorLink } from '@/components/links'
import { ActionIcon, Group, Popover, Stack, Table, Text } from '@mantine/core'
import { IconHelpHexagon } from '@tabler/icons-react'
import { createContext, useContext } from 'react'
import { TimeAgo } from '@/components/common'
import { LayerBadge } from '@/components/StatusBadge'
import { jsonValue, truncate } from '@/lib/format'
import { pointerLabel, provenanceFor, sameValue, type ProvenanceEntry } from '@/lib/provenance'

export const ProvenanceContext = createContext<{
  entityId: string
  prov?: Record<string, ProvenanceEntry>
  effective: unknown
}>({
  entityId: '',
  effective: undefined,
})

/**
 * The "why" affordance: answers "why does this value exist?" for one field — the winning source, its layer, revision
 * and time, and the candidates that lost — with a link to the full provenance table.
 */
export function Why({ pointer, value }: { pointer: string; value?: unknown }) {
  const { entityId, prov } = useContext(ProvenanceContext)
  const hit = provenanceFor(prov, pointer)
  if (!prov) return null
  return (
    <Popover position="left-start" width={360} withinPortal shadow="md">
      <Popover.Target>
        <ActionIcon
          size="sm"
          aria-label={`Why does ${pointerLabel(pointer)} have this value?`}
          data-testid={`why-${pointer}`}
        >
          <IconHelpHexagon size={14} stroke={1.6} />
        </ActionIcon>
      </Popover.Target>
      <Popover.Dropdown>
        {!hit ? (
          <Text size="sm">No source contributes {pointerLabel(pointer)}; it is derived or empty.</Text>
        ) : (
          <Stack gap={6}>
            <Text size="xs" c="dimmed">
              Why does <span className="mono">{pointerLabel(hit.pointer)}</span> have this value?
            </Text>
            <Group gap={6}>
              <Text size="sm" fw={600}>
                {hit.entry.source}
              </Text>
              <LayerBadge layer={hit.entry.layer} size="xs" />
            </Group>
            <Text size="xs" c="dimmed">
              Revision {hit.entry.revision} · reported <TimeAgo value={hit.entry.observed_at} />
            </Text>
            {value !== undefined && (
              <Text size="xs">
                Value: <span className="mono">{truncate(jsonValue(value), 120)}</span>
              </Text>
            )}
            {(hit.entry.candidates?.length ?? 0) > 0 && (
              <>
                <Text size="xs" fw={600} mt={4}>
                  Other candidates (lower precedence)
                </Text>
                <Table fz="xs" verticalSpacing={2} withRowBorders={false}>
                  <Table.Tbody>
                    {hit.entry.candidates?.map((c) => (
                      <Table.Tr key={c.source_id}>
                        <Table.Td>{c.source}</Table.Td>
                        <Table.Td>
                          <LayerBadge layer={c.layer} size="xs" />
                        </Table.Td>
                        <Table.Td className="mono" c={sameValue(c.value, value) ? 'dimmed' : undefined}>
                          {truncate(jsonValue(c.value), 40)}
                          {value !== undefined && !sameValue(c.value, value) && ' (lost)'}
                        </Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              </>
            )}
            <AnchorLink
              to="/entities/$id"
              params={{ id: entityId }}
              search={{ tab: 'provenance', ptr: hit.pointer }}
              size="xs"
            >
              Open full provenance
            </AnchorLink>
          </Stack>
        )}
      </Popover.Dropdown>
    </Popover>
  )
}
