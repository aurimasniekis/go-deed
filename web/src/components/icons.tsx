import {
  IconActivity,
  IconAlertOctagon,
  IconAlertTriangle,
  IconApps,
  IconArrowBigUpLines,
  IconArrowGuide,
  IconArrowsExchange,
  IconBell,
  IconBook,
  IconBox,
  IconBoxModel,
  IconBrandGit,
  IconBrandTerraform,
  IconBug,
  IconBulb,
  IconCertificate,
  IconChartLine,
  IconCircleCheck,
  IconCircleDot,
  IconCircleX,
  IconClockCheck,
  IconClockPause,
  IconClockQuestion,
  IconClockX,
  IconCloud,
  IconComponents,
  IconCube,
  IconDatabase,
  IconDatabaseExport,
  IconEye,
  IconFileCheck,
  IconFileCode,
  IconFileImport,
  IconFileText,
  IconGitBranch,
  IconGitPullRequest,
  IconHelpCircle,
  IconHexagon,
  IconHierarchy,
  IconIdBadge,
  IconInfoCircle,
  IconKey,
  IconLink,
  IconListCheck,
  IconLockAccess,
  IconMapPin,
  IconNetwork,
  IconNote,
  IconNotebook,
  IconPencil,
  IconPencilBolt,
  IconPlayerPlay,
  IconPlugConnected,
  IconPuzzle,
  IconRadar,
  IconRobot,
  IconRocket,
  IconRoute,
  IconServer,
  IconServer2,
  IconServerBolt,
  IconSettings,
  IconShieldCheck,
  IconStack2,
  IconTag,
  IconTerminal2,
  IconTopologyStar3,
  IconUsersGroup,
  IconWorld,
  IconWorldWww,
  IconMinus,
  type Icon,
  type IconProps,
} from '@tabler/icons-react'
import {
  siCloudflare,
  siDocker,
  siGitea,
  siGithub,
  siGitlab,
  siGrafana,
  siHetzner,
  siHomeassistant,
  siKubernetes,
  siNatsdotio,
  siPostgresql,
  siProxmox,
  siTerraform,
  siTraefikproxy,
  siUbiquiti,
} from 'simple-icons'
import { Store, useSelector } from '@tanstack/react-store'
import { createElement, type CSSProperties } from 'react'

/**
 * Icon references use the `pack:name` format (ADR-011b). Tabler icons render as components; Simple Icons (CC0) render
 * from their path data as monochrome SVGs tinted with currentColor so they follow the theme. Unknown references fall
 * back to a neutral cube.
 */
const tabler: Record<string, Icon> = {
  activity: IconActivity,
  'alert-octagon': IconAlertOctagon,
  'alert-triangle': IconAlertTriangle,
  apps: IconApps,
  'arrow-big-up-lines': IconArrowBigUpLines,
  'arrow-guide': IconArrowGuide,
  'arrows-exchange': IconArrowsExchange,
  bell: IconBell,
  book: IconBook,
  box: IconBox,
  'box-model': IconBoxModel,
  'brand-git': IconBrandGit,
  'brand-terraform': IconBrandTerraform,
  bug: IconBug,
  bulb: IconBulb,
  certificate: IconCertificate,
  'chart-line': IconChartLine,
  'circle-check': IconCircleCheck,
  'circle-dot': IconCircleDot,
  'circle-x': IconCircleX,
  'clock-check': IconClockCheck,
  'clock-pause': IconClockPause,
  'clock-question': IconClockQuestion,
  'clock-x': IconClockX,
  cloud: IconCloud,
  components: IconComponents,
  cube: IconCube,
  database: IconDatabase,
  'database-export': IconDatabaseExport,
  eye: IconEye,
  'file-check': IconFileCheck,
  'file-code': IconFileCode,
  'file-import': IconFileImport,
  'file-text': IconFileText,
  'git-branch': IconGitBranch,
  'git-pull-request': IconGitPullRequest,
  'help-circle': IconHelpCircle,
  minus: IconMinus,
  hexagon: IconHexagon,
  hierarchy: IconHierarchy,
  'id-badge': IconIdBadge,
  'info-circle': IconInfoCircle,
  key: IconKey,
  link: IconLink,
  'list-check': IconListCheck,
  'lock-access': IconLockAccess,
  'map-pin': IconMapPin,
  network: IconNetwork,
  note: IconNote,
  notebook: IconNotebook,
  pencil: IconPencil,
  'pencil-bolt': IconPencilBolt,
  'player-play': IconPlayerPlay,
  'plug-connected': IconPlugConnected,
  puzzle: IconPuzzle,
  radar: IconRadar,
  robot: IconRobot,
  rocket: IconRocket,
  route: IconRoute,
  server: IconServer,
  'server-2': IconServer2,
  'server-bolt': IconServerBolt,
  settings: IconSettings,
  'shield-check': IconShieldCheck,
  'stack-2': IconStack2,
  tag: IconTag,
  'terminal-2': IconTerminal2,
  'topology-star-3': IconTopologyStar3,
  'users-group': IconUsersGroup,
  world: IconWorld,
  'world-www': IconWorldWww,
}

interface SimpleIcon {
  title: string
  path: string
  hex: string
}

const simple: Record<string, SimpleIcon> = {
  cloudflare: siCloudflare,
  docker: siDocker,
  gitea: siGitea,
  github: siGithub,
  gitlab: siGitlab,
  grafana: siGrafana,
  hetzner: siHetzner,
  homeassistant: siHomeassistant,
  kubernetes: siKubernetes,
  natsdotio: siNatsdotio,
  postgresql: siPostgresql,
  proxmox: siProxmox,
  terraform: siTerraform,
  traefikproxy: siTraefikproxy,
  ubiquiti: siUbiquiti,
}

/** Uploaded icons (GET /icons), by name. Filled once per session by useCustomIconsLoader in the app shell. */
export const customIcons = new Store<Record<string, { url: string; monochrome: boolean }>>({})

export function customIconURL(name: string): string {
  return customIcons.state[name]?.url ?? `/api/v1/icons/${encodeURIComponent(name)}.svg`
}

export function tablerIcon(name: string | undefined): Icon {
  return tabler[name ?? ''] ?? IconCube
}

/** Parses `pack:name`; a bare name is a Tabler icon. */
export function parseIconRef(ref: string | undefined | null): { pack: string; name: string } {
  if (!ref) return { pack: 'tabler', name: 'cube' }
  const i = ref.indexOf(':')
  if (i < 0) return { pack: 'tabler', name: ref }
  return { pack: ref.slice(0, i), name: ref.slice(i + 1) }
}

export function brandColor(ref: string | undefined | null): string | undefined {
  const { pack, name } = parseIconRef(ref)
  return pack === 'simple-icons' ? `#${simple[name]?.hex ?? ''}` : undefined
}

export function RefIcon({
  iconRef,
  size = 16,
  stroke = 1.6,
  title,
  style,
  color,
}: {
  iconRef: string | undefined | null
  size?: number
  stroke?: number
  title?: string
  style?: CSSProperties
  color?: string
}) {
  const { pack, name } = parseIconRef(iconRef)
  const custom = useSelector(customIcons, (s) => (pack === 'custom' ? s[name] : undefined))
  if (pack === 'simple-icons' && name in simple) {
    const si = simple[name]
    if (si) {
      return (
        <svg
          role={title ? 'img' : undefined}
          aria-hidden={title ? undefined : true}
          aria-label={title}
          width={size}
          height={size}
          viewBox="0 0 24 24"
          fill="currentColor"
          style={{ flexShrink: 0, color, ...style }}
        >
          {/* 24 px Simple Icons are filled; scale down slightly to match Tabler's optical size. */}
          <path d={si.path} transform="translate(2 2) scale(0.8333)" />
        </svg>
      )
    }
  }
  if (pack === 'custom') {
    // Custom uploads are sanitized server-side and never inlined into the DOM (ADR-011b): full-colour icons render
    // through <img>, monochrome ones as a CSS mask so they take the surrounding text colour in both themes.
    const url = custom?.url ?? customIconURL(name)
    if (custom?.monochrome) {
      const mask = `url("${url}") center / contain no-repeat`
      return (
        <span
          role={title ? 'img' : undefined}
          aria-hidden={title ? undefined : true}
          aria-label={title}
          style={{
            display: 'inline-block',
            width: size,
            height: size,
            flexShrink: 0,
            backgroundColor: color ?? 'currentColor',
            mask,
            WebkitMask: mask,
            ...style,
          }}
        />
      )
    }
    return <img src={url} width={size} height={size} alt={title ?? ''} style={{ flexShrink: 0, ...style }} />
  }
  const props: IconProps = { size, stroke, style: { flexShrink: 0, ...style }, color }
  return createElement(
    tablerIcon(pack === 'tabler' ? name : 'cube'),
    title ? { ...props, role: 'img', 'aria-label': title } : { ...props, 'aria-hidden': true },
  )
}

/** Renders a Tabler icon by name (status vocabularies in lib/status.ts carry icon names). */
export function NamedIcon({ name, size = 14, stroke = 1.8 }: { name: string; size?: number; stroke?: number }) {
  return createElement(tablerIcon(name), { size, stroke, 'aria-hidden': true, style: { flexShrink: 0 } })
}
