import { Group, Text } from '@mantine/core'
import { useEffect } from 'react'
import { IconFlask } from '@tabler/icons-react'
import { useSelector } from '@tanstack/react-store'
import { useProjects } from '@/api/queries'
import { appStore, useMasked } from '@/lib/appStore'

/**
 * Persistent "Synthetic demo data" banner (DISCOVERY Q-58): shown whenever a readable project is marked synthetic or the
 * dashboard reports synthetic_data, so generated sample data is never mistaken for real infrastructure.
 */
export function SyntheticBanner() {
  const projects = useProjects()
  // The dashboard records the server's own synthetic_data flag here when it loads.
  const flagged = useSelector(appStore, (s) => s.syntheticData)
  const masked = useMasked()
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
      className="deed-banner"
    >
      <IconFlask size={16} aria-hidden color="var(--deed-banner-icon)" />
      <Text size="sm" fw={600} c="var(--deed-banner-fg)">
        Synthetic demo data
      </Text>
      <Text size="sm" c="var(--deed-banner-fg)" truncate>
        {synthetic.length > 0 && !masked
          ? `${synthetic.map((p) => p.display_name || p.name).join(', ')} ${synthetic.length === 1 ? 'contains' : 'contain'} generated sample data, not real infrastructure.`
          : 'Some data on this installation is generated sample data, not real infrastructure.'}
      </Text>
    </Group>
  )
}
