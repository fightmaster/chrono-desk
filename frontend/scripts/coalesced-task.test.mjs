import assert from 'node:assert/strict'
import test from 'node:test'
import {coalescedTask} from '../src/lib/coalesced-task.js'

const deferred = () => {
  let resolve
  const promise = new Promise(done => { resolve = done })
  return {promise, resolve}
}
const turn = () => new Promise(resolve => setImmediate(resolve))

test('slow polling coalesces many ticks and manual refreshes without overlap', async () => {
  const first = deferred(), second = deferred()
  let calls = 0, running = 0, maxRunning = 0
  const task = coalescedTask(async () => {
    calls++; running++; maxRunning = Math.max(maxRunning, running)
    await (calls === 1 ? first.promise : second.promise)
    running--
  })
  const completion = task.request()
  await turn()
  for (let i = 0; i < 20; i++) task.request()
  assert.equal(calls, 1)
  first.resolve()
  await turn()
  assert.equal(calls, 2)
  second.resolve()
  await completion
  assert.equal(maxRunning, 1)
  assert.equal(calls, 2)
})

test('closing a view drops queued work and prevents a scheduled first request', async () => {
  const held = deferred()
  let calls = 0
  const task = coalescedTask(async () => { calls++; await held.promise })
  const completion = task.request()
  await turn()
  task.request()
  task.close()
  held.resolve()
  await completion
  await task.request()
  assert.equal(calls, 1)
  const never = coalescedTask(() => { throw Error('closed view started network work') })
  const scheduled = never.request()
  never.close()
  await scheduled
})

test('a failed attempt does not permanently block future refreshes', async () => {
  let calls = 0
  const task = coalescedTask(async () => { if (++calls === 1) throw Error('offline') })
  await assert.rejects(task.request(), /offline/)
  await task.request()
  assert.equal(calls, 2)
})
