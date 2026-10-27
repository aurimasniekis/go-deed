import { Group, Modal, SimpleGrid, Stack, Text, Title } from '@mantine/core'
import { useSelector } from '@tanstack/react-store'
import { appStore, setShortcutsOpen } from '@/lib/appStore'
import { Kbd } from './common'

export const SHORTCUTS: { group: string; items: [string[], string][] }[] = [
  {
    group: 'Anywhere',
    items: [
      [['/'], 'Search entities and pages'],
      [['⌘/Ctrl', 'K'], 'Search entities and pages'],
      [['?'], 'Show this help'],
      [['g', 'd'], 'Go to dashboard'],
      [['g', 'i'], 'Go to inventory'],
      [['g', 'm'], 'Go to map'],
      [['g', 'r'], 'Go to relationships'],
      [['g', 'f'], 'Go to findings'],
      [['g', 'a'], 'Go to alerts'],
      [['g', 's'], 'Go to sources'],
      [['g', 'k'], 'Go to kinds'],
      [['g', 'p'], 'Go to projects'],
    ],
  },
  {
    group: 'Map',
    items: [
      [['←', '↑', '→', '↓'], 'Move selection to the nearest neighbour in that direction'],
      [['Enter'], 'Expand the selected node or cluster'],
      [['Shift', 'F10'], 'Quick actions for the selected node (also right-click)'],
      [['Esc'], 'Clear selection, close panels'],
      [['f'], 'Fit the view'],
      [['i'], 'Impact (downstream) of the selected entity'],
      [['l'], 'Toggle the legend'],
      [['v'], 'Show / hide kinds and relationship types'],
      [['m'], 'Mask mode: hide names for screenshots'],
      [['+', '−'], 'Zoom in / out'],
    ],
  },
  {
    group: 'Inventory',
    items: [
      [['/'], 'Focus the search box (when the page has one)'],
      [['Space'], 'Toggle the focused row’s checkbox'],
    ],
  },
]

export function ShortcutsModal() {
  const open = useSelector(appStore, (s) => s.shortcutsOpen)
  return (
    <Modal opened={open} onClose={() => setShortcutsOpen(false)} title="Keyboard shortcuts" size="xl" centered>
      <SimpleGrid cols={{ base: 1, sm: 2 }} spacing="lg">
        {SHORTCUTS.map((g) => (
          <Stack key={g.group} gap={6}>
            <Title order={3}>{g.group}</Title>
            {g.items.map(([keys, label]) => (
              <Group key={label + keys.join()} justify="space-between" wrap="nowrap" gap="sm">
                <Text size="sm">{label}</Text>
                <Group gap={4} wrap="nowrap">
                  {keys.map((k) => (
                    <Kbd key={k}>{k}</Kbd>
                  ))}
                </Group>
              </Group>
            ))}
          </Stack>
        ))}
      </SimpleGrid>
    </Modal>
  )
}
