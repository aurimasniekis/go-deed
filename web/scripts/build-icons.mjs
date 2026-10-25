#!/usr/bin/env node
// Generates src/components/thesvg-manifest.json: the curated theSVG icons bundled into the web UI (ADR-011b).
//
//   node scripts/build-icons.mjs          regenerate the manifest from the pinned @thesvg/icons devDependency
//   node scripts/build-icons.mjs --check  fail when the checked-in manifest differs from what would be generated
//
// The script reads each curated icon's module from @thesvg/icons (pinned in package.json) and fails when:
//   - the installed package is not the pinned version;
//   - an icon's per-icon `license` is not on LICENSES, unless the icon is on UNMODIFIED (no-derivatives licenses,
//     allowlisted one by one with a justification and rendered byte-for-byte as published);
//   - an icon's markup contains anything but static vector content (scripts, styles, foreign or namespaced elements,
//     event handlers, external references, data: URIs, entities) or has no viewBox.
// Monochrome icons (one paint) are recorded as `mask` and tinted with currentColor by the UI; multi-colour icons are
// `image` and keep their colours when their key colour reaches 3:1 against both themes' surfaces, else they fall back
// to `mask` (a no-derivatives icon that falls short fails the build instead, since it must not be recoloured).
import { readFileSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const PACKAGE = '@thesvg/icons'
const VERSION = '3.3.13'
const OUT = fileURLToPath(new URL('../src/components/thesvg-manifest.json', import.meta.url))

/** Per-icon licenses that allow redistribution and modification without copyleft or use restrictions. */
const LICENSES = new Set(['CC0-1.0', 'MIT', 'Apache-2.0', 'BSD-2-Clause', 'BSD-3-Clause', 'ISC', 'Unlicense'])

/**
 * No-derivatives icons bundled for unmodified use only: rendered from the published SVG without recolouring,
 * masking or minification (resizing is a format change the license permits). Attribution is in the manifest and
 * NOTICE. Each entry says why the icon is worth the exception.
 */
const UNMODIFIED = {
  'aws-amazon-ec2': 'AWS Architecture Icon; aws_instance (Terraform AWS mapping) is an EC2 instance',
  'aws-amazon-route-53': 'AWS Architecture Icon; aws_route53_zone / aws_route53_record (Terraform AWS mapping)',
  'aws-amazon-simple-storage-service': 'AWS Architecture Icon; aws_s3_bucket (Terraform AWS mapping)',
  'aws-amazon-rds': 'AWS Architecture Icon; aws_db_instance (Terraform AWS mapping)',
}

/**
 * The curated subset: brands relevant to an infrastructure inventory. `variant` defaults to `mono` when the icon has
 * one (it follows the theme), else `default`; `title` corrects a published title. Left out on purpose: Microsoft logos (theSVG
 * labels Azure and Windows MIT but its other Microsoft icons proprietary, so their provenance is doubtful), icons that
 * are only a wordmark and unreadable at 16 px (synology, qnap, vmware, juniper-networks), and the CC-BY-ND AWS
 * service icons other than those on UNMODIFIED.
 */
const ICONS = [
  // Clouds and hosting providers
  { slug: 'aws' },
  ...Object.keys(UNMODIFIED).map((slug) => ({ slug })),
  { slug: 'google-cloud' },
  { slug: 'gcp-compute-engine' },
  { slug: 'gcp-cloud-sql', title: 'Cloud SQL' },
  { slug: 'gcp-cloud-storage' },
  { slug: 'gcp-cloud-dns', title: 'Cloud DNS' },
  { slug: 'hetzner' },
  { slug: 'digitalocean' },
  { slug: 'cloudflare' },
  { slug: 'scaleway' },
  { slug: 'ovh' },
  { slug: 'akamai' },
  // Virtualisation, operating systems
  { slug: 'proxmox' },
  { slug: 'vsphere', title: 'vSphere' },
  // Storage and home lab
  { slug: 'truenas' },
  { slug: 'unraid' },
  { slug: 'home-assistant' },
  // Network
  { slug: 'opnsense' },
  { slug: 'pfsense' },
  { slug: 'mikrotik' },
  { slug: 'ubiquiti' },
  { slug: 'openwrt' },
  { slug: 'tailscale' },
  { slug: 'wireguard' },
  // Software
  { slug: 'valkey' },
]

const SURFACES = { light: '#ffffff', dark: '#242424' } // Mantine body backgrounds (theme/chart.ts surface)
const MIN_CONTRAST = 3 // WCAG 1.4.11 non-text contrast

const ELEMENTS = new Set([
  'svg',
  'g',
  'path',
  'circle',
  'ellipse',
  'line',
  'polyline',
  'polygon',
  'rect',
  'title',
  'desc',
  'defs',
  'linearGradient',
  'radialGradient',
  'stop',
  'clipPath',
  'mask',
  'use',
  'symbol',
])
const SHAPES = new Set(['path', 'circle', 'ellipse', 'line', 'polyline', 'polygon', 'rect'])
const NEUTRAL = new Set(['', 'none', 'transparent', 'inherit'])
const BLACK = new Set(['currentcolor', 'black', '#000', '#000000'])
const NAMED = { white: '#ffffff', black: '#000000' }

function fail(msg) {
  console.error(`build-icons: ${msg}`)
  process.exit(1)
}

/** Parses attributes of one tag; rejects anything that could reference or run something outside the image. */
function attributes(slug, raw) {
  const out = {}
  const rest = raw.replace(/([A-Za-z_][-\w:.]*)\s*=\s*(?:"([^"]*)"|'([^']*)')/g, (_, name, dq, sq) => {
    const value = dq ?? sq ?? ''
    const lower = name.toLowerCase()
    if (lower.startsWith('on')) fail(`${slug}: event handler attribute ${name}`)
    if (lower.includes(':') && !lower.startsWith('xmlns') && lower !== 'xlink:href' && lower !== 'xml:space') {
      fail(`${slug}: namespaced attribute ${name}`)
    }
    if ((lower === 'href' || lower === 'xlink:href') && !value.startsWith('#')) fail(`${slug}: external ${name}`)
    if (/javascript:|data:|@import|expression\s*\(/i.test(value)) fail(`${slug}: active content in ${name}`)
    for (const m of value.matchAll(/url\(\s*['"]?([^'")]*)/gi)) {
      if (!m[1]?.startsWith('#')) fail(`${slug}: url() to something other than a local fragment in ${name}`)
    }
    out[lower] = value
    return ''
  })
  if (rest.replace(/\/\s*$/, '').trim() !== '') fail(`${slug}: unparsable attributes ${JSON.stringify(rest.trim())}`)
  if (out.style !== undefined) {
    for (const decl of out.style.split(';')) {
      const [k, v] = decl.split(':').map((s) => s.trim().toLowerCase())
      if (k && v !== undefined && ['fill', 'stroke', 'stop-color'].includes(k)) out[k] = v
    }
  }
  return out
}

function hex(color) {
  const c = NAMED[color] ?? color
  if (/^#[0-9a-f]{3}$/.test(c)) return `#${[...c.slice(1)].map((d) => d + d).join('')}`
  if (/^#[0-9a-f]{6}$/.test(c)) return c
  return undefined
}

/** Walks the markup: validates it and collects the effective paints of every shape and gradient stop. */
function inspect(slug, svg) {
  if (/<!|<\?(?!xml\s)/.test(svg.replace(/<!--[\s\S]*?-->/g, ''))) fail(`${slug}: DOCTYPE, entity, CDATA or PI`)
  const body = svg.replace(/<\?xml[^>]*\?>/, '').replace(/<!--[\s\S]*?-->/g, '')
  const stack = []
  const paints = new Set()
  let gradients = false
  let viewBox = false
  let last = 0
  for (const m of body.matchAll(/<(\/?)([A-Za-z][-\w:.]*)([^>]*?)(\/?)>/g)) {
    const text = body.slice(last, m.index)
    last = m.index + m[0].length
    const inText = stack.at(-1)?.name
    if (text.trim() !== '' && inText !== 'title' && inText !== 'desc') fail(`${slug}: text content in <${inText}>`)
    const [, closing, name, rawAttrs, selfClosing] = m
    if (!ELEMENTS.has(name)) fail(`${slug}: element <${name}> is not allowed`)
    if (closing) {
      if (stack.pop()?.name !== name) fail(`${slug}: mismatched </${name}>`)
      continue
    }
    const a = attributes(slug, rawAttrs ?? '')
    if (name === 'svg' && stack.length === 0) viewBox = /^[-\d.\s,]+$/.test(a.viewbox ?? '')
    const parent = stack.at(-1) ?? { fill: undefined, stroke: undefined }
    const fill = (a.fill ?? parent.fill)?.toLowerCase()
    const stroke = (a.stroke ?? parent.stroke)?.toLowerCase()
    if (name === 'linearGradient' || name === 'radialGradient') gradients = true
    if (name === 'stop') paints.add(a['stop-color']?.toLowerCase() ?? 'black')
    if (SHAPES.has(name) && !stack.some((e) => ['defs', 'clipPath', 'mask', 'symbol'].includes(e.name))) {
      paints.add(fill ?? 'black')
      paints.add(stroke ?? 'none')
    }
    if (!selfClosing) stack.push({ name, fill, stroke })
  }
  if (stack.length > 0 || body.slice(last).trim() !== '') fail(`${slug}: unbalanced markup`)
  if (!viewBox) fail(`${slug}: no viewBox`)
  const colors = new Set()
  for (const p of paints) {
    if (NEUTRAL.has(p) || p.startsWith('url(')) continue
    if (BLACK.has(p)) colors.add('#000000')
    else colors.add(hex(p) ?? fail(`${slug}: unsupported paint ${JSON.stringify(p)}`))
  }
  return { colors: [...colors].sort(), gradients }
}

/**
 * Replaces a `<style>` sheet of plain class rules (`.a,.b{fill:#x}`, as the Google Cloud icons use) with inline
 * `style` attributes, so the icon passes the element allowlist. Anything but class selectors fails.
 */
function inlineClassStyles(slug, svg) {
  const sheets = [...svg.matchAll(/<style\b[^>]*>([\s\S]*?)<\/style>/g)]
  if (sheets.length === 0) return svg
  const rules = {}
  for (const sheet of sheets) {
    const css = (sheet[1] ?? '').replace(/\/\*[\s\S]*?\*\//g, '')
    const rest = css.replace(/([^{}]+)\{([^{}]*)\}/g, (_, selectors, decls) => {
      for (const sel of selectors.split(',').map((s) => s.trim())) {
        if (!/^\.[-\w]+$/.test(sel)) fail(`${slug}: unsupported CSS selector ${JSON.stringify(sel)}`)
        rules[sel.slice(1)] = `${rules[sel.slice(1)] ?? ''}${decls.trim().replace(/;?$/, ';')}`
      }
      return ''
    })
    if (rest.trim() !== '') fail(`${slug}: unsupported CSS ${JSON.stringify(rest.trim())}`)
  }
  return svg
    .replace(/<style\b[^>]*>[\s\S]*?<\/style>/g, '')
    .replace(/<defs>\s*<\/defs>/g, '')
    .replace(/<([A-Za-z]+)([^>]*?)\sclass="([^"]*)"([^>]*?)(\/?)>/g, (_, name, before, classes, after, close) => {
      const fromClasses = classes
        .split(/\s+/)
        .filter(Boolean)
        .map((c) => rules[c] ?? fail(`${slug}: class ${c} has no rule`))
        .join('')
      let attrs = `${before}${after}`
      const own = /\sstyle="([^"]*)"/.exec(attrs)
      attrs = attrs.replace(/\sstyle="[^"]*"/, '')
      return `<${name}${attrs} style="${fromClasses}${own?.[1] ?? ''}"${close}>`
    })
}

/** Drops the XML declaration, comments, title/desc (the UI labels icons itself) and insignificant whitespace. */
function minify(svg) {
  return svg
    .replace(/<\?xml[^>]*\?>/, '')
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/<(title|desc)\b[^>]*>[\s\S]*?<\/\1>/g, '')
    .replace(/\sdata-[-\w]+="[^"]*"/g, '')
    .replace(/>\s+</g, '><')
    .replace(/\s+/g, ' ')
    .replace(/\s*(\/?>)/g, '$1')
    .trim()
}

function luminance(color) {
  const [r, g, b] = [1, 3, 5].map((i) => {
    const c = parseInt(color.slice(i, i + 2), 16) / 255
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

function contrast(a, b) {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p)
  return (x + 0.05) / (y + 0.05)
}

const round = (n) => Math.round(n * 100) / 100

async function build() {
  const pkg = JSON.parse(readFileSync(new URL(`../node_modules/${PACKAGE}/package.json`, import.meta.url), 'utf8'))
  if (pkg.version !== VERSION) fail(`${PACKAGE} ${pkg.version} is installed; this script expects ${VERSION}`)
  const icons = []
  for (const { slug, variant: wanted, title } of ICONS) {
    if (!/^[a-z0-9][a-z0-9._-]{0,127}$/.test(slug)) fail(`${slug}: not a valid icon reference name`)
    const mod = (await import(`${PACKAGE}/${slug}`)).default
    const unmodified = UNMODIFIED[slug] !== undefined
    if (!LICENSES.has(mod.license) && !unmodified)
      fail(`${slug}: license ${JSON.stringify(mod.license)} is not allowed`)
    if (unmodified && !/^CC-BY-ND-/.test(mod.license)) {
      fail(`${slug}: on the no-derivatives allowlist but licensed ${JSON.stringify(mod.license)}`)
    }
    const variant = unmodified ? 'default' : (wanted ?? ('mono' in mod.variants ? 'mono' : 'default'))
    const published = mod.variants[variant] ?? fail(`${slug}: no ${variant} variant`)
    const source = unmodified ? published : inlineClassStyles(slug, published)
    const { colors, gradients } = inspect(slug, source)
    const mono = !unmodified && !gradients && colors.length <= 1
    const entry = {
      name: slug,
      title: title ?? mod.title,
      license: mod.license,
      source: mod.url || `https://thesvg.org/icons/${slug}`,
      hex: /^[0-9a-fA-F]{3}([0-9a-fA-F]{3})?$/.test(mod.hex) ? hex(`#${mod.hex.toLowerCase()}`).slice(1) : undefined,
      variant,
      render: mono ? 'mask' : 'image',
    }
    if (unmodified) entry.unmodified = UNMODIFIED[slug]
    if (!mono) {
      // Judge legibility by the icon's key colour: the brand colour when the drawing uses it, else its best colour
      // other than black and white (those are usually glyphs drawn on top of a coloured shape, not its outline).
      const brand = entry.hex && colors.includes(`#${entry.hex}`) ? [`#${entry.hex}`] : undefined
      const key = brand ?? colors.filter((c) => c !== '#000000' && c !== '#ffffff')
      if (key.length === 0) fail(`${slug}: multi-colour icon without a key colour`)
      const best = (surface) => round(Math.max(...key.map((c) => contrast(c, surface))))
      const light = best(SURFACES.light)
      const dark = best(SURFACES.dark)
      if (light < MIN_CONTRAST || dark < MIN_CONTRAST) {
        // Too faint on one theme: fall back to a monochrome rendering, which follows the text colour. A
        // no-derivatives icon cannot be recoloured, so it has to leave the curated list instead.
        if (unmodified) fail(`${slug}: key colour below ${MIN_CONTRAST}:1 (light ${light}, dark ${dark})`)
        entry.render = 'mask'
      } else {
        entry.contrast = { light, dark }
      }
    }
    entry.svg = unmodified ? source : minify(source)
    icons.push(entry)
  }
  icons.sort((a, b) => a.name.localeCompare(b.name))
  return {
    package: PACKAGE,
    version: VERSION,
    license: `${PACKAGE} collection: ${pkg.license}; each icon carries its own license below`,
    generatedBy: 'web/scripts/build-icons.mjs',
    icons,
  }
}

const manifest = `${JSON.stringify(await build(), null, 2)}\n`
if (process.argv.includes('--check')) {
  if (readFileSync(OUT, 'utf8') !== manifest) fail('src/components/thesvg-manifest.json is out of date; run pnpm icons')
  console.log('build-icons: manifest is up to date')
} else {
  writeFileSync(OUT, manifest)
  console.log(`build-icons: wrote ${OUT} (${JSON.parse(manifest).icons.length} icons)`)
}
