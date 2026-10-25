# Deed web UI

The web UI is a single-page React application in `web/`, built into `internal/webui/dist` and embedded in the `deed`
binary (ADR-011). It talks only to the public `/api/v1` API and `/auth/*` routes; everything it can do, the API and
CLI can do too.

![Map: neighbourhood of a service with measured traffic labels, light theme](assets/screenshots/map-focus-light.png)

| Dashboard (light)                                                 | Entity overview (dark)                                                      |
| ----------------------------------------------------------------- | --------------------------------------------------------------------------- |
| ![Dashboard, light theme](assets/screenshots/dashboard-light.png) | ![Entity overview, dark theme](assets/screenshots/entity-overview-dark.png) |

![Inventory, dark theme](assets/screenshots/inventory-dark.png)

Screenshots are produced by the accessibility suite (`web/e2e/screenshots/`, every view in both themes) against
synthetic seed data.

## Building and developing

| Command                       | What it does                                                                                                                     |
| ----------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `make web`                    | `pnpm install --frozen-lockfile`, then `make web-build`                                                                          |
| `make web-build`              | `tsc`, production build into `internal/webui/dist`, `scripts/check-dist.mjs`                                                     |
| `make web-dev`                | Vite dev server proxying `/api` and `/auth` to a running deed at `DEED_API` (default `http://127.0.0.1:8787`)                    |
| `make web-lint`               | ESLint (strict type-checked, react-hooks, jsx-a11y strict, `--max-warnings=0`) and Prettier check                                |
| `make web-test` / `web-check` | `tsc --noEmit`, lint, Vitest unit tests (`web/src/**/*.test.ts`); part of `make check`                                           |
| `make web-gen`                | Regenerates the typed API client; CI fails when it is stale                                                                      |
| `make web-e2e`                | Playwright + axe against a freshly seeded server (run `make web-build` first; builds `bin/deed` unless `DEED_E2E_BINARY` is set) |

- TypeScript 7 (the native compiler, installed as the `typescript-7` alias) type-checks the app. The `typescript`
  package itself is TypeScript 6, because typescript-eslint and openapi-typescript need the JavaScript compiler API
  that TypeScript 7 does not ship. No pnpm hooks are involved, so `package.json` + `pnpm-lock.yaml` alone install
  reproducibly (the container build relies on that).
- `make web-gen` (`pnpm gen:api`) regenerates `src/api/schema.gen.ts` from `api/openapi.json`; the client
  (`openapi-fetch`) is fully typed from it, so API drift fails `tsc`.
- The dev proxy rewrites `Origin` to the backend's origin, because the server's CSRF check compares `Origin` with its
  own URL.
- TanStack Query/Router devtools load only under `import.meta.env.DEV`. `scripts/check-dist.mjs` fails the build when
  `dist` contains devtools markers, inline `<script>` bodies, `eval`/`new Function`, non-image `data:` URIs in CSS,
  or is missing `README.txt`.

### Security constraints

- **CSP**: no inline scripts or styles from our code, no external fonts or CDNs, no `eval`. Fonts
  (`@fontsource-variable/inter`, `…/jetbrains-mono`) and icons (Tabler, `simple-icons`) are bundled. Mantine's
  injected `<style>` tags carry the per-request nonce read from `<meta name="deed-csp-nonce">`.
- **Session**: cookie session only. Tokens (API tokens, setup/recovery codes) are shown once and never written to
  `localStorage`/`sessionStorage`; `localStorage` holds UI preferences only (theme, map options, column layouts,
  dismissed hints).
- **CSRF**: every mutating request sends `X-Deed-CSRF` with the token from `GET /auth/me`; the token is replaced
  whenever the session rotates.
- **Re-authentication**: a 401/403 `reauth_required` or `mfa_required` problem opens the re-authentication modal
  (password + TOTP → `POST /auth/reauth`) or follows the problem's SSO `action`, then retries the original request
  once with the new CSRF token; concurrent requests share one prompt.
- ESLint bans `dangerouslySetInnerHTML`, `innerHTML`/`outerHTML` assignment, `insertAdjacentHTML`-style parsers,
  `document.write` and devtools imports outside dev-only modules.

## Information architecture

```
Dashboard                 /                      health, counts, recent changes, open findings/alerts, sources
Inventory                 /inventory             virtualized table, URL-encoded filters, saved views, bulk, CSV
  Entity                  /entities/:id          overview · compare (declared vs observed) · provenance · conflicts ·
                                                 relations · operational (telemetry) · history · audit · sources
Map                       /map                   overview / neighbourhood / path / impact; options drawer; legend
Relationships             /relations             relation table with evidence
Findings                  /findings              rule results, acknowledge/suppress
Alerts                    /alerts                alerts · rules · silences
Versions                  /versions              newest release / end-of-life status per versioned entity, re-check
Sources                   /sources               list, schema-driven create/edit, test, preview, sync, runs, tokens
Kinds                     /kinds                 kind list, schema editor; /relationship-types
Icons                     /settings/icons        custom SVG icons (custom:<name>), monochrome ones tinted per theme
Settings                  /settings              projects (members, tokens), bridges, users, account (MFA,
                                                 sessions), telemetry (integrations, bindings), backups, audit, about
```

Every page's state that a user might share — filters, sort, selected tab, map focus/mode/depth/layout/options — lives
in typed URL search params (TanStack Router validators and codecs in `src/lib/urlState.ts`), so links and the back button reproduce a
view exactly. Server state is TanStack Query; live updates come from SSE (`/api/v1/stream/*`, `src/api/live.ts`) and
invalidate only the affected query keys. Telemetry SSE patches edge/entity data in place and never triggers a
relayout.

Role-gated actions are hidden for roles that cannot perform them, and the server remains the authority: a 403 shows
the server's reason inline rather than failing silently.

## Design system

- Mantine 8 with a single theme (`src/theme/theme.ts`): neutral grays, one indigo accent (`#4263eb`, the logo
  colour), 4 px radii, Inter for text and JetBrains Mono for identifiers, IDs and code. Light and dark schemes are
  first-class; dark uses its own shade (`primaryShade.dark = 7`) and surface tokens rather than an inverted palette.
- Status vocabulary is fixed and always rendered as icon + label (`StatusBadge`): healthy, degraded, down, unknown,
  stale; severity info/warning/critical. Status colours are never reused for categories.
- Kinds have an icon (Tabler or brand mark from `simple-icons`) and a group accent; relationship types have a colour
  from a fixed categorical order (ADR-011c), overridable per saved view.
- Empty, loading and error states are shared components (`src/components/States.tsx`) and always say what to do next.

## Map interaction model

Modes: **overview** (projects/kinds aggregated into group nodes), **neighbourhood** (focus entity + depth),
**path** (between two entities) and **impact** (downstream of the focus). The server enforces depth and node caps
(`max_nodes` ≤ 2,000) and aggregates into cluster nodes beyond its threshold, so the client never renders more than
the ADR-011 budget.

- Click selects and opens the inspector; double-click or `Enter` expands a node or cluster in place; the arrow keys
  move the selection to the nearest neighbour in that direction, so the map is fully keyboard-operable.
- Layouts: ELK layered (direction configurable), force (d3-force) and radial around the focus, all computed in Web
  Workers. Layered layouts wrap very tall layers (a hub with hundreds of neighbours) into several columns so large
  neighbourhoods stay compact. Above 150 nodes layouts take cheaper paths: ELK uses polyline instead of orthogonal
  routing (the map draws its own edges), and the force layout folds degree-1 nodes onto rings around their neighbour,
  simulates the remaining skeleton from a sector-radial warm start with ticks scaled to its size (and a coarser
  Barnes–Hut theta above 400 skeleton nodes).
  Layout runs only when topology changes, and finished layouts are cached in memory per (saved view, layout options,
  topology, pins), so going back to a graph seen before skips the worker. Dragged nodes are pinned and saved with the
  view.
- **Level of detail**: a map of 200 or more nodes zoomed out below 40 % (back above 45 %) draws each entity as a plain
  block in its kind colour (thick outlines for focus, failed and warning health, a stack for clusters) and all
  relationships as straight lines batched into one SVG layer — no card text, arrowheads, labels, animation or edge hit
  areas. Accessible names, selection, the keyboard model, the inspector and the Table view work as before; the legend
  and the status bar say that the drawing is simplified and what it leaves out.
- **Edge encoding** (ADR-011c): colour by relationship type (or health, freshness, evidence class or source); solid =
  declared, dashed = inferred (hover shows the evidence), dotted = stale, thick double = bridge, faded = unknown. Width
  is log-scaled measured rate, only for fresh measurements.
- **Relationship-type colours**: each type's colour comes from `/relationship-types`; the options drawer overrides it
  per browser and saved view, and installation admins can make a colour the default for everyone ("Make default", or
  the palette button on the Relationship types page). That works for built-in types too: the colour is stored as an
  installation override (the definition stays as Deed ships it), shown as "colour changed", and "Reset to default"
  restores the built-in colour (`PUT`/`DELETE /relationship-types/{name}/color`, [CUSTOM_KINDS.md](CUSTOM_KINDS.md)).
- **Colour by source**: graph edges list the sources asserting them (`sources`, highest precedence first, and
  `primary_source`: the highest layer priority, or the source policy's priority, then the oldest first claim, as in
  ADR-003). In this mode each relationship takes its primary source's colour from a 24-colour palette whose colours all
  have at least 3:1 contrast against the canvas in both themes. Colours are assigned over every source you can see,
  oldest first, each taking the slot its ID hashes to or the next free one, so a source keeps its colour across
  reloads and a new source never recolours an existing one (beyond 24 sources colours repeat). Bundled relationships
  with different primary sources are drawn in a neutral warm grey, edges without a visible source in grey. The legend
  names every source colour on screen with its layer and project, and edge tooltips and the inspector list every
  asserting source (with counts for bundles), so colour is never the only cue. Sources of projects you cannot read
  are never listed, also not on edges to redacted stubs. The overview aggregates sources only in this mode
  (`/graph/overview?sources=true`), since that is a second pass over the catalog's assertions.
- **Traffic overlay**: rates come from `/graph/edge-rates`. Only `measured` edges animate (flow dots, speed ∝ rate,
  labelled "sampled, N-min window"); `inferred` rates are labelled with their note and never animate; `unavailable`
  edges are never animated. All animation stops under `prefers-reduced-motion` or the user toggle.
- The legend is generated from the styles actually on screen. Options (filters, grouping, layout, colour mode,
  routing, labels, animation, node density) persist per saved view.

## Accessibility

- Target: WCAG 2.2 AA. Every route is scanned with axe (`@axe-core/playwright`, tags `wcag2a`, `wcag2aa`, `wcag21a`,
  `wcag21aa`, `wcag22aa`) in both light and dark schemes by `web/e2e/a11y.spec.ts`; serious or critical violations
  fail the run.
- Landmarks and a skip link on every page; one `h1` per page; visible `:focus-visible` rings; target sizes ≥ 24 px; the
  map has a keyboard model (above) and every node/edge is also reachable through the inspector and the relations
  table.
- Information is never colour-only: statuses carry icons and labels, edge evidence classes differ in stroke pattern,
  and the legend names every encoding.
- Motion honours `prefers-reduced-motion`.
- ESLint `jsx-a11y` (strict) runs in CI.

## Keyboard shortcuts

Press `?` anywhere to see them in the app.

| Keys                                  | Action                                                    |
| ------------------------------------- | --------------------------------------------------------- |
| `/`, `⌘/Ctrl K`                       | Search entities and pages                                 |
| `?`                                   | Shortcut help                                             |
| `g d` / `g i` / `g m` / `g r`         | Dashboard / inventory / map / relationships               |
| `g f` / `g a` / `g s` / `g k` / `g p` | Findings / alerts / sources / kinds / projects            |
| Map: arrows                           | Move selection to the nearest neighbour in that direction |
| Map: `Enter`                          | Expand the selected node or cluster                       |
| Map: `Esc`                            | Clear selection, close panels                             |
| Map: `f` / `i` / `l`                  | Fit view / impact of selection / toggle legend            |
| Map: `+` / `−`                        | Zoom                                                      |
| Inventory: `Space`                    | Toggle the focused row's checkbox                         |

## Performance budgets and measurements

Budgets (ADR-011): ≤ 2,000 nodes and 4,000 edges rendered at once; time to first neighbourhood < 500 ms p95 for
typical neighbourhoods; 60 fps pan/zoom with ≤ 500 nodes.

`web/e2e/perf.spec.ts` measures, against the production build served by `deed`: fetch → layout → first rendered frame
(`performance.mark('deed-map-rendered')`, five runs for the seeded neighbourhood) and `requestAnimationFrame` intervals
while panning (a 3 s circular drag that starts on empty canvas) and zooming (wheel notches ~13× in and back out around
the fitted view, twice, ~12 s). With `DEED_E2E_LARGE=2000` it also covers the synthetic "scale" project: a ~500-node
neighbourhood (`dmz-net`, depth 2) and the 2,000-node one (`core-net`, depth 2, the render cap), each in all three
layouts, plus an in-app revisit (to the overview and back with `history.back()`) that is served from the layout cache.
Run it with `DEED_E2E_LARGE=2000 make web-e2e` (or `pnpm exec playwright test perf.spec.ts`); results land in
`web/e2e/.state/perf.json`.

Measured 2026-10-25 on an Apple M1 Max (10 cores, 64 GiB, macOS), Chromium headless via Playwright at 1440×900,
production build served by `deed` from the same machine. Ranges are three runs each (the seeded first-frame range is
fifteen); the "before" columns are the previous build measured with the same spec on the same machine in the same
session.

| Scenario                          | Nodes / edges | Layout  | Fetch → first frame (layout)                           | Pan p50 / p95 (fps)                             | Zoom p95 (fps), longest frame                          | Revisit                        |
| --------------------------------- | ------------- | ------- | ------------------------------------------------------ | ----------------------------------------------- | ------------------------------------------------------ | ------------------------------ |
| Seeded (`api-gateway`, depth 2–3) | 26 / 31       | layered | 176–278 ms (110–117 ms); before 199–241 (133–138)      | 16.7 / 16.8 ms (60)                             | 16.8 ms (60)                                           | —                              |
| Medium (`dmz-net`, depth 2)       | 502 / 510     | layered | 330–341 ms (233–241); before 509–515 (305–307)         | 16.7 / 16.8 (60); before 16.7 / 16.8 (60)       | 16.8 (58), 133 ms; before 16.8 (59), 167 ms            | 70–72 ms; before 323–338       |
| same                              | 502 / 510     | force   | 119–144 ms (16–18); before 637–649 (397–404)           | 16.7 / 16.8 (60); before 16.7 / 16.8 (60)       | 16.8 (59), 100–117 ms; before 16.8 (59), 150–167 ms    | 72–73 ms; before 550–556       |
| same                              | 502 / 510     | radial  | 127–142 ms (2); before 346–380 (2)                     | 16.7 / 16.8 (60); before 16.7 / 16.8 (60)       | 16.8 (59), 83–100 ms; before 16.8 (60), 17 ms          | —                              |
| Large (`core-net`, depth 2)       | 2,000 / 2,008 | layered | 591–601 ms (346–363); before 1,898–1,907 (1,233–1,239) | 16.7 / 16.8 (60); before 33 / 83 (28–29)        | 16.8 (54–55), 500–517 ms; before 50 (43), 667–733 ms   | 220–230 ms; before 1,644–1,669 |
| same                              | 2,000 / 2,008 | force   | 303–309 ms (53–57); before 1,985–2,047 (1,267–1,278)   | 16.7 / 16.8 (59–60); before 67 / 84–100 (18–19) | 16.8 (56), 383–400 ms; before 83 (28–29), 750–800 ms   | 226–244 ms; before 1,904–1,952 |
| same                              | 2,000 / 2,008 | radial  | 208–270 ms (5); before 707–1,149 (5–6)                 | 16.7 / 16.8 (60); before 16.7 / 16.8–33 (57–59) | 16.8 (55–56), 333–350 ms; before 16.8 (57), 567–767 ms | —                              |

Before this round (first measurement, same machine, pan only, the drag starting at the canvas centre): 2,000 nodes
layered 1,719 ms to first frame (layout 1,019 ms), pan p95 83 ms (31 fps); force 1,974 ms (layout 1,244 ms), 100 ms
(16 fps); radial 1,052 ms, 59 fps. The ~500-node case had not been measured.

What changed: the level-of-detail drawing (see the map interaction model above; every fitted large and medium view
above is drawn as blocks), cards built from plain elements instead of Mantine layout components, ELK polyline routing
and leaf-folding force layouts above 150 nodes, the ELK worker started while the data loads, and the layout cache.
Panning was slow because React Flow re-runs a store selector per mounted node and edge on every viewport change; at a
fitted 2,000-node view no edge components are mounted any more.

Budgets, plainly:

- **≤ 2,000 nodes / 4,000 edges rendered at once — met** (enforced by the server cap and `enforceBudget`).
- **First neighbourhood < 500 ms p95 for typical neighbourhoods — met**: the seeded 26-node neighbourhood takes
  176–278 ms. The ~500-node neighbourhood is also inside 500 ms in every layout (119–341 ms). The 2,000-node one is
  inside it for force and radial (≤ 309 ms) but **not for layered (591–601 ms)**, where ELK alone takes ~350 ms in a
  freshly started worker.
- **60 fps pan/zoom with ≤ 500 nodes — met for panning** (p95 16.8 ms, 60 fps, all layouts) **and for steady
  zooming** (p95 16.8 ms), **with one caveat**: crossing the level-of-detail threshold (zooming in past 45 % or out
  past 40 %) swaps the drawing and costs one long frame of 83–133 ms at ~500 nodes, so the zoom run averages 58–59 fps.
  At 2,000 nodes (beyond the 500-node budget) panning a fitted view is now 59–60 fps; the threshold crossing costs
  333–517 ms once per crossing (it mounts the cards and edges in view; the layered drawing has ~1,200 long edges in
  view at that zoom), so the zoom run averages 54–56 fps.

Known gaps: the ADR's "10k/50k dataset" first-neighbourhood figure has not been measured in the browser; the radial
layout of 2,000 nodes is larger than the minimum zoom (5 %) can fit, so its fitted view shows only part of the
drawing; and the threshold-crossing frame above is the remaining cost to remove (e.g. by mounting cards and edges over
several frames).

## Testing

- **Unit** (Vitest): API session/CSRF/reauth handling, SSE reconnect and invalidation mapping, URL codecs, schema
  forms, edge styling, map model and layout helpers, provenance rendering.
- **End-to-end** (Playwright, Chromium): `e2e/global-setup.ts` starts a fresh `deed` with a temporary data dir and
  mock upstreams (Prometheus, GitHub API), seeds projects, kinds, sources, entities, relations, telemetry and
  findings (the owner account is enrolled in TOTP during setup), then runs: auth (password + TOTP sign-in with
  return-to, setup-link and SSO error pages), core flows (dashboard → finding → inventory → entity → focused map →
  path; impact; bulk annotations; relations table), map (filters, grouping, inspector, keyboard, provenance, overview
  expansion, traffic overlay on measured edges only), admin (GitHub source create/test/preview against the mock,
  push-token reveal-once, custom kind with schema dry run, audit hash-chain verification, saved views, version
  re-check, custom icon upload, findings re-evaluation), an axe scan of 27 views in both schemes plus the login page
  (screenshots to `web/e2e/screenshots/`), and the perf budgets.
