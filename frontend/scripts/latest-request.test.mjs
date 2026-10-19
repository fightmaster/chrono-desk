import assert from 'node:assert/strict'
import test from 'node:test'
import {latestRequest} from '../src/lib/latest-request.js'

test('an older response cannot apply after reload even when transport ignores abort', async () => {
  const slot = latestRequest()
  const old = slot.begin()
  let resolveOld
  const result = new Promise(resolve => { resolveOld = resolve }).then(value => old.current() ? value : null)
  const next = slot.begin()
  assert.equal(old.signal.aborted, true)
  resolveOld('old participant')
  assert.equal(await result, null)
  assert.equal(next.current(), true)
})

test('closing a drawer or hiding pending history invalidates its response', () => {
  const slot = latestRequest()
  const request = slot.begin()
  slot.cancel()
  assert.equal(request.signal.aborted, true)
  assert.equal(request.current(), false)
  slot.cancel()
  assert.equal(slot.begin().current(), true)
})
