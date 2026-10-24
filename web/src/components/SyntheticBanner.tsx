import { Group, Text } from '@mantine/core'
import { useEffect } from 'react'
import { IconFlask } from '@tabler/icons-react'
import { useSelector } from '@tanstack/react-store'
import { useProjects } from '@/api/queries'
import { appStore } from '@/lib/appStore'

/**
 * Persistent "Synthetic demo data" banner (DISCOVERY Q-58): shown whenever a readable project is marked synthetic or the
 * dashboard reports synthetic_data, so generated sample data is never mistaken for real infrastructure.
 */
export function SyntheticBanner() {
  const projects = useProjects()
  // The dashboard records the server's own synthetic_data flag here when it loads.
  const flagged = useSelector(appStore, (s) => s.syntheticData)
  const synthetic = (projects.data ?? []).filter((p) => p.synthetic)
  const shown = synthetic.length > 0 || flagged
  // Full-height views (the map) subtract the banner's height.
  useEffect(() => {
    document.documentElement.style.setProperty('--deed-banner-h', shown ? '33px' : '0px')
  }, [shown])
  if (!shown) return null
  return (
    <Group
      gap="xs"
      px="md"
      py={6}
      h={33}
      wrap="nowrap"
      role="status"
      data-testid="synthetic-banner"
      style={{
        background: 'light-dark(var(--mantine-color-yellow-0), rgba(250, 176, 5, 0.12))',
        borderBottom: '1px solid light-dark(var(--mantine-color-yellow-3), rgba(250, 176, 5, 0.35))',
      }}
    >
      <IconFlask size={16} aria-hidden color="light-dark(#8a4b00, #ffe066)" />
      <Text size="sm" fw={600} c="light-dark(#6b3a00, #ffe066)">
        Synthetic demo data
      </Text>
      <Text size="sm" c="light-dark(#6b3a00, #fff3bf)" truncate>
        {synthetic.length > 0
          ? `${synthetic.map((p) => p.display_name || p.name).join(', ')} ${synthetic.length === 1 ? 'contains' : 'contain'} generated sample data, not real infrastructure.`
          : 'Some data on this installation is generated sample data, not real infrastructure.'}
      </Text>
    </Group>
  )
}
