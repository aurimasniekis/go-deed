# ADR-011b: Brand logos and icon packs

- Icon reference format `pack:name` (`simple-icons:cloudflare`, `thesvg:proxmox`, `tabler:server`, `custom:<name>`).
- Packs: **Simple Icons** (CC0), **Tabler** (MIT, generic fallback), **theSVG** (collection MIT; per-icon license
  field), **custom** (admin uploads).
- The web UI bundles a curated subset by named imports from pinned packages (`@tabler/icons-react`, `simple-icons`),
  so the bundler keeps only the icons listed in `web/src/components/icons.tsx`; the package versions in
  `web/package.json` are the manifest. `web/src/components/icons.test.ts` fails when a server-side reference (built-in
  kinds, Terraform mappings, connectors, demo seed) is not in the bundle. No `Custom`, unknown or no-derivatives
  (e.g. CC BY-ND) icons are bundled. `docs/ICONS.md` and NOTICE carry attribution and a trademark / nominative-use
  disclaimer.
- **Amendment (implementation):** the `thesvg` pack is accepted as a reference by the API but not bundled yet: no
  pinned, per-icon-licensed package was adopted, and a `thesvg:` reference renders as the fallback cube. Upload the
  logo as a `custom:` icon instead.
- Kinds have default icons; entities override via `metadata.icon`; Terraform mappings assign icons
  (`cloudflare_*` → `simple-icons:cloudflare`).
- Custom uploads: SVG only, ≤ 64 KiB, sanitized server-side with an element/attribute allowlist (no `script`,
  `foreignObject`, event handlers, `href` to anything but local fragments, no external references, `url()` only to local fragments, no `style`),
  served from `/api/v1/icons/{name}.svg` with `Content-Security-Policy: default-src 'none'; sandbox`,
  `X-Content-Type-Options: nosniff`, rendered only via `<img>`.
- Monochrome icons are tinted with a CSS mask so they follow the theme; brand colors are used only as accents.
