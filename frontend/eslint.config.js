import js from '@eslint/js'
import tseslint from 'typescript-eslint'
import jsxA11y from 'eslint-plugin-jsx-a11y'

// ADR-0019's constraints, as rules rather than as good intentions.
//
// The type system carries most of C1 already — FiscalAmount is a branded string
// with no arithmetic on it — but a type is only a compile-time argument, and
// `Number(amount)` type-errors while `+amount` does not always. These rules are
// the second line. C4 and C8 have no type-level expression at all, so here they
// are the only line.
export default tseslint.config(
  { ignores: ['dist', 'dist-tsc', 'node_modules'] },

  js.configs.recommended,
  ...tseslint.configs.recommendedTypeChecked,
  {
    languageOptions: {
      parserOptions: {
        project: ['./tsconfig.app.json', './tsconfig.node.json'],
        tsconfigRootDir: import.meta.dirname,
      },
    },
  },

  {
    files: ['src/**/*.{ts,tsx}'],
    plugins: { 'jsx-a11y': jsxA11y },
    rules: {
      // C6 — accessibility is a gate from the first screen, not a phase before
      // launch. Far cheaper to hold now than to retrofit across 38 screens.
      ...jsxA11y.configs.recommended.rules,

      'no-restricted-globals': [
        'error',
        // C1 — no fiscal arithmetic in the browser, ever. A displayed total is
        // a total the server calculated (ADR-0019 C1, ADR-0001 §4.2).
        { name: 'parseFloat', message: 'C1: fiscal values are displayed, never computed. The server provides derived figures.' },
        { name: 'parseInt', message: 'C1: fiscal values are displayed, never computed. The server provides derived figures.' },
      ],

      'no-restricted-syntax': [
        'error',
        {
          selector: 'CallExpression[callee.name="Number"]',
          message: 'C1: converting a value to a binary float is how a tax amount loses precision. Display the string.',
        },
        {
          selector: 'MemberExpression[property.name="toFixed"]',
          message: 'C1: toFixed rounds in binary floating-point, with no rounding policy and no evidence record (ADR-0002 §2.3).',
        },
        {
          selector: 'UnaryExpression[operator="+"] > Identifier',
          message: 'C1: unary plus coerces to a binary float.',
        },
        // C8 — no fiscal or personal data in browser storage beyond a session.
        // Residency does not follow data into a browser, and a cached figure
        // outlives the correction that superseded it.
        {
          selector: 'MemberExpression[object.name=/^(localStorage|sessionStorage)$/]',
          message: 'C8: no fiscal or personal data in browser storage beyond a session.',
        },
        {
          selector: 'NewExpression[callee.name="Intl"], MemberExpression[object.name="Intl"][property.name="NumberFormat"]',
          message: 'C1: Intl.NumberFormat takes a number. Format the canonical decimal string instead (see fiscal/money.ts).',
        },
      ],
    },
  },

  {
    // C4 — the API client is the generated TypeScript SDK; hand-written calls
    // against ZoikoTax endpoints drift from the contract and bypass the error
    // taxonomy (ADR-0016 §2.9). The SDK opens in W2 lane K.
    //
    // Two files are excepted, each pinned by name rather than by directory so
    // the exception cannot widen into a habit:
    //
    //   platform/health.ts  the operational probes, which are not part of the
    //                       fiscal contract and are not in any SDK.
    //   platform/api.ts     the administration and session surface. Also not
    //                       the fiscal contract: no endpoint it calls carries a
    //                       fiscal amount, and its types cannot express one. It
    //                       is replaced by generated code when lane K lands.
    //
    // What remains prohibited is the thing C4 is actually about: a fetch against
    // a determination, transaction, obligation or decision endpoint, anywhere.
    files: ['src/**/*.{ts,tsx}'],
    ignores: ['src/platform/health.ts', 'src/platform/api.ts'],
    rules: {
      'no-restricted-properties': [
        'error',
        { object: 'window', property: 'fetch', message: 'C4: use the generated SDK. Health probes live in platform/health.ts.' },
      ],
      'no-restricted-globals': [
        'error',
        { name: 'fetch', message: 'C4: use the generated SDK (W2 lane K). Health probes live in platform/health.ts.' },
        { name: 'XMLHttpRequest', message: 'C4: use the generated SDK (W2 lane K).' },
        { name: 'parseFloat', message: 'C1: fiscal values are displayed, never computed.' },
        { name: 'parseInt', message: 'C1: fiscal values are displayed, never computed.' },
      ],
    },
  },

  {
    files: ['*.config.{js,ts}'],
    ...tseslint.configs.disableTypeChecked,
  },
)
