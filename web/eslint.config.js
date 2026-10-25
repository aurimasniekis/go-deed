// @ts-check
// Adapted from Porch web/eslint.config.js (Apache-2.0).
import js from '@eslint/js'
import prettier from 'eslint-config-prettier'
import jsxA11y from 'eslint-plugin-jsx-a11y'
import reactHooks from 'eslint-plugin-react-hooks'
import { defineConfig, globalIgnores } from 'eslint/config'
import globals from 'globals'
import tseslint from 'typescript-eslint'

/**
 * Security-relevant syntax bans (DISCOVERY Q-52): catalog content (names, descriptions, labels, provider output) must
 * never reach the DOM as HTML.
 */
const htmlInjectionBans = [
  {
    selector: "JSXAttribute[name.name='dangerouslySetInnerHTML']",
    message: 'dangerouslySetInnerHTML is forbidden. Render text nodes.',
  },
  {
    selector: "Property[key.name='dangerouslySetInnerHTML']",
    message: 'dangerouslySetInnerHTML is forbidden. Render text nodes.',
  },
  {
    selector: 'AssignmentExpression > MemberExpression.left[property.name=/^(innerHTML|outerHTML)$/]',
    message: 'Assigning innerHTML/outerHTML is forbidden.',
  },
  {
    selector: 'CallExpression[callee.property.name=/^(insertAdjacentHTML|createContextualFragment|parseFromString)$/]',
    message: 'HTML-parsing DOM APIs are forbidden.',
  },
  {
    selector: "CallExpression[callee.object.name='document'][callee.property.name=/^(write|writeln)$/]",
    message: 'document.write is forbidden.',
  },
]

export default defineConfig([
  globalIgnores([
    'dist',
    'node_modules',
    'src/routeTree.gen.ts',
    'src/api/schema.gen.ts',
    'test-results',
    'playwright-report',
    'blob-report',
    'e2e/screenshots',
    'e2e/.state',
    'public',
  ]),
  {
    files: ['**/*.{ts,tsx,js,mjs}'],
    extends: [js.configs.recommended, tseslint.configs.strictTypeChecked, tseslint.configs.stylisticTypeChecked],
    languageOptions: {
      parserOptions: {
        projectService: true,
        tsconfigRootDir: import.meta.dirname,
      },
      globals: { ...globals.browser },
    },
    rules: {
      'no-eval': 'error',
      'no-implied-eval': 'error',
      'no-new-func': 'error',
      'no-script-url': 'error',
      'no-restricted-syntax': ['error', ...htmlInjectionBans],
      'no-restricted-imports': [
        'error',
        {
          patterns: [
            {
              group: ['@tanstack/*devtools*'],
              message:
                'Devtools must only be loaded through the import.meta.env.DEV dynamic import in src/devtools.tsx so they are tree-shaken from production.',
            },
          ],
        },
      ],
      '@typescript-eslint/restrict-template-expressions': ['error', { allowNumber: true }],
      '@typescript-eslint/consistent-type-definitions': ['error', 'interface'],
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
      '@typescript-eslint/no-confusing-void-expression': ['error', { ignoreArrowShorthand: true }],
      '@typescript-eslint/no-misused-promises': ['error', { checksVoidReturn: { attributes: false } }],
      // `||` on strings and booleans is deliberate: an empty string (an unset display name, owner…) falls back too.
      '@typescript-eslint/prefer-nullish-coalescing': ['error', { ignorePrimitives: { string: true, boolean: true } }],
    },
  },
  {
    files: ['src/**/*.{ts,tsx}'],
    extends: [jsxA11y.flatConfigs.strict, reactHooks.configs.flat.recommended],
  },
  {
    files: ['**/*.{js,mjs}'],
    extends: [tseslint.configs.disableTypeChecked],
  },
  {
    files: ['../scripts/**/*.mjs', 'scripts/**/*.mjs', '*.config.{ts,js}', 'e2e/**/*.ts'],
    languageOptions: { globals: { ...globals.node } },
  },
  {
    // TanStack Router route files use `throw redirect()` / `throw notFound()`.
    files: ['src/routes/**/*.tsx'],
    rules: { '@typescript-eslint/only-throw-error': 'off' },
  },
  {
    files: ['src/devtools.tsx'],
    rules: { 'no-restricted-imports': 'off' },
  },
  prettier,
])
