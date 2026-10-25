# ADR-011b: Brand logos and icon packs

- Icon reference format `pack:name` (`simple-icons:cloudflare`, `thesvg:proxmox`, `tabler:server`, `custom:<name>`).
- Packs: **Simple Icons** (CC0), **Tabler** (MIT, generic fallback), **theSVG** (collection MIT; per-icon license
  field), **custom** (admin uploads).
- The web UI bundles a curated subset by named imports from pinned packages (`@tabler/icons-react`, `simple-icons`),
  so the bundler keeps only the icons listed in `web/src/components/icons.tsx`; the package versions in
  `web/package.json` are the manifest. `web/src/components/icons.test.ts` fails when a server-side reference (built-in
  kinds, Terraform mappings, connectors, demo seed) is not in the bundle. No `Custom`, unknown or no-derivatives
  (e.g. CC BY-ND) icons are bundled, except no-derivatives icons allowlisted one by one for unmodified use (below).
  `docs/ICONS.md` and NOTICE carry attribution and a trademark / nominative-use disclaimer.
- theSVG is bundled from the pinned devDependency `@thesvg/icons` through `web/scripts/build-icons.mjs`, which
  writes the checked-in manifest `web/src/components/thesvg-manifest.json` (name, title, license, source URL, brand
  hex, rendering and the SVG) for a curated list. The script is the license gate: it fails on a per-icon license
  outside CC0-1.0, MIT, Apache-2.0, BSD-2-Clause, BSD-3-Clause, ISC and Unlicense (so `Custom`, unknown, fair use,
  trademark-only, copyleft, non-commercial and no-derivatives icons are refused), unless the icon is on its
  `UNMODIFIED` allowlist with a written justification; such icons must be CC BY-ND and are bundled byte-for-byte,
  never recoloured or masked. It also refuses markup other than static vector content (scripts, styles it cannot
  inline, foreign or namespaced elements, event handlers, external references, `data:` URIs). `icons.test.ts`
  re-checks every manifest entry's license and markup; `pnpm --dir web run icons:check` fails when the manifest is
  stale. The manifest is a separate chunk, loaded on the first `thesvg:` reference.
- Kinds have default icons; entities override via `metadata.icon`; Terraform mappings assign icons
  (`cloudflare_*` → `simple-icons:cloudflare`, `aws_instance` → `thesvg:aws-amazon-ec2`).
- Custom uploads: SVG only, ≤ 64 KiB, sanitized server-side with an element/attribute allowlist (no `script`,
  `foreignObject`, event handlers, `href` to anything but local fragments, no external references, `url()` only to local fragments, no `style`),
  served from `/api/v1/icons/{name}.svg` with `Content-Security-Policy: default-src 'none'; sandbox`,
  `X-Content-Type-Options: nosniff`, rendered only via `<img>`.
- Monochrome icons are tinted with a CSS mask so they follow the theme; brand colors are used only as accents.
  Bundled theSVG icons are never inlined into the DOM: one-paint icons render as a CSS mask over a `data:` URI and
  take the text colour; multi-colour icons render through `<img>` with their own colours only when their key colour
  reaches 3:1 against both themes' surfaces (otherwise the build script falls back to the mask, or fails for an
  unmodified icon).
