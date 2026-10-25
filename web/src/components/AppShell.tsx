import {
  ActionIcon,
  AppShell,
  Avatar,
  Badge,
  Box,
  Burger,
  Group,
  Menu,
  NavLink,
  ScrollArea,
  Stack,
  Text,
  Tooltip,
  UnstyledButton,
  useMantineColorScheme,
} from '@mantine/core'
import { useDisclosure, useHotkeys } from '@mantine/hooks'
import { spotlight } from '@mantine/spotlight'
import {
  IconAlertTriangle,
  IconBell,
  IconBinaryTree2,
  IconCategory,
  IconChartDots3,
  IconHistory,
  IconInfoCircle,
  IconKeyboard,
  IconLayoutDashboard,
  IconLogout,
  IconMoon,
  IconPlugConnected,
  IconSearch,
  IconSettings,
  IconShieldLock,
  IconSun,
  IconTable,
  IconTopologyStar3,
  IconUser,
  IconUsers,
  IconFolders,
  IconArrowsExchange,
  IconArchive,
  IconActivityHeartbeat,
  IconListDetails,
} from '@tabler/icons-react'
import { useQueryClient } from '@tanstack/react-query'
import { Link, Outlet, useNavigate, useRouterState } from '@tanstack/react-router'
import { useEffect, useRef, type ReactNode } from 'react'
import { canReadAllAudit, isInstallationAdmin, meKey, useMe } from '@/api/auth'
import { api } from '@/api/client'
import { setCsrfToken, setUnauthorizedHandler, loginUrl } from '@/api/session'
import { useLiveConnection } from '@/api/useLive'
import { ReauthModal } from '@/features/auth/ReauthModal'
import { setShortcutsOpen, useLoadUserPrefs } from '@/lib/appStore'
import { BrandMark } from './Brand'
import { GlobalSearch } from './GlobalSearch'
import { LiveIndicator } from './LiveIndicator'
import { ProjectSwitcher } from './ProjectSwitcher'
import { ShortcutsModal } from './ShortcutsModal'
import { SyntheticBanner } from './SyntheticBanner'

interface NavItem {
  to: string
  label: string
  icon: ReactNode
  show?: boolean
}

function NavSection({ title, items, onNavigate }: { title?: string; items: NavItem[]; onNavigate: () => void }) {
  const path = useRouterState({ select: (s) => s.location.pathname })
  const visible = items.filter((i) => i.show !== false)
  if (visible.length === 0) return null
  return (
    <Stack gap={2}>
      {title && (
        <Text size="xs" fw={600} c="dimmed" tt="uppercase" px="sm" pt="sm" pb={2} style={{ letterSpacing: '0.04em' }}>
          {title}
        </Text>
      )}
      {visible.map((item) => {
        const active = item.to === '/' ? path === '/' : path === item.to || path.startsWith(`${item.to}/`)
        return (
          <NavLink
            key={item.to}
            component={Link}
            to={item.to}
            label={item.label}
            leftSection={item.icon}
            active={active}
            aria-current={active ? 'page' : undefined}
            onClick={onNavigate}
            styles={{ label: { fontSize: 13 } }}
          />
        )
      })}
    </Stack>
  )
}

function UserMenu() {
  const me = useMe().data
  const qc = useQueryClient()
  const navigate = useNavigate()
  if (!me) return null
  const initials = me.display_name
    .split(/\s+/)
    .map((p) => p.charAt(0))
    .join('')
    .slice(0, 2)
    .toUpperCase()
  return (
    <Menu position="bottom-end" width={240} withinPortal>
      <Menu.Target>
        <UnstyledButton
          aria-label={`${initials || 'Account'}: account menu for ${me.display_name}`}
          data-testid="user-menu"
        >
          <Avatar size={28} radius="xl" color="deed">
            {initials || <IconUser size={14} />}
          </Avatar>
        </UnstyledButton>
      </Menu.Target>
      <Menu.Dropdown>
        <Menu.Label>
          <Text size="sm" fw={600} c="var(--mantine-color-text)">
            {me.display_name}
          </Text>
          <Text size="xs" c="dimmed">
            {me.email}
          </Text>
          {me.installation_role && (
            <Badge size="xs" mt={4} variant="outline">
              {me.installation_role}
            </Badge>
          )}
        </Menu.Label>
        <Menu.Divider />
        <Menu.Item component={Link} to="/settings/account" leftSection={<IconUser size={14} />}>
          Account & security
        </Menu.Item>
        <Menu.Item leftSection={<IconKeyboard size={14} />} onClick={() => setShortcutsOpen(true)}>
          Keyboard shortcuts
        </Menu.Item>
        <Menu.Divider />
        <Menu.Item
          leftSection={<IconLogout size={14} />}
          onClick={() => {
            void api
              .POST('/auth/logout')
              .catch(() => undefined)
              .finally(() => {
                setCsrfToken('')
                qc.clear()
                qc.setQueryData(meKey, null)
                void navigate({ to: '/login', replace: true })
              })
          }}
        >
          Sign out
        </Menu.Item>
      </Menu.Dropdown>
    </Menu>
  )
}

function ThemeToggle() {
  const { toggleColorScheme } = useMantineColorScheme()
  return (
    <Tooltip label="Toggle light/dark theme">
      <ActionIcon onClick={toggleColorScheme} aria-label="Toggle light/dark theme" data-testid="theme-toggle">
        <IconSun size={17} className="theme-light-only" />
        <IconMoon size={17} className="theme-dark-only" />
      </ActionIcon>
    </Tooltip>
  )
}

/** "g then d" style navigation shortcuts. */
function useGoShortcuts() {
  const navigate = useNavigate()
  const pendingG = useRef(0)
  useEffect(() => {
    const targets: Record<string, string> = {
      d: '/',
      i: '/inventory',
      m: '/map',
      r: '/relations',
      f: '/findings',
      a: '/alerts',
      s: '/sources',
      k: '/kinds',
      p: '/settings/projects',
    }
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null
      if (t && (t.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(t.tagName))) return
      if (e.metaKey || e.ctrlKey || e.altKey) return
      if (e.key === 'g') {
        pendingG.current = Date.now()
        return
      }
      if (pendingG.current && Date.now() - pendingG.current < 1200) {
        const to = targets[e.key]
        pendingG.current = 0
        if (to) {
          e.preventDefault()
          void navigate({ to })
        }
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [navigate])
}

export function AppShellLayout() {
  const me = useMe().data
  const [opened, { toggle, close }] = useDisclosure()
  const navigate = useNavigate()
  const admin = isInstallationAdmin(me)
  useLoadUserPrefs(me?.user_id)
  useLiveConnection(!!me && !me.mfa_pending && !me.must_enroll)
  useGoShortcuts()

  useEffect(
    () =>
      setUnauthorizedHandler(() => {
        const here = `${window.location.pathname}${window.location.search}`
        void navigate({ href: loginUrl(here), replace: true })
      }),
    [navigate],
  )

  useHotkeys([
    ['mod+K', () => spotlight.open()],
    ['/', () => spotlight.open()],
    ['shift+?', () => setShortcutsOpen(true)],
  ])

  const explore: NavItem[] = [
    { to: '/', label: 'Dashboard', icon: <IconLayoutDashboard size={17} stroke={1.6} /> },
    { to: '/inventory', label: 'Inventory', icon: <IconTable size={17} stroke={1.6} /> },
    { to: '/map', label: 'Map', icon: <IconTopologyStar3 size={17} stroke={1.6} /> },
    { to: '/relations', label: 'Relationships', icon: <IconListDetails size={17} stroke={1.6} /> },
  ]
  const operate: NavItem[] = [
    { to: '/findings', label: 'Findings', icon: <IconAlertTriangle size={17} stroke={1.6} /> },
    { to: '/alerts', label: 'Alerts', icon: <IconBell size={17} stroke={1.6} /> },
    { to: '/sources', label: 'Sources', icon: <IconPlugConnected size={17} stroke={1.6} /> },
  ]
  const model: NavItem[] = [
    { to: '/kinds', label: 'Kinds', icon: <IconCategory size={17} stroke={1.6} /> },
    { to: '/relationship-types', label: 'Relationship types', icon: <IconBinaryTree2 size={17} stroke={1.6} /> },
  ]
  const settings: NavItem[] = [
    { to: '/settings/projects', label: 'Projects', icon: <IconFolders size={17} stroke={1.6} /> },
    { to: '/settings/bridges', label: 'Bridges', icon: <IconArrowsExchange size={17} stroke={1.6} /> },
    { to: '/settings/users', label: 'Users', icon: <IconUsers size={17} stroke={1.6} />, show: admin },
    { to: '/settings/account', label: 'Account', icon: <IconShieldLock size={17} stroke={1.6} /> },
    { to: '/settings/telemetry', label: 'Telemetry', icon: <IconActivityHeartbeat size={17} stroke={1.6} /> },
    { to: '/settings/audit', label: 'Audit log', icon: <IconHistory size={17} stroke={1.6} /> },
    {
      to: '/settings/backups',
      label: 'Backups',
      icon: <IconArchive size={17} stroke={1.6} />,
      show: canReadAllAudit(me),
    },
    { to: '/settings/about', label: 'About', icon: <IconInfoCircle size={17} stroke={1.6} /> },
  ]

  return (
    <AppShell
      header={{ height: 52 }}
      navbar={{ width: 224, breakpoint: 'sm', collapsed: { mobile: !opened } }}
      padding={0}
    >
      <a href="#main" className="skip-link">
        Skip to content
      </a>
      <AppShell.Header px="sm" style={{ borderBottomColor: 'var(--deed-border)' }}>
        <Group h="100%" justify="space-between" wrap="nowrap" gap="sm">
          <Group gap="sm" wrap="nowrap">
            <Burger opened={opened} onClick={toggle} hiddenFrom="sm" size="sm" aria-label="Toggle navigation" />
            <UnstyledButton component={Link} to="/" aria-label="Deed home">
              <Group gap={8} wrap="nowrap">
                <BrandMark size={24} />
                <Text fw={700} size="lg" style={{ letterSpacing: '-0.01em' }}>
                  Deed
                </Text>
              </Group>
            </UnstyledButton>
            <Box visibleFrom="md" ml="sm">
              <ProjectSwitcher />
            </Box>
          </Group>
          <Group gap={6} wrap="nowrap">
            <UnstyledButton
              onClick={() => spotlight.open()}
              title="Search (press /)"
              data-testid="search-button"
              px="sm"
              py={5}
              style={{
                border: '1px solid var(--deed-border)',
                borderRadius: 4,
                background: 'var(--deed-surface-2)',
              }}
            >
              <Group gap={6} wrap="nowrap">
                <IconSearch size={14} aria-hidden />
                <Text size="sm" c="dimmed" visibleFrom="sm">
                  Search entities…
                </Text>
                <Text size="xs" c="dimmed" visibleFrom="sm" className="mono">
                  /
                </Text>
              </Group>
            </UnstyledButton>
            <LiveIndicator />
            <Tooltip label="Keyboard shortcuts (?)">
              <ActionIcon onClick={() => setShortcutsOpen(true)} aria-label="Keyboard shortcuts">
                <IconKeyboard size={17} />
              </ActionIcon>
            </Tooltip>
            <ThemeToggle />
            <UserMenu />
          </Group>
        </Group>
      </AppShell.Header>
      <AppShell.Navbar style={{ borderRightColor: 'var(--deed-border)' }} aria-label="Main navigation">
        <ScrollArea type="auto" px="xs" py="xs">
          <Box hiddenFrom="md" px="xs" pb="xs">
            <ProjectSwitcher />
          </Box>
          <NavSection items={explore} onNavigate={close} />
          <NavSection title="Operate" items={operate} onNavigate={close} />
          <NavSection title="Model" items={model} onNavigate={close} />
          <NavSection title="Settings" items={settings} onNavigate={close} />
          <Group gap={6} px="sm" pt="md" pb="xs">
            <IconChartDots3 size={14} aria-hidden color="var(--mantine-color-dimmed)" />
            <Text size="xs" c="dimmed">
              Press <span className="mono">?</span> for shortcuts
            </Text>
          </Group>
          <Group gap={6} px="sm">
            <IconSettings size={14} aria-hidden color="var(--mantine-color-dimmed)" />
            <Text size="xs" c="dimmed">
              {me?.installation_role ? `Installation ${me.installation_role}` : 'Project member'}
            </Text>
          </Group>
        </ScrollArea>
      </AppShell.Navbar>
      <AppShell.Main id="main" tabIndex={-1} style={{ outline: 'none' }}>
        <SyntheticBanner />
        <Outlet />
      </AppShell.Main>
      <GlobalSearch />
      <ShortcutsModal />
      <ReauthModal />
    </AppShell>
  )
}

/** Standard page padding and width for non-map pages. */
export function Page({ children, wide = false }: { children: ReactNode; wide?: boolean }) {
  return (
    <Box px={{ base: 'md', md: 'xl' }} py="lg" maw={wide ? undefined : 1440} mx="auto">
      {children}
    </Box>
  )
}
