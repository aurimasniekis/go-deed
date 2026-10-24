# ADR-011: Frontend stack and graph rendering

- Vite 8, React 19, TypeScript, Mantine 8 (radius 4 px, Inter + JetBrains Mono, light/dark via
  `useMantineColorScheme`), TanStack Router (file routes, typed search params = URL state), TanStack Query (server
  state), TanStack Store (graph view state: selection, expansion, styles), TanStack Table + Virtual (inventory),
  ECharts 6 (charts), `@xyflow/react` 12 (map), elkjs in a Web Worker (layered layout), `openapi-fetch` with types
  generated from `api/openapi.json`, Tabler icons. Devtools load only in development builds; `scripts/check-dist.mjs`
  fails the build if devtools code is present in `dist`.
- Built into `internal/webui/dist`, embedded with `go:embed`, served with a per-request CSP nonce.
- **Graph**: the server answers neighborhood/path/impact queries with depth and node caps; above 500 nodes results
  are aggregated into cluster nodes (by project, kind, environment or host). Layout runs only on topology change;
  telemetry updates patch edge data in place. Saved views and layouts are server-side.
- **Budgets**: ≤ 2,000 nodes and 4,000 edges rendered at once; time to first neighborhood < 500 ms p95 on the
  10k/50k dataset (server + render, measured in PERFORMANCE.md); 60 fps pan/zoom target with ≤ 500 nodes.
- Required views: dashboard, inventory, entity detail (overview, declared vs observed, provenance/conflicts,
  relations, operational charts, links, versions, history, audit, sources), sources & integrations, global map,
  findings & alerts, kinds & relationship types, settings (projects, members, tokens, account/MFA, backups, audit).
