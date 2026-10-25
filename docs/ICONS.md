# Icons

Kinds, entities and Terraform mappings refer to icons by reference; the web UI resolves the reference to a bundled
icon or to an admin-uploaded SVG (ADR-011b).

## References
An icon reference is `pack:name`. The API accepts exactly this pattern for `metadata.icon` on entities and for a
kind's `icon`:

```
^(tabler|simple-icons|thesvg|custom):[a-z0-9][a-z0-9._-]{0,127}$
```

| Pack | Content | Examples |
|---|---|---|
| `tabler` | generic UI icons, the fallback for kinds | `tabler:server`, `tabler:database`, `tabler:certificate` |
| `simple-icons` | brand logos | `simple-icons:cloudflare`, `simple-icons:docker`, `simple-icons:grafana` |
| `thesvg` | brand logos and cloud service icons from the theSVG collection (curated subset, see below) | `thesvg:proxmox`, `thesvg:aws`, `thesvg:aws-amazon-ec2`, `thesvg:gcp-cloud-sql` |
| `custom` | SVGs uploaded to this installation | `custom:core-router` |

An invalid reference is refused with `must be pack:name with pack tabler, simple-icons, thesvg or custom`. The API
checks the format only; it does not check that the named icon exists in a pack or has been uploaded.

Where icons come from:
- every built-in kind has a default icon (`internal/catalog/builtin/kinds.yaml`), and custom kinds may set one;
- `metadata.icon` on an entity overrides its kind's icon;
- Terraform mappings set icons on the entities they create (`cloudflare_*` → `simple-icons:cloudflare`,
  `hcloud_*` → `simple-icons:hetzner`, `aws_instance` → `thesvg:aws-amazon-ec2`, `aws_route53_*` →
  `thesvg:aws-amazon-route-53`, `aws_s3_bucket` → `thesvg:aws-amazon-simple-storage-service`, `aws_db_instance` →
  `thesvg:aws-amazon-rds`, `vsphere_virtual_machine` → `thesvg:vsphere`, see `internal/tfimport/mappings/`); the
  GitHub connector and the Docker agent set `simple-icons:github` and `simple-icons:docker`.

The web UI bundles a curated subset of Tabler (`@tabler/icons-react`) and Simple Icons (`simple-icons`) by named
imports in `web/src/components/icons.tsx`, so only those icons ship; the pinned versions in `web/package.json` record
the source. Simple Icons render from their path data as monochrome SVGs tinted with `currentColor`, so they follow
the theme; brand colors are used only as accents. theSVG icons are bundled from a generated manifest (below). A
reference to an icon that is not bundled renders as a neutral cube. `web/src/components/icons.test.ts` checks that
every icon the server assigns (built-in kinds, Terraform mappings, connectors, demo seed) is bundled. To use another
logo, upload it as a custom icon.

The kind editor's **Icon** field suggests every bundled icon and the uploaded ones; type part of a reference or a
title (`azure`, `route 53`, `nas`) to search. Any well-formed reference can still be typed.

### theSVG
theSVG icons come from the npm package `@thesvg/icons`, pinned in `web/package.json` (devDependency, exact version;
the package publishes one module per icon with `slug`, `title`, `hex`, `variants`, `license` and `url`).
`web/scripts/build-icons.mjs` reads the curated list from it and writes `web/src/components/thesvg-manifest.json`,
which is checked in and records for each icon its name, title, license, source URL, brand hex, the variant used, how
it renders and the SVG. The script fails when:

- the installed package is not the pinned version;
- an icon's license is not CC0-1.0, MIT, Apache-2.0, BSD-2-Clause, BSD-3-Clause, ISC or Unlicense, unless the icon
  is on its `UNMODIFIED` allowlist (no-derivatives icons, each with a justification; they must be CC BY-ND and are
  bundled byte-for-byte);
- the markup is not static vector content (`<script>`, `<style>` other than plain class rules, which are inlined,
  foreign or namespaced elements, event handlers, `href` or `url()` outside the icon, `data:` URIs, DOCTYPEs) or has
  no `viewBox`;
- an unmodified icon's key colour is below 3:1 on one of the themes.

```sh
pnpm --dir web run icons        # regenerate the manifest after editing the curated list
pnpm --dir web run icons:check  # fail when the checked-in manifest is out of date
```

Rendering: icons drawn in one paint (the `mono` variant where theSVG has one) render as a CSS mask over a `data:`
URI and take the surrounding text colour, like monochrome custom icons. Multi-colour icons render through `<img>`
with their own colours when their key colour (the brand colour, or the strongest colour other than black and white)
reaches 3:1 against both the light (`#ffffff`) and the dark (`#242424`) surface; otherwise the build falls back to
the mask. The SVG is never inserted into the DOM as markup. The manifest is a separate chunk (about 16 kB gzipped),
fetched on the first `thesvg:` reference or when the icon picker opens, so other pages do not pay for it.

Bundled theSVG icons (`@thesvg/icons` 3.3.13):

| Reference | Title | License | Rendering |
|---|---|---|---|
| `thesvg:akamai` | Akamai | CC0-1.0 | tinted |
| `thesvg:aws` | AWS | MIT | tinted |
| `thesvg:aws-amazon-ec2` | Amazon EC2 | CC-BY-ND-2.0 (unmodified) | colour |
| `thesvg:aws-amazon-rds` | Amazon RDS | CC-BY-ND-2.0 (unmodified) | colour |
| `thesvg:aws-amazon-route-53` | Amazon Route 53 | CC-BY-ND-2.0 (unmodified) | colour |
| `thesvg:aws-amazon-simple-storage-service` | Amazon Simple Storage Service | CC-BY-ND-2.0 (unmodified) | colour |
| `thesvg:cloudflare` | Cloudflare | CC0-1.0 | tinted |
| `thesvg:digitalocean` | DigitalOcean | CC0-1.0 | tinted |
| `thesvg:gcp-cloud-dns` | Cloud DNS | Apache-2.0 | colour |
| `thesvg:gcp-cloud-sql` | Cloud SQL | Apache-2.0 | colour |
| `thesvg:gcp-cloud-storage` | Cloud Storage | Apache-2.0 | colour |
| `thesvg:gcp-compute-engine` | Compute Engine | Apache-2.0 | colour |
| `thesvg:google-cloud` | Google Cloud | CC0-1.0 | tinted |
| `thesvg:hetzner` | Hetzner | CC0-1.0 | tinted |
| `thesvg:home-assistant` | Home Assistant | CC0-1.0 | tinted |
| `thesvg:microsoft-azure` | Microsoft Azure | MIT | colour |
| `thesvg:mikrotik` | MikroTik | CC0-1.0 | tinted |
| `thesvg:openwrt` | OpenWrt | CC0-1.0 | tinted |
| `thesvg:opnsense` | OPNsense | CC0-1.0 | tinted |
| `thesvg:ovh` | OVH | CC0-1.0 | tinted |
| `thesvg:pfsense` | pfSense | CC0-1.0 | tinted |
| `thesvg:proxmox` | Proxmox | CC0-1.0 | tinted |
| `thesvg:scaleway` | Scaleway | CC0-1.0 | tinted |
| `thesvg:tailscale` | Tailscale | CC0-1.0 | tinted |
| `thesvg:truenas` | TrueNAS | CC0-1.0 | tinted |
| `thesvg:ubiquiti` | Ubiquiti | CC0-1.0 | tinted |
| `thesvg:unraid` | Unraid | CC0-1.0 | tinted |
| `thesvg:valkey` | Valkey | BSD-3-Clause | tinted |
| `thesvg:vsphere` | vSphere | MIT | tinted |
| `thesvg:windows` | Windows | MIT | tinted |
| `thesvg:wireguard` | WireGuard | CC0-1.0 | tinted |

The four AWS Architecture Icons are licensed CC BY-ND 2.0 and allowlisted for unmodified use because the Terraform
AWS mapping assigns them (`aws_instance`, `aws_route53_zone` / `aws_route53_record`, `aws_s3_bucket`,
`aws_db_instance`): they ship exactly as published and are only scaled. The other AWS service icons are not
bundled. Logos that are only a wordmark and unreadable at 16 px (Synology, QNAP, VMware, Juniper) are left out;
upload them as custom icons instead.

## Custom icons
Installation owners and admins upload SVGs; every signed-in user can list and display them.

| Method | Path | Who | Purpose |
|---|---|---|---|
| `GET` | `/api/v1/icons` | any user | list icons (`name`, `ref`, `url`, `sha256`, `monochrome`, `bytes`, creator, time) |
| `PUT` | `/api/v1/icons/{name}` | owner, admin | upload or replace `{"svg": "<svg …>"}` |
| `DELETE` | `/api/v1/icons/{name}` | owner, admin | delete |
| `GET` | `/api/v1/icons/{name}.svg` | any authenticated caller | the sanitized SVG, for `<img>` tags |

```sh
jq -n --rawfile svg core-router.svg '{svg: $svg}' |
  curl -s -X PUT https://deed.example.com/api/v1/icons/core-router \
    -H "Authorization: Bearer $DEED_TOKEN" -H 'Content-Type: application/json' -d @-
# then reference it: "metadata": {"icon": "custom:core-router"}
```

- Names are lowercase slugs: `^[a-z0-9][a-z0-9._-]{0,127}$` (otherwise `icon names are lowercase slugs`).
- Uploading to an existing name replaces the icon.
- Uploads and deletions are audited as `icon.uploaded` (with the SHA-256, the stored size and how many bytes the
  sanitizer removed) and `icon.deleted`.
- Deleting an icon does not touch entities that reference it; they fall back to their kind's icon.

## Sanitizer
Uploads are parsed and **re-serialized** through an allowlist (`internal/icons/sanitize.go`); the original bytes
are never stored. Rejections answer `422` with `not an acceptable SVG: <reason>`.

| Rule | Behavior |
|---|---|
| Size | at most 64 KiB (65,536 bytes) |
| Structure | strict XML; at most 32 levels deep and 5,000 elements; exactly one root element, `<svg>` in the SVG namespace |
| DOCTYPE, entities, directives | rejected |
| Processing instructions | rejected, except the `<?xml …?>` declaration |
| Comments | dropped |
| Elements | kept: `svg`, `g`, `path`, `circle`, `ellipse`, `line`, `polyline`, `polygon`, `rect`, `title`, `desc`, `defs`, `linearGradient`, `radialGradient`, `stop`, `clipPath`, `mask`, `use`, `symbol`. Anything else (`script`, `style`, `foreignObject`, `image`, `a`, `animate`, `set`, …) is dropped **with its content**; so are elements in other namespaces |
| Attributes | geometry, paint, stroke, opacity, transform, gradient, clip/mask, `viewBox`, `preserveAspectRatio`, `id`, `href` and a few others; everything else is dropped (`on*`, `style`, `class`, `xml:base`, namespaced attributes) |
| `href` | only local fragments (`#name`); `xlink:href` is rewritten to `href` |
| Values | at most 16 KiB, limited to the characters geometry and colors need; anything containing `javascript`, `expression` or `data:` is dropped; `url(…)` may only point at a local `#id` |
| Text | whitespace-only text is dropped |

The root always gets `xmlns="http://www.w3.org/2000/svg"`. An icon is **monochrome** when every `fill`, `stroke`
and `stop-color` is empty, `none`, `currentColor`, `black`, `#000`, `#000000` or `inherit`; the UI may then tint
it to follow the theme.

## Serving
`GET /api/v1/icons/{name}.svg` requires authentication (`401` otherwise) and answers `404` for unknown names. It
sends:

| Header | Value |
|---|---|
| `Content-Type` | `image/svg+xml` |
| `Content-Security-Policy` | `default-src 'none'; sandbox` |
| `X-Content-Type-Options` | `nosniff` |
| `Content-Disposition` | `inline; filename="<name>.svg"` |
| `Cache-Control` | `private, no-cache` (revalidated by `ETag`, so a replaced icon shows up immediately) |
| `ETag` | the first 32 hex characters of the icon's SHA-256 (`If-None-Match` answers `304`) |

The UI renders custom icons only through `<img>`, so even an SVG that slipped past the sanitizer cannot run script
in the application's origin.

## Licensing and trademarks
| Pack | License |
|---|---|
| Simple Icons (https://simpleicons.org) | CC0 1.0 Universal |
| Tabler Icons (https://tabler.io/icons) | MIT License, Copyright (c) 2020-2026 Paweł Kuna |
| theSVG (https://thesvg.org, `@thesvg/icons`) | collection and package code: MIT License, Copyright (c) 2025 thesvg.org; each icon under its own license, recorded per icon in `web/src/components/thesvg-manifest.json`: CC0-1.0, MIT, Apache-2.0, BSD-3-Clause, and CC-BY-ND-2.0 for the four unmodified AWS Architecture Icons (Amazon Web Services) |
| custom | whatever the uploader has the right to use |

No icon whose license is `Custom`, unknown, fair use, trademark-only, copyleft or non-commercial is bundled, and no
no-derivatives (e.g. CC BY-ND) icon except those allowlisted for unmodified use in `web/scripts/build-icons.mjs`
(ADR-011b); the build script refuses anything else.

All product names, logos and brands are property of their respective owners. Deed uses them for identification
only, to show which product an entity is (nominative use); this does not imply endorsement. A license on an icon
set covers the drawing, not the trademark it depicts: before uploading a logo as a custom icon, check that its
owner's guidelines allow this kind of use. See `NOTICE` for the attribution shipped with Deed.

## Validation status
`internal/icons/sanitize_test.go` covers geometry preservation, monochrome detection, removal of active content
(scripts, event handlers, `foreignObject`, `javascript:` and external `href`/`url()`, `<style>`, `data:` images,
`animate`/`set`) and rejection of DOCTYPEs, non-SVG roots, multiple roots, stylesheet processing instructions,
excessive nesting and oversized input. `FuzzSanitize` checks that output is re-accepted unchanged and never contains
active content (`make fuzz-smoke`). `web/src/components/icons.test.ts` checks bundle coverage of server-side references (including `thesvg:`), that
every theSVG manifest entry has an allowed license (no-derivatives only for allowlisted, unmodified, colour-rendered
icons), contains static vector markup only and, when rendered in colour, reaches 3:1 on both themes, and that
`thesvg:` references resolve. `pnpm --dir web run icons:check` regenerates the manifest from the pinned package and
fails when it differs from the checked-in file.
