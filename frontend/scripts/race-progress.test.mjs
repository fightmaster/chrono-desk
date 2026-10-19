import assert from 'node:assert/strict'
import test from 'node:test'
import {progressSummary} from '../src/lib/race-progress.js'

test('finished count does not imply DNF/DNS/DSQ are still expected', () => {
  const text = progressSummary({counts:{finished:76,on_course:0,dnf:4,dns:5,dsq:0}}, null)
  assert.match(text, /Финишировали: 76/)
  assert.match(text, /На дистанции: 0/)
  assert.match(text, /Сошли: 4/)
  assert.match(text, /DNS: 5/)
  assert.doesNotMatch(text, /из 80/)
})

test('older API data never invents a zero on-course count', () => {
  assert.equal(progressSummary(null, null), '')
  const text = progressSummary(undefined, {finished:76,dnf:4,dns:5,dsq:0,started:80})
  assert.match(text, /Финишировали: 76/)
  assert.doesNotMatch(text, /На дистанции|из 80|undefined/)
})

test('unknown/future starts, review and reserves are separate', () => {
  const text = progressSummary({counts:{finished:0,on_course:2,dnf:0,dns:1,dsq:0,not_started:3,needs_review:1,reserve:5}})
  assert.match(text, /Старт не наступил или не задан: 3/)
  assert.match(text, /Требуют проверки: 1/)
  assert.match(text, /Резерв: 5/)
})
