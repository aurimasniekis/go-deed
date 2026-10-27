import type { Rect } from '@xyflow/react'

/** Space around the drawing in an exported image, in CSS pixels. */
const PAD = 24
/** Browser canvas limits (Chrome: 16,384 px a side; Safari is stricter on area): larger drawings are scaled down. */
const MAX_SIDE = 16_384
const MAX_AREA = 100_000_000

export interface MapImage {
  blob: Blob
  width: number
  height: number
  /** 1 at 1:1; below 1 when the drawing had to be scaled down to fit the browser's canvas limits. */
  scale: number
}

/** The first non-transparent background behind el, so the image has the canvas colour of the current theme. */
function backgroundOf(el: Element): string {
  for (let cur: Element | null = el; cur; cur = cur.parentElement) {
    const bg = getComputedStyle(cur).backgroundColor
    if (bg && bg !== 'transparent' && !/rgba\(.*,\s*0\)$/.test(bg)) return bg
  }
  return getComputedStyle(document.body).backgroundColor || '#ffffff'
}

const dataURLs = new Map<string, Promise<string>>()

/** A same-origin resource as a data: URL (cached), or "" when it cannot be fetched. */
function dataURL(url: string): Promise<string> {
  let p = dataURLs.get(url)
  if (!p) {
    p = fetch(url, { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.blob() : Promise.reject(new Error(String(r.status)))))
      .then(
        (b) =>
          new Promise<string>((resolve) => {
            const fr = new FileReader()
            fr.onload = () => resolve(typeof fr.result === 'string' ? fr.result : '')
            fr.onerror = () => resolve('')
            fr.readAsDataURL(b)
          }),
      )
      .catch(() => '')
    dataURLs.set(url, p)
  }
  return p
}

const urlRE = /url\((["']?)([^"')]+)\1\)/g

/** Replaces every non-data url(…) in css (resolved against base) with a data: URL. */
async function inlineURLs(css: string, base: string): Promise<string> {
  const urls = new Set<string>()
  for (const m of css.matchAll(urlRE)) if (m[2] && !m[2].startsWith('data:')) urls.add(m[2])
  const out = new Map<string, string>()
  await Promise.all([...urls].map(async (u) => out.set(u, await dataURL(new URL(u, base).href))))
  return css.replace(urlRE, (all, _q: string, u: string) => {
    const d = out.get(u)
    return d ? `url("${d}")` : all
  })
}

const sheetTexts = new Map<string, Promise<string>>()

/**
 * The page's own CSS for the image: the source text of every stylesheet (not its rules' cssText, which drops
 * shorthands built from var(), such as `border: 1px solid var(--deed-border)`), with fonts and other url()s inlined
 * (an SVG image cannot load anything) and `:root` rules applying to the image's root element.
 */
async function pageCSS(): Promise<string> {
  const parts = await Promise.all(
    [...document.styleSheets].map(async (sheet) => {
      if (sheet.href) {
        const href = sheet.href
        let text = sheetTexts.get(href)
        if (!text) {
          text = fetch(href, { credentials: 'same-origin' })
            .then((r) => (r.ok ? r.text() : ''))
            .then((t) => inlineURLs(t, href))
            .catch(() => '')
          sheetTexts.set(href, text)
        }
        return text
      }
      return inlineURLs(sheet.ownerNode?.textContent ?? '', document.baseURI)
    }),
  )
  return parts.join('\n').replace(/:root\b/g, '.deed-export-root')
}

/** Copies the theme: CSS custom properties, colour-scheme attributes and text styles of <html> and <body>. */
function rootShell(width: number, height: number): HTMLElement {
  const root = document.createElement('div')
  root.className = `deed-export-root ${document.documentElement.className} ${document.body.className}`
  for (const el of [document.documentElement, document.body]) {
    for (const a of el.attributes) if (a.name.startsWith('data-')) root.setAttribute(a.name, a.value)
  }
  const html = getComputedStyle(document.documentElement)
  for (const p of html) if (p.startsWith('--')) root.style.setProperty(p, html.getPropertyValue(p))
  const body = getComputedStyle(document.body)
  for (const p of ['font-family', 'font-size', 'line-height', 'color', 'color-scheme', '-webkit-font-smoothing'])
    root.style.setProperty(p, body.getPropertyValue(p))
  root.style.setProperty('width', `${width}px`)
  root.style.setProperty('height', `${height}px`)
  root.style.setProperty('margin', '0')
  root.style.setProperty('overflow', 'hidden')
  return root
}

/**
 * Renders the map's React Flow viewport element as a PNG of the whole drawing at zoom 1 (1:1, one CSS pixel per graph
 * unit), whatever the current zoom and pan: the bounds of every node plus a small margin. Controls, the minimap, the
 * legend and the dot grid are outside the viewport element and are not part of it.
 *
 * The drawing is a plain clone of the DOM inside an SVG foreignObject, styled by the page's own stylesheets inside the
 * image under copies of its ancestors (so selectors such as `.react-flow__node .deed-node` and the colour scheme still
 * match): no per-element style copying, which takes minutes for a map of 2,000 cards.
 */
export async function renderMapImage(viewport: HTMLElement, bounds: Rect): Promise<MapImage> {
  const width = Math.ceil(bounds.width + PAD * 2)
  const height = Math.ceil(bounds.height + PAD * 2)
  const scale = Math.min(1, MAX_SIDE / width, MAX_SIDE / height, Math.sqrt(MAX_AREA / (width * height)))

  const css = await pageCSS()
  const root = rootShell(width, height)
  // Shallow copies of the viewport's ancestors keep their classes and attributes, laid out to hold the whole drawing.
  let parent: HTMLElement = root
  const chain: HTMLElement[] = []
  for (let el = viewport.parentElement; el && el !== document.body; el = el.parentElement) chain.unshift(el)
  for (const el of chain) {
    const shell = el.cloneNode(false) as HTMLElement
    shell.removeAttribute('id')
    for (const [p, v] of [
      ['position', 'relative'],
      ['inset', 'auto'],
      ['width', `${width}px`],
      ['height', `${height}px`],
      ['margin', '0'],
      ['padding', '0'],
      ['border', '0'],
      ['overflow', 'visible'],
      ['transform', 'none'],
    ] as const)
      shell.style.setProperty(p, v, 'important')
    parent.appendChild(shell)
    parent = shell
  }
  const clone = viewport.cloneNode(true) as HTMLElement
  clone.style.setProperty('transform', `translate(${PAD - bounds.x}px, ${PAD - bounds.y}px) scale(1)`, 'important')
  parent.appendChild(clone)
  // Server-hosted images (custom icons) as data: URLs; theSVG icons and inline SVG are already self-contained.
  await Promise.all(
    [...clone.querySelectorAll('img')].map(async (img) => {
      const src = img.getAttribute('src')
      if (src && !src.startsWith('data:'))
        img.setAttribute('src', (await dataURL(new URL(src, document.baseURI).href)) || src)
    }),
  )
  await Promise.all(
    [...clone.querySelectorAll<HTMLElement>('[style*="url("]')].map(async (el) => {
      el.setAttribute('style', await inlineURLs(el.getAttribute('style') ?? '', document.baseURI))
    }),
  )
  const style = document.createElement('style')
  style.textContent = css
  root.prepend(style)

  const xhtml = new XMLSerializer().serializeToString(root)
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}">` +
    `<foreignObject x="0" y="0" width="100%" height="100%">${xhtml}</foreignObject></svg>`
  const img = new Image()
  img.decoding = 'async'
  img.src = 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(svg)
  await img.decode()

  const canvas = document.createElement('canvas')
  canvas.width = Math.max(1, Math.round(width * scale))
  canvas.height = Math.max(1, Math.round(height * scale))
  const ctx = canvas.getContext('2d')
  if (!ctx) throw new Error('the browser could not create a canvas')
  ctx.fillStyle = backgroundOf(viewport)
  ctx.fillRect(0, 0, canvas.width, canvas.height)
  ctx.scale(scale, scale)
  ctx.drawImage(img, 0, 0, width, height)
  const blob = await new Promise<Blob | null>((resolve) => {
    canvas.toBlob(resolve, 'image/png')
  })
  if (!blob) throw new Error('the browser could not encode the image')
  return { blob, width: canvas.width, height: canvas.height, scale }
}

/**
 * Puts an image on the clipboard, or downloads it where the clipboard cannot take images (no ClipboardItem, an
 * insecure origin, or a refused permission). The clipboard write starts synchronously with a promised blob, which
 * Safari requires to keep the click's user activation while the image renders.
 */
export async function copyOrDownload(image: Promise<MapImage>, filename: string): Promise<'copied' | 'downloaded'> {
  let copied: Promise<void> | null = null
  // navigator.clipboard exists only in secure contexts (HTTPS or localhost).
  if (typeof ClipboardItem !== 'undefined' && window.isSecureContext) {
    copied = navigator.clipboard.write([new ClipboardItem({ 'image/png': image.then((i) => i.blob) })])
    copied.catch(() => undefined) // awaited below once the image is known to exist; never an unhandled rejection
  }
  const { blob } = await image
  if (copied) {
    try {
      await copied
      return 'copied'
    } catch {
      // fall through to the download
    }
  }
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  setTimeout(() => {
    URL.revokeObjectURL(url)
  }, 10_000)
  return 'downloaded'
}

/** Resolves once cond() holds on an animation frame (and one more frame has painted), or rejects after timeoutMs. */
export function untilFrame(cond: () => boolean, timeoutMs: number): Promise<void> {
  const start = performance.now()
  return new Promise((resolve, reject) => {
    const tick = () => {
      if (cond()) {
        requestAnimationFrame(() => {
          resolve()
        })
        return
      }
      if (performance.now() - start > timeoutMs) {
        reject(new Error('the map did not finish drawing'))
        return
      }
      requestAnimationFrame(tick)
    }
    requestAnimationFrame(tick)
  })
}
