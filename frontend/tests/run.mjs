// Runs tests/*.test.mjs against the TypeScript sources. There is no DOM and no test
// framework dependency: esbuild (already installed with vite) turns the TypeScript
// the tests import into plain JavaScript in a temp folder, and `node --test` runs it.
import { build } from 'esbuild'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, readdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const entries = readdirSync(here).filter((f) => f.endsWith('.test.mjs')).map((f) => join(here, f))
const out = mkdtempSync(join(tmpdir(), 'ztax-fe-tests-'))
let code = 1
try {
  await build({ entryPoints: entries, bundle: true, platform: 'node', format: 'esm', outdir: out, outExtension: { '.js': '.mjs' }, logLevel: 'error' })
  const files = readdirSync(out).filter((f) => f.endsWith('.mjs')).map((f) => join(out, f))
  code = spawnSync(process.execPath, ['--test', ...files], { stdio: 'inherit' }).status ?? 1
} finally {
  rmSync(out, { recursive: true, force: true })
}
process.exit(code)
