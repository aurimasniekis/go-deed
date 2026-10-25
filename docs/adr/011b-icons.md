# ADR-011b: Brand logos and icon packs

- Icon reference format `pack:name` (`simple-icons:cloudflare`, `thesvg:proxmox`, `tabler:server`, `custom:<name>`).
- Packs: **Simple Icons** (CC0), **Tabler** (MIT, generic fallback), **theSVG** (collection MIT; per-icon license
  field), **custom** (admin uploads).
- `web/scripts/build-icons.mjs` bundles a curated subset into `web/src/icons/generated/` with a `manifest.json`
  (pack, name, title, license, source URL, brand hex). Icons whose license is `Custom`, unknown, or no-derivatives
  (e.g. CC BY-ND) are excluded unless explicitly allowlisted for unmodified use. `docs/ICONS.md` and NOTICE carry
  attribution and a trademark / nominative-use disclaimer.
- Kinds have default icons; entities override via `metadata.icon`; Terraform mappings assign icons
  (`cloudflare_*` → `simple-icons:cloudflare`).
- Custom uploads: SVG only, ≤ 64 KiB, sanitized server-side with an element/attribute allowlist (no `script`,
  `foreignObject`, event handlers, `href` to anything but local fragments, no external references, `url()` only to local fragments, no `style`),
  served from `/api/v1/icons/{name}.svg` with `Content-Security-Policy: default-src 'none'; sandbox`,
  `X-Content-Type-Options: nosniff`, rendered only via `<img>`.
- Monochrome icons are tinted with a CSS mask so they follow the theme; brand colors are used only as accents.
