import test from 'node:test'
import assert from 'node:assert/strict'
import { drainSync } from '../src/lib/drain-sync.js'

test('one click drains a backlog larger than ten thousand in acknowledged chunks', async () => {
  let remaining = 12909
  const sizes = []
  const result = await drainSync(async () => {
    const n = Math.min(1000, remaining)
    remaining -= n
    sizes.push(n)
    return {sent: {rfid_logs: n}, has_more: remaining > 0, remaining_observations: remaining}
  })
  assert.equal(result.observations, 12909)
  assert.equal(result.sent.rfid_logs, 12909)
  assert.equal(result.batches, 13)
  assert.equal(Math.max(...sizes), 1000)
})

test('an interrupted transfer keeps completed progress and surfaces the failure', async () => {
  const progress = []
  let calls = 0
  await assert.rejects(drainSync(async () => {
    if (++calls === 2) throw new Error('HTTP 429')
    return {sent: {rfid_logs: 1000}, has_more: true, remaining_observations: 2000}
  }, value => progress.push(value)), /429/)
  assert.equal(calls, 2)
  assert.equal(progress[0].observations, 1000)
})

test('old responses remain compatible and a stalled queue cannot loop forever', async () => {
  assert.equal((await drainSync(async () => ({sent: {rfid_logs: 5}}))).batches, 1)
  await assert.rejects(drainSync(async () => ({sent: {rfid_logs: 0}, has_more: true})), /не продвигается/)
})

test('continuous live intake is bounded per user action without claiming the queue is empty', async () => {
  let calls = 0
  const result = await drainSync(async () => {
    calls++
    return {sent: {rfid_logs: 1000}, has_more: true, remaining_observations: 1}
  }, () => {}, 3)
  assert.equal(calls, 3)
  assert.equal(result.has_more, true)
  assert.equal(result.remaining_observations, 1)
})
