/**
 * Seeds a Deed installation with realistic, clearly synthetic data for development, screenshots and Playwright.
 *
 *   node e2e/seed.ts --url http://127.0.0.1:8787 --setup-link 'http://…/setup#bootstrap=deed_setup_…' \
 *     [--state e2e/.state/owner.json] [--large 2000]
 *
 * Everything goes through the public API (api/openapi.json): the first owner is created from the bootstrap link and
 * enrolls TOTP (codes are computed here), then projects (marked synthetic), a custom kind, declarations, relationships,
 * a bridge, two push sources with tokens pushing discovered items and observations, and an extra user. With
 * --large N a separate synthetic "scale" project receives about N entities for map budget measurements.
 */
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname } from 'node:path'
import { DeedClient } from './client.ts'
import { TotpClock } from './totp.ts'

export interface OwnerState {
  baseURL: string
  email: string
  password: string
  totpSecret: string
  lastStep: number
  recoveryCodes: string[]
}

export const OWNER = { email: 'ada@deed.test', password: 'correct horse battery staple', name: 'Ada Lovelace' }

interface Me {
  csrf_token?: string
  must_enroll: boolean
  mfa_pending: boolean
}

/** Consumes the bootstrap link: creates the owner, enrolls TOTP and returns a fully signed-in client. */
export async function bootstrapOwner(
  baseURL: string,
  setupLink: string,
): Promise<{ client: DeedClient; state: OwnerState }> {
  const token = new URL(setupLink).hash.replace(/^#bootstrap=/, '')
  const c = new DeedClient(baseURL)
  await c.post('/auth/setup/inspect', { token })
  const me = await c.post<Me>('/auth/setup/complete', {
    token,
    email: OWNER.email,
    display_name: OWNER.name,
    password: OWNER.password,
  })
  if (!me.must_enroll) throw new Error('expected the owner to enroll TOTP')
  const enr = await c.post<{ secret: string; otpauth_uri: string }>('/me/mfa/totp')
  const clock = new TotpClock(enr.secret)
  const res = await c.post<{ recovery_codes: string[] }>('/me/mfa/totp/confirm', { code: await clock.next() })
  await c.me()
  return {
    client: c,
    state: {
      baseURL,
      email: OWNER.email,
      password: OWNER.password,
      totpSecret: enr.secret,
      lastStep: clock.last,
      recoveryCodes: res.recovery_codes,
    },
  }
}

/** Password + TOTP sign-in; updates state.lastStep so later sign-ins never reuse a code. */
export async function signIn(state: OwnerState): Promise<DeedClient> {
  const c = new DeedClient(state.baseURL)
  const me = await c.post<Me>('/auth/login', { email: state.email, password: state.password })
  if (me.mfa_pending) {
    const clock = new TotpClock(state.totpSecret, state.lastStep)
    await c.post('/auth/mfa', { code: await clock.next() })
    state.lastStep = clock.last
  }
  await c.me()
  return c
}

const days = (n: number) => new Date(Date.now() + n * 86_400_000).toISOString()

interface Declared {
  entity_id: string
}

type Meta = Record<string, unknown>

export interface SeedResult {
  projects: Record<string, string>
  entities: Record<string, string>
  sources: Record<string, { id: string; token: string }>
}

/** Declares the catalog. Idempotent enough for development: re-running against a seeded server fails fast. */
export async function seedCatalog(
  c: DeedClient,
  opts: { large?: number; prometheusURL?: string } = {},
): Promise<SeedResult> {
  const out: SeedResult = { projects: {}, entities: {}, sources: {} }

  for (const p of [
    { name: 'home', display_name: 'Home infrastructure', description: 'Self-hosted services, network and storage.' },
    { name: 'lab', display_name: 'Lab', description: 'Experiments and staging workloads.' },
  ]) {
    const v = await c.post<{ id: string }>('/projects', { ...p, synthetic: true })
    out.projects[p.name] = v.id
  }

  await c.post('/kinds', {
    name: 'backup_job',
    title: 'Backup job',
    plural: 'Backup jobs',
    group: 'ops',
    icon: 'tabler:database-export',
    color: '#7048e8',
    description: 'A scheduled backup of a data store.',
    schema: {
      $schema: 'https://json-schema.org/draft/2020-12/schema',
      type: 'object',
      required: ['schedule'],
      properties: {
        schedule: { type: 'string', description: 'Cron expression' },
        target: { type: 'string' },
        retention_days: { type: 'integer', minimum: 1 },
        encrypted: { type: 'boolean' },
      },
      additionalProperties: false,
    },
  })

  const declare = async (project: string, kind: string, name: string, metadata: Meta = {}, spec: Meta = {}) => {
    const d = await c.post<Declared>('/entities', { project, kind, name, metadata, spec, source: 'api' })
    out.entities[`${kind}/${name}`] = d.entity_id
    return d.entity_id
  }
  const relate = async (type: string, from: string, to: string, attributes: Meta = {}) => {
    const f = out.entities[from]
    const t = out.entities[to]
    if (!f || !t) throw new Error(`unknown entity in relation ${from} -${type}-> ${to}`)
    await c.post('/relations', { type, from: { id: f }, to: { id: t }, source: 'api', attributes })
  }

  const links = (repo?: string, dash?: string, runbook?: string) => [
    ...(repo ? [{ type: 'repository', title: 'Source', url: repo }] : []),
    ...(dash ? [{ type: 'dashboard', title: 'Grafana', url: dash }] : []),
    ...(runbook ? [{ type: 'runbook', title: 'Runbook', url: runbook }] : []),
  ]

  // ---- home: owners, hosts, workloads ----
  await declare('home', 'group', 'platform-team', {
    displayName: 'Platform team',
    description: 'Runs the shared platform.',
  })
  await declare('home', 'group', 'home-automation', { displayName: 'Home automation', description: 'Smart home.' })
  await declare('home', 'location', 'hel1-rack', { displayName: 'Helsinki rack', environment: 'production' })

  const core = { environment: 'production', owner: 'platform-team', lifecycle: 'active' }
  await declare(
    'home',
    'server',
    'srv-hel1-01',
    {
      ...core,
      displayName: 'srv-hel1-01',
      description: 'Primary dedicated server (Hetzner AX52).',
      labels: { tier: 'core', 'deed.dev/synthetic': 'true' },
      links: links(
        undefined,
        'https://grafana.example.com/d/node?var-host=srv-hel1-01',
        'https://docs.example.com/runbooks/servers',
      ),
    },
    {
      hostname: 'srv-hel1-01',
      ipv4: '10.10.0.11',
      provider: 'hetzner',
      cpu_cores: 16,
      memory_mb: 65536,
      os: 'Debian 12',
      region: 'hel1',
    },
  )
  await declare(
    'home',
    'server',
    'srv-hel1-02',
    {
      ...core,
      description: 'Secondary dedicated server.',
      labels: { tier: 'core', 'deed.dev/synthetic': 'true' },
    },
    {
      hostname: 'srv-hel1-02',
      ipv4: '10.10.0.12',
      provider: 'hetzner',
      cpu_cores: 8,
      memory_mb: 32768,
      os: 'Debian 12',
      region: 'hel1',
    },
  )
  await declare(
    'home',
    'hypervisor',
    'pve-01',
    { ...core, description: 'Proxmox VE host.', icon: 'simple-icons:proxmox', labels: { tier: 'core' } },
    {
      hostname: 'pve-01',
      ipv4: '10.10.0.20',
    },
  )
  await declare(
    'home',
    'storage',
    'nas-01',
    { ...core, owner: 'home-automation', description: 'Synology NAS for backups and media.' },
    {},
  )
  for (const vm of ['vm-k3s-1', 'vm-k3s-2', 'vm-db-1']) {
    await declare(
      'home',
      'vm',
      vm,
      { ...core, labels: { tier: vm.includes('db') ? 'data' : 'compute' } },
      { hostname: vm },
    )
  }
  await declare('home', 'docker_host', 'docker-01', { ...core, description: 'Docker engine for self-hosted apps.' }, {})

  const svc = async (name: string, m: Meta, repo?: string) =>
    declare('home', 'service', name, {
      ...core,
      ...m,
      links: links(
        repo,
        `https://grafana.example.com/d/svc?var-service=${name}`,
        `https://docs.example.com/runbooks/${name}`,
      ),
    })
  await svc('api-gateway', {
    displayName: 'API gateway',
    description: 'Traefik ingress for every public service.',
    labels: { tier: 'edge' },
    version: { declared: '3.1.2', product: 'traefik' },
  })
  await svc(
    'billing-api',
    {
      displayName: 'Billing API',
      description: 'Invoices and payments.',
      labels: { tier: 'app', team: 'billing' },
      version: { declared: '2.4.0' },
    },
    'https://github.com/example/billing-api',
  )
  await svc(
    'notifications-worker',
    {
      displayName: 'Notifications worker',
      description: 'Sends email and push notifications from events.',
      labels: { tier: 'app' },
    },
    'https://github.com/example/notifications',
  )
  await svc('auth', {
    displayName: 'Auth (Porch)',
    description: 'Single sign-on for internal apps.',
    labels: { tier: 'app' },
    version: { declared: '1.8.0' },
  })
  await svc('home-assistant', {
    displayName: 'Home Assistant',
    owner: 'home-automation',
    icon: 'simple-icons:homeassistant',
    description: 'Smart home hub.',
  })
  await svc('git', { displayName: 'Gitea', icon: 'simple-icons:gitea', description: 'Self-hosted Git.' })
  await svc('legacy-reports', {
    displayName: 'Legacy reports',
    lifecycle: 'deprecated',
    owner: '',
    description: 'Old reporting job; scheduled for removal.',
  })

  await declare(
    'home',
    'database',
    'postgres-main',
    { ...core, displayName: 'PostgreSQL (main)', icon: 'simple-icons:postgresql', version: { declared: '16.4' } },
    {},
  )
  await declare('home', 'database', 'redis-cache', { ...core, displayName: 'Redis cache' }, {})

  await declare(
    'home',
    'domain',
    'example-home.net',
    { ...core, description: 'Public domain.' },
    { expires_at: days(20), auto_renew: false },
  )
  await declare('home', 'dns_zone', 'example-home.net', { ...core }, {})
  for (const r of ['grafana', 'api', 'git']) {
    await declare('home', 'dns_record', `${r}.example-home.net`, { ...core }, { type: 'A', value: '203.0.113.10' })
  }
  await declare(
    'home',
    'certificate',
    'grafana.example-home.net',
    { ...core },
    { not_after: days(5), issuer: "Let's Encrypt" },
  )
  await declare(
    'home',
    'certificate',
    'api.example-home.net',
    { ...core },
    { not_after: days(24), issuer: "Let's Encrypt" },
  )
  await declare(
    'home',
    'certificate',
    'git.example-home.net',
    { ...core },
    { not_after: days(80), issuer: "Let's Encrypt" },
  )

  await declare(
    'home',
    'repository',
    'infra-terraform',
    { ...core, links: links('https://github.com/example/infra-terraform') },
    {},
  )
  await declare(
    'home',
    'repository',
    'billing-api',
    { ...core, labels: { team: 'billing' }, links: links('https://github.com/example/billing-api') },
    {},
  )

  await declare('home', 'nats_system', 'nats-main', { ...core, displayName: 'NATS (main)' }, {})
  await declare('home', 'nats_stream', 'orders', { ...core, displayName: 'ORDERS' }, {})
  for (const s of ['orders.created', 'orders.paid', 'billing.invoice.issued']) {
    await declare('home', 'nats_subject', s, { ...core }, {})
  }
  await declare('home', 'event_contract', 'order-created-v1', { ...core, displayName: 'OrderCreated v1' }, {})
  await declare('home', 'event_contract', 'invoice-issued-v1', { ...core, displayName: 'InvoiceIssued v1' }, {})
  await declare('home', 'grafana_dashboard', 'platform-overview', { ...core, displayName: 'Platform overview' }, {})
  await declare(
    'home',
    'backup_job',
    'nightly-postgres',
    { ...core, description: 'Nightly dump of the main database.' },
    {
      schedule: '0 3 * * *',
      target: 'nas-01',
      retention_days: 14,
      encrypted: true,
    },
  )

  // Relationships (declared through the API source).
  await relate('located_in', 'server/srv-hel1-01', 'location/hel1-rack')
  await relate('located_in', 'server/srv-hel1-02', 'location/hel1-rack')
  await relate('runs_on', 'hypervisor/pve-01', 'server/srv-hel1-02')
  for (const vm of ['vm-k3s-1', 'vm-k3s-2', 'vm-db-1']) await relate('runs_on', `vm/${vm}`, 'hypervisor/pve-01')
  await relate('runs_on', 'docker_host/docker-01', 'server/srv-hel1-01')
  await relate('runs_on', 'database/postgres-main', 'vm/vm-db-1')
  await relate('runs_on', 'database/redis-cache', 'vm/vm-k3s-2')
  for (const s of ['billing-api', 'notifications-worker', 'auth'])
    await relate('runs_on', `service/${s}`, 'vm/vm-k3s-1')
  await relate('runs_on', 'service/home-assistant', 'docker_host/docker-01')
  await relate('runs_on', 'service/git', 'docker_host/docker-01')
  await relate('runs_on', 'service/api-gateway', 'docker_host/docker-01')
  await relate('runs_on', 'service/legacy-reports', 'vm/vm-k3s-2')
  await relate('routes_to', 'service/api-gateway', 'service/billing-api')
  await relate('routes_to', 'service/api-gateway', 'service/auth')
  await relate('routes_to', 'service/api-gateway', 'service/git')
  await relate('depends_on', 'service/billing-api', 'database/postgres-main')
  await relate('depends_on', 'service/billing-api', 'database/redis-cache')
  await relate('depends_on', 'service/auth', 'database/postgres-main')
  await relate('depends_on', 'service/legacy-reports', 'database/postgres-main')
  await relate('authenticates_with', 'service/billing-api', 'service/auth')
  await relate('authenticates_with', 'service/git', 'service/auth')
  await relate('publishes', 'service/billing-api', 'nats_subject/orders.paid')
  await relate('publishes', 'service/billing-api', 'nats_subject/billing.invoice.issued')
  await relate('subscribes_to', 'service/notifications-worker', 'nats_subject/orders.created')
  await relate('subscribes_to', 'service/notifications-worker', 'nats_subject/billing.invoice.issued')
  await relate('contains', 'nats_system/nats-main', 'nats_stream/orders')
  await relate('contains', 'nats_stream/orders', 'nats_subject/orders.created')
  await relate('contains', 'nats_stream/orders', 'nats_subject/orders.paid')
  await relate('carried_on', 'event_contract/order-created-v1', 'nats_subject/orders.created')
  await relate('carried_on', 'event_contract/invoice-issued-v1', 'nats_subject/billing.invoice.issued')
  await relate('deployed_from', 'service/billing-api', 'repository/billing-api')
  await relate('resolves_to', 'dns_record/grafana.example-home.net', 'server/srv-hel1-01')
  await relate('resolves_to', 'dns_record/api.example-home.net', 'server/srv-hel1-01')
  await relate('resolves_to', 'dns_record/git.example-home.net', 'server/srv-hel1-01')
  await relate('contains', 'dns_zone/example-home.net', 'dns_record/grafana.example-home.net')
  await relate('contains', 'dns_zone/example-home.net', 'dns_record/api.example-home.net')
  await relate('contains', 'dns_zone/example-home.net', 'dns_record/git.example-home.net')
  await relate('secured_by_certificate', 'service/api-gateway', 'certificate/api.example-home.net')
  await relate('secured_by_certificate', 'service/git', 'certificate/git.example-home.net')
  await relate('secured_by_certificate', 'dns_record/grafana.example-home.net', 'certificate/grafana.example-home.net')
  await relate('monitored_by', 'service/billing-api', 'grafana_dashboard/platform-overview')
  await relate('monitored_by', 'service/api-gateway', 'grafana_dashboard/platform-overview')
  await relate('stored_in', 'backup_job/nightly-postgres', 'storage/nas-01')
  await relate('depends_on', 'backup_job/nightly-postgres', 'database/postgres-main')
  await relate('owned_by', 'service/billing-api', 'group/platform-team')

  // ---- lab ----
  const lab = { environment: 'staging', owner: 'platform-team', lifecycle: 'active' }
  await declare('lab', 'hypervisor', 'lab-pve', { ...lab, icon: 'simple-icons:proxmox' }, { hostname: 'lab-pve' })
  for (const vm of ['lab-vm-1', 'lab-vm-2']) {
    await declare('lab', 'vm', vm, lab, { hostname: vm })
    await relate('runs_on', `vm/${vm}`, 'hypervisor/lab-pve')
  }
  await declare('lab', 'service', 'experiment-api', {
    ...lab,
    displayName: 'Experiment API',
    description: 'Feature experiments; calls production billing.',
  })
  await declare('lab', 'service', 'load-generator', { ...lab, owner: '' })
  await relate('runs_on', 'service/experiment-api', 'vm/lab-vm-1')
  await relate('runs_on', 'service/load-generator', 'vm/lab-vm-2')
  await relate('connects_to', 'service/load-generator', 'service/experiment-api')
  // A bridge lets lab declare dependencies on home (cross-project edges).
  await c.post('/bridges', {
    from_project: 'lab',
    to_project: 'home',
    description: 'Staging may call production APIs.',
    allowed_types: ['connects_to', 'depends_on'],
  })
  await relate('depends_on', 'service/experiment-api', 'service/billing-api')

  // ---- push sources: runtime discovery with tokens ----
  const docker = await c.post<{ id: string }>('/sources', {
    project: 'home',
    provider: 'push',
    name: 'docker-agent',
    display_name: 'Docker agent (docker-01)',
    config: { notes: 'deed-agent docker collector on docker-01' },
    schedule_seconds: 0,
    expected_interval_seconds: 900,
    policy: { on_absent: 'tombstone', max_removal_percent: 30, default_ttl_seconds: 3600 },
  })
  const dockerTok = await c.post<{ token: string }>(`/sources/${docker.id}/tokens`, {
    name: 'docker-01 agent',
    expires_in_days: 90,
  })
  out.sources['docker-agent'] = { id: docker.id, token: dockerTok.token }
  const agent = c.withBearer(dockerTok.token)
  const containers = [
    ['traefik', 'traefik:v3.1.2', 'ok'],
    ['grafana', 'grafana/grafana:11.2.0', 'ok'],
    ['prometheus', 'prom/prometheus:v2.54.1', 'ok'],
    ['home-assistant', 'ghcr.io/home-assistant/home-assistant:2024.9', 'warning'],
    ['gitea', 'gitea/gitea:1.22.2', 'ok'],
    ['vaultwarden', 'vaultwarden/server:1.32.0', 'ok'],
    ['mosquitto', 'eclipse-mosquitto:2.0.18', 'failed'],
    ['nats', 'nats:2.10.20', 'ok'],
  ] as const
  await agent.post(`/sources/${docker.id}/items`, {
    entities: containers.map(([name, image, health], i) => ({
      kind: 'container',
      name,
      external_id: `docker://docker-01/${name}`,
      revision: 1,
      metadata: { environment: 'production', labels: { 'com.docker.compose.project': 'apps' } },
      spec: { image, state: health === 'failed' ? 'restarting' : 'running', host: 'docker-01' },
      health: {
        state: health,
        ...(health !== 'ok'
          ? { reason: health === 'failed' ? 'restart loop (exit 1)' : 'slow health check (4.1 s)' }
          : {}),
      },
      observed_at: new Date(Date.now() - i * 60_000).toISOString(),
    })),
    relations: [
      ...containers.map(([name]) => ({
        type: 'runs_on',
        from: { kind: 'container', name },
        to: { kind: 'docker_host', name: 'docker-01' },
        evidence_class: 'structural',
        revision: 1,
      })),
      {
        type: 'connects_to',
        from: { kind: 'container', name: 'grafana' },
        to: { kind: 'container', name: 'prometheus' },
        evidence_class: 'observed',
        revision: 1,
      },
      {
        type: 'connects_to',
        from: { kind: 'container', name: 'traefik' },
        to: { kind: 'container', name: 'gitea' },
        evidence_class: 'observed',
        revision: 1,
      },
      {
        type: 'connects_to',
        from: { kind: 'container', name: 'home-assistant' },
        to: { kind: 'container', name: 'mosquitto' },
        evidence_class: 'inferred',
        inferred: true,
        confidence: 0.7,
        evidence: { reason: 'Home Assistant config references mqtt://mosquitto:1883', detector: 'config-scan' },
        revision: 1,
      },
      {
        type: 'connects_to',
        from: { kind: 'container', name: 'gitea' },
        to: { kind: 'database', name: 'postgres-main' },
        evidence_class: 'observed',
        observed_at: days(-9),
        revision: 1,
      },
      // References an entity nobody declared: shows up as an unresolved relationship.
      {
        type: 'depends_on',
        from: { kind: 'container', name: 'vaultwarden' },
        to: { kind: 'service', name: 'legacy-ldap' },
        evidence_class: 'observed',
        revision: 1,
      },
    ],
  })
  // The agent also reports the running version of the billing API (declared 2.4.0 → drift).
  await agent.post(`/sources/${docker.id}/items`, {
    entities: [
      {
        kind: 'service',
        name: 'billing-api',
        external_id: 'docker://docker-01/billing-api',
        revision: 2,
        metadata: { version: { observed: '2.3.1' } },
        spec: { replicas: 2 },
        health: { state: 'ok' },
      },
    ],
  })
  await agent.post(`/sources/${docker.id}/heartbeat`)

  const scanner = await c.post<{ id: string }>('/sources', {
    project: 'home',
    provider: 'push',
    name: 'network-scanner',
    display_name: 'Network scanner',
    config: { notes: 'nmap sweep of 10.10.0.0/24' },
    schedule_seconds: 0,
    expected_interval_seconds: 3600,
    policy: { priority: 50, on_absent: 'keep' },
  })
  const scanTok = await c.post<{ token: string }>(`/sources/${scanner.id}/tokens`, {
    name: 'scanner',
    expires_in_days: 30,
  })
  out.sources['network-scanner'] = { id: scanner.id, token: scanTok.token }
  const scan = c.withBearer(scanTok.token)
  // Two discovered sources disagree about srv-hel1-02's address: a conflict with provenance.
  await scan.post(`/sources/${scanner.id}/items`, {
    entities: [
      {
        kind: 'server',
        name: 'srv-hel1-02',
        revision: 1,
        spec: { ipv4: '10.10.0.12', mac: '52:54:00:12:34:56', open_ports: [22, 443] },
      },
      { kind: 'server', name: 'srv-hel1-01', revision: 1, spec: { ipv4: '10.10.0.11', open_ports: [22, 80, 443] } },
    ],
  })
  await agent.post(`/sources/${docker.id}/items`, {
    entities: [
      { kind: 'server', name: 'srv-hel1-02', revision: 3, spec: { ipv4: '10.10.0.112', mac: '52:54:00:ab:cd:ef' } },
    ],
  })

  // Runtime observations (time-bounded) come from an observation-layer source: health and observed values.
  const ids = out.entities
  const metrics = await c.post<{ id: string }>('/sources', {
    project: 'home',
    provider: 'push',
    name: 'metrics-summaries',
    display_name: 'Metrics summaries',
    layer: 'observation',
    config: { notes: 'Prometheus recording rules summarised every 5 minutes' },
    schedule_seconds: 0,
    expected_interval_seconds: 300,
  })
  const metricsTok = await c.post<{ token: string }>(`/sources/${metrics.id}/tokens`, {
    name: 'prometheus bridge',
    expires_in_days: 30,
  })
  out.sources['metrics-summaries'] = { id: metrics.id, token: metricsTok.token }
  await c.withBearer(metricsTok.token).post(`/sources/${metrics.id}/observations`, {
    observations: [
      {
        ref: { id: ids['service/api-gateway'] },
        observed: { requests_per_min: 1240, p95_ms: 42 },
        health: { state: 'ok' },
        expires_at: days(1),
      },
      {
        ref: { id: ids['service/billing-api'] },
        observed: { requests_per_min: 310, error_rate: 0.004 },
        health: { state: 'ok' },
        expires_at: days(1),
      },
      {
        ref: { id: ids['service/notifications-worker'] },
        observed: { queue_depth: 1820 },
        health: { state: 'warning', reason: 'consumer lag 1,820 messages' },
        expires_at: days(1),
      },
      {
        ref: { id: ids['database/postgres-main'] },
        observed: { connections: 87, replication_lag_s: 0 },
        health: { state: 'ok' },
        expires_at: days(1),
      },
      {
        ref: { id: ids['service/legacy-reports'] },
        observed: { last_run: days(-3) },
        health: { state: 'failed', reason: 'last 3 runs failed' },
        expires_at: days(1),
      },
    ],
  })

  // A viewer for permission screenshots.
  try {
    const u = await c.post<{ id: string }>('/users', {
      email: 'viewer@deed.test',
      display_name: 'Victor Viewer',
      password: 'viewer password 123',
    })
    await c.put(`/projects/lab/members/${u.id}`, { role: 'viewer' })
  } catch (e) {
    console.warn('extra user not created:', (e as Error).message)
  }

  if (opts.prometheusURL) await seedTelemetry(c, opts.prometheusURL)
  if (opts.large && opts.large > 0) await seedLarge(c, opts.large, out)
  return out
}

/** About n entities in a synthetic "scale" project: hosts with containers and a mesh of service connections. */
/**
 * Telemetry: a Prometheus integration (the e2e mock, e2e/mock-prometheus.ts) and bindings — request rate, latency
 * series and an error-rate health check for services, plus an edge-rate binding for routes_to and connects_to so the
 * map's traffic overlay has measured rates.
 */
export async function seedTelemetry(c: DeedClient, prometheusURL: string): Promise<void> {
  await c.post('/integrations', {
    name: 'metrics',
    adapter: 'prometheus',
    enabled: true,
    config: { url: prometheusURL, insecure_http: true, allow_cidrs: ['127.0.0.0/8'] },
  })
  const svc = [{ name: 'svc', type: 'label', from: '/metadata/name' }]
  const bindings = [
    {
      name: 'http-rps',
      title: 'Requests per second',
      kinds: ['service', 'container'],
      query: 'sum(rate(http_requests_total{service="{{svc}}"}[{{window}}]))',
      params: svc,
      shape: 'scalar',
      unit: 'req/s',
      role: 'chart',
      freshness_seconds: 30,
    },
    {
      name: 'http-latency',
      title: 'p95 latency',
      kinds: ['service'],
      query:
        'histogram_quantile(0.95, sum by (le) (rate(http_request_duration_seconds_bucket{service="{{svc}}"}[5m]))) * 1000',
      params: svc,
      shape: 'timeseries',
      unit: 'ms',
      role: 'chart',
      freshness_seconds: 60,
    },
    {
      name: 'http-errors',
      title: 'Error ratio',
      kinds: ['service'],
      query:
        'sum(rate(http_requests_total{service="{{svc}}",code=~"5.."}[{{window}}])) / sum(rate(http_requests_total{service="{{svc}}"}[{{window}}]))',
      params: svc,
      shape: 'scalar',
      unit: 'ratio',
      role: 'health',
      thresholds: { warning: 0.01, critical: 0.05, direction: 'above' },
      freshness_seconds: 60,
    },
  ]
  for (const b of bindings) await c.post('/bindings', { ...b, integration: 'metrics' })
  for (const type of ['routes_to', 'connects_to']) {
    await c.post('/bindings', {
      name: `${type.replace('_', '-')}-rate`,
      title: `${type} request rate`,
      integration: 'metrics',
      role: 'edge_rate',
      relationship_type: type,
      query: 'sum(rate(http_requests_total{source="{{src}}",destination="{{dst}}"}[{{window}}]))',
      params: [
        { name: 'src', type: 'label', from: 'from:/metadata/name' },
        { name: 'dst', type: 'label', from: 'to:/metadata/name' },
      ],
      shape: 'scalar',
      unit: 'req/s',
      freshness_seconds: 30,
    })
  }
}

export async function seedLarge(c: DeedClient, n: number, out: SeedResult): Promise<void> {
  const p = await c.post<{ id: string }>('/projects', {
    name: 'scale',
    display_name: 'Scale test',
    description: 'Generated topology for map budget measurements.',
    synthetic: true,
  })
  out.projects.scale = p.id
  const src = await c.post<{ id: string }>('/sources', {
    project: 'scale',
    provider: 'push',
    name: 'generator',
    display_name: 'Topology generator',
    schedule_seconds: 0,
    expected_interval_seconds: 86400,
  })
  const tok = await c.post<{ token: string }>(`/sources/${src.id}/tokens`, { name: 'generator', expires_in_days: 7 })
  const g = c.withBearer(tok.token)
  const hosts = Math.max(1, Math.round(n / 50))
  const perHost = Math.max(1, Math.floor((n - hosts) / hosts))
  const envs = ['production', 'staging', 'development']
  let batch: { entities: unknown[]; relations: unknown[] } = { entities: [], relations: [] }
  const flush = async () => {
    if (batch.entities.length === 0 && batch.relations.length === 0) return
    await g.post(`/sources/${src.id}/items`, batch)
    batch = { entities: [], relations: [] }
  }
  for (let h = 0; h < hosts; h++) {
    const host = `host-${String(h).padStart(3, '0')}`
    batch.entities.push({
      kind: 'docker_host',
      name: host,
      revision: 1,
      metadata: { environment: envs[h % 3] },
      health: { state: 'ok' },
    })
    for (let i = 0; i < perHost; i++) {
      const name = `${host}-app-${String(i).padStart(2, '0')}`
      const health = (h * perHost + i) % 37 === 0 ? 'failed' : (h * perHost + i) % 11 === 0 ? 'warning' : 'ok'
      batch.entities.push({
        kind: 'container',
        name,
        revision: 1,
        metadata: { environment: envs[h % 3] },
        health: { state: health },
      })
      batch.relations.push({
        type: 'runs_on',
        from: { kind: 'container', name },
        to: { kind: 'docker_host', name: host },
        evidence_class: 'structural',
        revision: 1,
      })
      if (i > 0 && i % 3 === 0) {
        batch.relations.push({
          type: 'connects_to',
          from: { kind: 'container', name },
          to: {
            kind: 'container',
            name: `host-${String((h + 1) % hosts).padStart(3, '0')}-app-${String(i - 1).padStart(2, '0')}`,
          },
          evidence_class: 'observed',
          revision: 1,
        })
      }
    }
    if (batch.entities.length >= 400) await flush()
  }
  await flush()
  // Second pass: cross-host connections whose targets now exist.
  await flush()
}

async function main() {
  const args = process.argv.slice(2)
  const arg = (name: string) => {
    const i = args.indexOf(`--${name}`)
    return i >= 0 ? args[i + 1] : undefined
  }
  const url = arg('url') ?? 'http://127.0.0.1:8787'
  const link = arg('setup-link')
  const statePath = arg('state') ?? 'e2e/.state/owner.json'
  const large = Number(arg('large') ?? '0')
  let client: DeedClient
  let state: OwnerState
  if (link) {
    ;({ client, state } = await bootstrapOwner(url, link))
  } else {
    state = JSON.parse(readFileSync(statePath, 'utf8')) as OwnerState
    client = await signIn(state)
  }
  mkdirSync(dirname(statePath), { recursive: true, mode: 0o700 })
  writeFileSync(statePath, JSON.stringify(state, null, 2), { mode: 0o600 })
  const res = args.includes('--large-only')
    ? (await seedLarge(client, large, { projects: {}, entities: {}, sources: {} }), null)
    : await seedCatalog(client, { large })
  writeFileSync(statePath, JSON.stringify({ ...state, seed: res }, null, 2), { mode: 0o600 })
  console.log(`seeded ${url}; owner ${state.email}; state in ${statePath}`)
}

if (process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop() ?? '')) {
  main().catch((e: unknown) => {
    console.error(e)
    process.exit(1)
  })
}
