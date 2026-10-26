import {
  IconActivity,
  IconAlertOctagon,
  IconAlertTriangle,
  IconApi,
  IconApps,
  IconArrowBigUpLines,
  IconArrowGuide,
  IconArrowsExchange,
  IconBell,
  IconBook,
  IconBox,
  IconBoxModel,
  IconBrandAws,
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
  IconDeviceDesktop,
  IconDoor,
  IconDoorEnter,
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
  IconLock,
  IconLockAccess,
  IconMapPin,
  IconMinus,
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
  IconRouter,
  IconServer,
  IconServer2,
  IconServerBolt,
  IconSettings,
  IconShieldCheck,
  IconShieldLock,
  IconStack2,
  IconTag,
  IconTerminal2,
  IconTopologyStar3,
  IconUsersGroup,
  IconWorld,
  IconWorldWww,
  type Icon,
  type IconProps,
} from '@tabler/icons-react'
import {
  siCloudflare,
  siDigitalocean,
  siDocker,
  siGitea,
  siGithub,
  siGoogle,
  siGitlab,
  siGrafana,
  siHetzner,
  siHomeassistant,
  siKubernetes,
  siLetsencrypt,
  siNatsdotio,
  siPostgresql,
  siPrometheus,
  siProxmox,
  siTerraform,
  siTraefikproxy,
  siUbiquiti,
} from 'simple-icons'
import { Store, useSelector } from '@tanstack/react-store'
import { createElement, useEffect, type CSSProperties } from 'react'

/**
 * Icon references use the `pack:name` format (ADR-011b). Tabler icons render as components; Simple Icons (CC0) render
 * from their path data as monochrome SVGs tinted with currentColor so they follow the theme; theSVG icons render from
 * the generated manifest (thesvg-manifest.json, loaded on first use) as a tinted CSS mask or an <img>. Unknown
 * references fall back to a neutral cube.
 */
const tabler: Record<string, Icon> = {
  activity: IconActivity,
  'alert-octagon': IconAlertOctagon,
  'alert-triangle': IconAlertTriangle,
  api: IconApi,
  apps: IconApps,
  'arrow-big-up-lines': IconArrowBigUpLines,
  'arrow-guide': IconArrowGuide,
  'arrows-exchange': IconArrowsExchange,
  bell: IconBell,
  book: IconBook,
  box: IconBox,
  'box-model': IconBoxModel,
  'brand-aws': IconBrandAws,
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
  router: IconRouter,
  'device-desktop': IconDeviceDesktop,
  'shield-lock': IconShieldLock,
  'door-enter': IconDoorEnter,
  door: IconDoor,
  lock: IconLock,
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
  google: siGoogle,
  digitalocean: siDigitalocean,
  docker: siDocker,
  gitea: siGitea,
  github: siGithub,
  gitlab: siGitlab,
  grafana: siGrafana,
  hetzner: siHetzner,
  homeassistant: siHomeassistant,
  kubernetes: siKubernetes,
  letsencrypt: siLetsencrypt,
  natsdotio: siNatsdotio,
  postgresql: siPostgresql,
  prometheus: siPrometheus,
  proxmox: siProxmox,
  terraform: siTerraform,
  traefikproxy: siTraefikproxy,
  ubiquiti: siUbiquiti,
}

/** One bundled theSVG icon (an entry of thesvg-manifest.json, generated by web/scripts/build-icons.mjs). */
export interface TheSvgIcon {
  name: string
  title: string
  /** SPDX identifier of the icon's own license; the build script allows only permissive ones (ADR-011b). */
  license: string
  source: string
  hex?: string
  variant: string
  /**
   * `mask`: one paint (or a multi-colour icon too faint on one theme), tinted with currentColor; `image`: multi-colour,
   * rendered with its own colours through <img>.
   */
  render: 'mask' | 'image'
  /** Set for no-derivatives icons, which are bundled byte-for-byte and never recoloured: why the exception. */
  unmodified?: string
  /** Contrast of a multi-colour icon's key colour against the light and dark surfaces. */
  contrast?: { light: number; dark: number }
  svg: string
}

/**
 * The theSVG manifest is ~44 kB of path data (~16 kB gzipped), so it is a separate chunk loaded on the first `thesvg:`
 * reference rather than part of every page. Null until loaded.
 */
export const theSvgIcons = new Store<Record<string, TheSvgIcon> | null>(null)
let theSvgLoading: Promise<Record<string, TheSvgIcon>> | undefined

export function loadTheSvgIcons(): Promise<Record<string, TheSvgIcon>> {
  theSvgLoading ??= import('./thesvg-manifest.json').then((m) => {
    const icons = Object.fromEntries((m.default.icons as TheSvgIcon[]).map((i) => [i.name, i]))
    theSvgIcons.setState(() => icons)
    return icons
  })
  return theSvgLoading
}

const svgDataURIs = new Map<string, string>()

/** A data: URI for a bundled (build-time, license-checked, allowlist-validated) SVG; never inlined into the DOM. */
function svgDataURI(icon: TheSvgIcon): string {
  let uri = svgDataURIs.get(icon.name)
  if (uri === undefined) {
    uri = `data:image/svg+xml,${encodeURIComponent(icon.svg)}`
    svgDataURIs.set(icon.name, uri)
  }
  return uri
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
  if (pack === 'simple-icons') return `#${simple[name]?.hex ?? ''}`
  const hex = pack === 'thesvg' ? theSvgIcons.state?.[name]?.hex : undefined
  return hex ? `#${hex}` : undefined
}

/** A monochrome image (uploaded or bundled) drawn as a CSS mask, so it takes the surrounding text colour. */
function MaskIcon({
  url,
  size,
  inset = 0,
  title,
  color,
  style,
}: {
  url: string
  size: number
  /** Padding inside the box, in px (the mask is drawn in the content box). */
  inset?: number
  title?: string
  color?: string
  style?: CSSProperties
}) {
  const mask = `url("${url}") center / contain no-repeat content-box`
  return (
    <span
      role={title ? 'img' : undefined}
      aria-hidden={title ? undefined : true}
      aria-label={title}
      style={{
        display: 'inline-block',
        width: size,
        height: size,
        boxSizing: 'border-box',
        padding: inset,
        flexShrink: 0,
        backgroundColor: color ?? 'currentColor',
        mask,
        WebkitMask: mask,
        ...style,
      }}
    />
  )
}

function TheSvgRefIcon({
  name,
  size,
  title,
  color,
  style,
}: {
  name: string
  size: number
  title?: string
  color?: string
  style?: CSSProperties
}) {
  const icons = useSelector(theSvgIcons, (s) => s)
  useEffect(() => {
    if (!icons) void loadTheSvgIcons()
  }, [icons])
  if (!icons) {
    // Same footprint while the manifest chunk loads, so rows and map nodes do not shift.
    return <span aria-hidden style={{ display: 'inline-block', width: size, height: size, flexShrink: 0, ...style }} />
  }
  const icon = icons[name]
  if (!icon) {
    const props: IconProps = { size, style: { flexShrink: 0, ...style }, color }
    return createElement(
      IconCube,
      title ? { ...props, role: 'img', 'aria-label': title } : { ...props, 'aria-hidden': true },
    )
  }
  const url = svgDataURI(icon)
  // Brand logos fill their viewBox; inset them like Simple Icons to match Tabler's optical size.
  const inset = size / 12
  if (icon.render === 'mask') {
    return <MaskIcon url={url} size={size} inset={inset} title={title} color={color} style={style} />
  }
  // Multi-colour (and no-derivatives) icons keep their published colours, which the build script checked reach 3:1
  // on both themes' surfaces; `color` does not apply.
  return (
    <img
      src={url}
      width={size}
      height={size}
      alt={title ?? ''}
      style={{ flexShrink: 0, boxSizing: 'border-box', padding: inset, objectFit: 'contain', ...style }}
    />
  )
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
    if (custom?.monochrome) return <MaskIcon url={url} size={size} title={title} color={color} style={style} />
    return <img src={url} width={size} height={size} alt={title ?? ''} style={{ flexShrink: 0, ...style }} />
  }
  if (pack === 'thesvg') return <TheSvgRefIcon name={name} size={size} title={title} color={color} style={style} />
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

/**
 * Whether a `tabler:`, `simple-icons:` or `thesvg:` reference resolves to a bundled icon rather than the fallback
 * cube. theSVG icons are known once loadTheSvgIcons() has resolved.
 */
export function isBundledIcon(ref: string): boolean {
  const { pack, name } = parseIconRef(ref)
  if (pack === 'tabler') return name in tabler || name === 'cube'
  if (pack === 'simple-icons') return name in simple
  if (pack === 'thesvg') return theSvgIcons.state?.[name] !== undefined
  return false
}

export interface IconChoice {
  ref: string
  pack: string
  title: string
}

/**
 * Every bundled icon (and the uploaded ones passed in) as `pack:name` choices for icon pickers. theSVG icons are
 * included once loadTheSvgIcons() has resolved.
 */
export function iconChoices(
  custom: readonly string[] = [],
  theSvg: Record<string, TheSvgIcon> | null = theSvgIcons.state,
): IconChoice[] {
  return [
    ...Object.keys(tabler)
      .sort()
      .map((n) => ({ ref: `tabler:${n}`, pack: 'tabler', title: n.replaceAll('-', ' ') })),
    ...Object.entries(simple).map(([n, i]) => ({ ref: `simple-icons:${n}`, pack: 'simple-icons', title: i.title })),
    ...Object.values(theSvg ?? {}).map((i) => ({ ref: `thesvg:${i.name}`, pack: 'thesvg', title: i.title })),
    ...custom.map((n) => ({ ref: `custom:${n}`, pack: 'custom', title: n })),
  ]
}
