import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'
import { defineConfig, globalIgnores } from 'eslint/config'

export default defineConfig([
  globalIgnores(['dist']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      tseslint.configs.recommended,
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
    ],
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
    },
  },
  // Playwright E2E harness (plan-13): globalSetup/teardown, fixtures, and specs run in
  // NODE (the Playwright runner), not the browser — they use child_process/fs/process. Add
  // Node globals so `eslint .` lints them without no-undef noise, and disable the
  // React-Fast-Refresh component-export rule (these are test modules, not components).
  {
    files: ['e2e/**/*.ts', 'playwright.config.ts'],
    languageOptions: {
      globals: { ...globals.node },
    },
    rules: {
      'react-refresh/only-export-components': 'off',
    },
  },
  // FE import ratchet (plan-0). Encodes today's layering: components/ is a PRESENTATION layer and
  // must not reach into the api/ transport layer directly — controller/API access routes through a
  // store (e.g. stores/controllerStore, which owns the api/controllerClient). New violations are
  // hard errors; the components that already imported from api/ before the ratchet existed are
  // grandfathered by the shrink-only allow-list block that FOLLOWS this one (later flat-config
  // blocks win). The regex matches any specifier ending in an `api/…` segment (e.g. the actual
  // '../../api/controllerClient' imports), sidestepping minimatch's dot-segment handling of '../'.
  {
    files: ['src/components/**/*.{ts,tsx}'],
    ignores: ['src/components/**/*.test.{ts,tsx}'],
    rules: {
      'no-restricted-imports': ['error', {
        patterns: [{
          regex: '(^|/)api/',
          message:
            'components/ must not import from the api/ layer directly — route controller/API access through a store (e.g. stores/controllerStore). See the plan-0 FE import ratchet in eslint.config.js.',
        }],
      }],
    },
  },
  // Shrink-only allow-list: the components that imported from api/ BEFORE the ratchet existed. This
  // block turns the rule OFF for exactly these files, capturing current reality so `npm run lint`
  // is green today. TODO(plan-0): migrate each to consume api data via a store, then delete its
  // entry here — the list only ever shrinks, tightening the boundary. Do NOT add new entries: a new
  // component importing api/ must fail lint.
  {
    files: [
      'src/components/deploy/AgentUpdateSettings.tsx',
      'src/components/deploy/BootstrapSettings.tsx',
      'src/components/deploy/MimicCatalogSettings.tsx',
      'src/components/deploy/TelemetryHistorySettings.tsx',
      'src/components/deploy/TwoFactorSettings.tsx',
      'src/components/deploy/UpdateStatusChip.tsx',
    ],
    rules: {
      'no-restricted-imports': 'off',
    },
  },
])
