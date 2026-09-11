import assert from 'node:assert/strict'
import {execFile} from 'node:child_process'
import {fileURLToPath} from 'node:url'
import {promisify} from 'node:util'
import test from 'node:test'

const execute = promisify(execFile)
const smoke = fileURLToPath(new URL('./runtime-smoke.mjs', import.meta.url))
const browser = fileURLToPath(new URL('./testdata/smoke-browser.mjs', import.meta.url))

// Exercise the actual harness, including its child cleanup, with a fake CDP
// peer. The separate real-browser smoke remains the frontend acceptance gate.
function run(env = {}) {
  return execute(process.execPath, [smoke], {
    env: {...process.env, CHRONO_DESK_BROWSER: browser, ...env},
    timeout: 40000,
    maxBuffer: 128 * 1024,
  })
}

test('cold browser startup may exceed the normal command deadline', async () => {
  const {stdout} = await run({SMOKE_FAKE_STARTUP_MS: '6000'})
  assert.match(stdout, /frontend runtime smoke passed/)
})

test('a browser that never answers startup still fails within its deadline', async () => {
  await assert.rejects(run({SMOKE_FAKE_HANG: 'Target.createTarget'}), error => {
    assert.equal(error.code, 1)
    assert.match(error.stderr, /Target.createTarget after 30000ms/)
    assert.match(error.stderr, /Requests: \[\]/)
    return true
  })
})

test('commands after startup retain their shorter deadline', async () => {
  await assert.rejects(run({SMOKE_FAKE_HANG: 'Page.enable'}), error => {
    assert.equal(error.code, 1)
    assert.match(error.stderr, /Page.enable after 5000ms/)
    return true
  })
})

test('frontend exceptions still fail the smoke', async () => {
  await assert.rejects(run({SMOKE_FAKE_EXCEPTION: '1'}), error => {
    assert.equal(error.code, 1)
    assert.match(error.stderr, /deliberate frontend exception/)
    return true
  })
})
