import assert from 'node:assert/strict'
import test from 'node:test'
import {msToInput,inputToMs} from '../src/lib/api.js'

test('checkpoint upper bound preserves local millisecond precision',()=>{
  const at=new Date(2026,9,21,12,5,0,35).getTime()
  assert.equal(inputToMs(msToInput(at,true)),at)
  assert.match(msToInput(at,true),/\.035$/)
  assert.doesNotMatch(msToInput(at),/\.035$/)
})
