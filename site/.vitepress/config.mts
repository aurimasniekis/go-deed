import { cpSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vitepress';

const repo = 'https://github.com/aurimasniekis/go-deed';
const base = '/go-deed/';

// Screenshots live once, in docs/assets/screenshots (the README and docs/UI.md use them from there); the site serves a
// copy from public/screenshots (git-ignored), refreshed on every dev start and build.
cpSync(fileURLToPath(new URL('../../docs/assets/screenshots', import.meta.url)), fileURLToPath(new URL('../public/screenshots', import.meta.url)), {
  recursive: true,
});
const screenshot = /(?:\.\.\/)*(?:docs\/)?assets\/screenshots\//g;

// docs/*.md show screenshots as <picture> with a prefers-color-scheme source, which follows the OS; on the site the
// light and dark images follow the theme toggle instead (theme/custom.css).
function siteHtml(html: string): string {
  return html
    .replace(
      /<picture>\s*<source media="\(prefers-color-scheme: dark\)" srcset="([^"]+)">\s*<img src="([^"]+)" alt="([^"]*)"[^>]*>\s*<\/picture>/g,
      (_m, dark, light, alt) => `<img class="ss-light" src="${light}" alt="${alt}"><img class="ss-dark" src="${dark}" alt="${alt}">`,
    )
    .replace(screenshot, '/screenshots/');
}

// Engineering docs in ../docs mention each other as docs/FOO.md or FOO.md; on the site they are included into pages.
const docPages: Record<string, string> = {
  DEPLOYMENT: '/operators/deployment',
  CLOUDFLARE: '/operators/cloudflare',
  BACKUP: '/operators/backups',
  OPERATIONS: '/operators/operations',
  SSO: '/operators/sso',
  SERVICE_ACCOUNTS: '/operators/service-accounts',
  CONNECTORS: '/guide/connectors',
  TELEMETRY: '/guide/telemetry',
  PULSE: '/guide/pulse',
  TERRAFORM_IMPORT: '/guide/terraform-import',
  CUSTOM_KINDS: '/guide/custom-kinds',
  LABEL_RULES: '/guide/label-rules',
  FINDINGS: '/guide/findings',
  ICONS: '/guide/icons',
  AGENT: '/guide/agent',
  UI: '/guide/ui',
  INGEST_API: '/reference/ingest-api',
  VERSIONING: '/reference/versioning',
  COMPATIBILITY: '/reference/compatibility',
  THREAT_MODEL: '/reference/threat-model',
  VALIDATION: '/reference/validation',
  PERFORMANCE: '/reference/performance',
  TESTING: '/reference/testing',
};

export default defineConfig({
  title: 'Deed',
  description:
    'A source-aware graph of your infrastructure, software, identities and operational relationships — declared with Terraform, imported from state, observed at runtime.',
  lang: 'en-US',
  // GitHub Pages project site: https://aurimasniekis.github.io/go-deed/
  base,
  cleanUrls: true,
  lastUpdated: false,
  // Engineering docs link to source files and to each other with repository-relative paths.
  ignoreDeadLinks: true,
  head: [['link', { rel: 'icon', type: 'image/svg+xml', href: '/go-deed/logo.svg' }]],
  markdown: {
    theme: { light: 'github-light', dark: 'github-dark' },
    config: (md) => {
      const original = md.renderer.rules.link_open!;
      md.renderer.rules.link_open = (tokens, idx, options, env, self) => {
        const href = tokens[idx].attrGet('href');
        const m = href?.match(/^(?:\.\/|\.\.\/)?(?:docs\/)?([A-Z_]+)\.md(#.*)?$/);
        if (m && docPages[m[1]]) {
          tokens[idx].attrSet('href', docPages[m[1]] + (m[2] ?? ''));
        } else if (href && /^(?:\.\.\/)*(internal|cmd|pkg|deploy|scripts|api)\//.test(href)) {
          tokens[idx].attrSet('href', repo + '/blob/main/' + href.replace(/^(\.\.\/)+/, ''));
        }
        return original(tokens, idx, options, env, self);
      };
      for (const rule of ['html_block', 'html_inline'] as const) {
        const render = md.renderer.rules[rule]!;
        md.renderer.rules[rule] = (tokens, idx, options, env, self) => siteHtml(render(tokens, idx, options, env, self));
      }
      const image = md.renderer.rules.image!;
      md.renderer.rules.image = (tokens, idx, options, env, self) => {
        const src = tokens[idx].attrGet('src');
        if (src) tokens[idx].attrSet('src', src.replace(screenshot, '/screenshots/'));
        return image(tokens, idx, options, env, self);
      };
    },
  },
  themeConfig: {
    logo: '/logo.svg',
    nav: [
      { text: 'Guide', link: '/guide/what-is-deed' },
      { text: 'Operators', link: '/operators/deployment' },
      { text: 'Reference', link: '/reference/ingest-api' },
      { text: 'Terraform provider', link: 'https://github.com/aurimasniekis/terraform-provider-deed' },
    ],
    sidebar: [
      {
        text: 'Guide',
        items: [
          { text: 'What is Deed?', link: '/guide/what-is-deed' },
          { text: 'Try it locally', link: '/guide/try-locally' },
          { text: 'Web UI tour', link: '/guide/ui' },
          { text: 'Concepts', link: '/guide/concepts' },
          { text: 'Terraform provider', link: '/guide/terraform' },
          { text: 'Terraform state import', link: '/guide/terraform-import' },
          { text: 'Custom kinds', link: '/guide/custom-kinds' },
          { text: 'Label rules and overrides', link: '/guide/label-rules' },
          { text: 'Connectors (GitHub, HallPass, Porch)', link: '/guide/connectors' },
          { text: 'Pulse events', link: '/guide/pulse' },
          { text: 'Telemetry', link: '/guide/telemetry' },
          { text: 'Findings and alerts', link: '/guide/findings' },
          { text: 'deed-agent and the Go SDK', link: '/guide/agent' },
          { text: 'Icons', link: '/guide/icons' },
        ],
      },
      {
        text: 'Operators',
        items: [
          { text: 'Deployment', link: '/operators/deployment' },
          { text: 'Cloudflare', link: '/operators/cloudflare' },
          { text: 'Single sign-on', link: '/operators/sso' },
          { text: 'Service accounts', link: '/operators/service-accounts' },
          { text: 'Backups and restore', link: '/operators/backups' },
          { text: 'Operations', link: '/operators/operations' },
        ],
      },
      {
        text: 'Reference',
        items: [
          { text: 'Ingest API', link: '/reference/ingest-api' },
          { text: 'HTTP API (OpenAPI)', link: '/reference/api' },
          { text: 'Versioning', link: '/reference/versioning' },
          { text: 'Compatibility', link: '/reference/compatibility' },
          { text: 'Threat model', link: '/reference/threat-model' },
          { text: 'Validation', link: '/reference/validation' },
          { text: 'Performance', link: '/reference/performance' },
          { text: 'Testing', link: '/reference/testing' },
          { text: 'Architecture decisions', link: '/reference/decisions' },
        ],
      },
    ],
    socialLinks: [{ icon: 'github', link: repo }],
    editLink: { pattern: repo + '/edit/main/site/:path' },
    footer: {
      message: 'Apache-2.0 · Not externally security reviewed · Not yet released',
      copyright: 'Copyright 2026 Aurimas Niekis',
    },
    search: { provider: 'local' },
  },
});
