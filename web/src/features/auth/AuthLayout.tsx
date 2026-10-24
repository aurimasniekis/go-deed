import { ActionIcon, Box, Card, Center, Group, Stack, Text, Title, Tooltip, useMantineColorScheme } from '@mantine/core'
import { IconMoon, IconSun } from '@tabler/icons-react'
import type { ReactNode } from 'react'
import { BrandMark } from '@/components/Brand'

/** The frame of the sign-in pages: no navigation, no API calls beyond the page's own. */
export function AuthLayout({
  title,
  subtitle,
  children,
  width = 420,
}: {
  title: string
  subtitle?: ReactNode
  children: ReactNode
  width?: number
}) {
  const { toggleColorScheme } = useMantineColorScheme()
  return (
    <Center mih="100vh" p="md" component="main">
      <Stack w="100%" maw={width} gap="md">
        <Group gap="xs" justify="center">
          <BrandMark size={28} />
          <Text fw={700} size="xl" style={{ letterSpacing: '-0.01em' }}>
            Deed
          </Text>
        </Group>
        <Card padding="lg" shadow="xs">
          <Stack gap="sm">
            <div>
              <Title order={1} fz={18}>
                {title}
              </Title>
              {subtitle && (
                <Text size="sm" c="dimmed" mt={4}>
                  {subtitle}
                </Text>
              )}
            </div>
            {children}
          </Stack>
        </Card>
        <Group justify="space-between">
          <Text size="xs" c="dimmed">
            Declarative infrastructure catalog and live relationship map
          </Text>
          <Box>
            <Tooltip label="Toggle light/dark theme">
              <ActionIcon onClick={toggleColorScheme} aria-label="Toggle light/dark theme">
                <IconSun size={16} className="theme-light-only" />
                <IconMoon size={16} className="theme-dark-only" />
              </ActionIcon>
            </Tooltip>
          </Box>
        </Group>
      </Stack>
    </Center>
  )
}
