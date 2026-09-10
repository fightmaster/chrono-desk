// Opt-in lifecycle regression for the smoke harness, not product UI acceptance.
// Use an existing browser; no package install, site, event DB or reader.
import assert from 'node:assert/strict'
import {spawn} from 'node:child_process'
import {copyFile, mkdir, mkdtemp, rm, writeFile} from 'node:fs/promises'
import {tmpdir} from 'node:os'
import {join} from 'node:path'
import {test} from 'node:test'

test('browser smoke requires completed actions and propagates page failures', {timeout: 90000}, async t => {
  assert.ok(process.env.CHRONO_DESK_BROWSER, 'set CHRONO_DESK_BROWSER to an installed browser')
  const root = await mkdtemp(join(tmpdir(), 'chrono-smoke-regression-'))
  t.after(() => rm(root, {recursive: true, force: true}))
  await mkdir(join(root, 'scripts'))
  await mkdir(join(root, 'dist'))
  for (const file of ['runtime-smoke.mjs', 'edge-smoke.mjs']) {
    await copyFile(new URL(file, import.meta.url), join(root, 'scripts', file))
  }
  for (const scenario of [
    {name: 'delayed mount', html: `<script>setTimeout(() => document.body.dataset.chronoDeskReady = 'true', 100)</script>`, pass: true},
    {name: 'uncaught exception', html: `<script>throw new Error('synthetic-render-failure')</script>`, error: /synthetic-render-failure/},
    {name: 'missing mount', html: '', error: /Timed out waiting for the rendered shell/},
    // A mounted shell is insufficient for --edge; its actual bootstrap must
    // reach the action completion marker and fixture request-count assertions.
    {name: 'incomplete edge actions', html: `<div data-chrono-desk-ready="true"></div>`, edge: true, error: /Edge smoke timed out waiting for UI/}
  ]) {
    await t.test(scenario.name, {timeout: 25000}, async () => {
      await writeFile(join(root, 'dist', 'index.html'), '<!doctype html><html><head></head><body>' + scenario.html + '</body></html>')
      const child = spawn(process.execPath, [join(root, 'scripts', 'runtime-smoke.mjs'), ...(scenario.edge ? ['--edge'] : [])], {
        env: process.env, stdio: ['ignore', 'pipe', 'pipe']
      })
      let output = ''
      child.stdout.on('data', data => { output += data })
      child.stderr.on('data', data => { output += data })
      const result = await new Promise((resolve, reject) => {
        child.once('error', reject)
        child.once('close', (code, signal) => resolve({code, signal}))
      })
      assert.equal(result.signal, null, output)
      if (scenario.pass) {
        assert.equal(result.code, 0, output)
        assert.match(output, /runtime smoke passed/)
      } else {
        assert.notEqual(result.code, 0, output)
        assert.match(output, scenario.error)
        assert.doesNotMatch(output, /smoke passed/)
      }
    })
  }
})
