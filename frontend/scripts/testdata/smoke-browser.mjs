#!/usr/bin/env node
// CDP transport fixture only: it does not prove that the frontend renders.
import {createReadStream, writeSync} from 'node:fs'

let input = ''
setTimeout(() => {
  const requests = createReadStream(null, {fd: 3, encoding: 'utf8'})
  requests.on('data', chunk => {
    input += chunk
    for (let end; (end = input.indexOf('\0')) !== -1;) {
      const request = JSON.parse(input.slice(0, end))
      input = input.slice(end + 1)
      if (request.method === process.env.SMOKE_FAKE_HANG) continue
      let result = {}
      if (request.method === 'Target.createTarget') result = {targetId: 'fixture'}
      if (request.method === 'Target.attachToTarget') result = {sessionId: 'fixture'}
      if (request.method === 'Runtime.evaluate') {
        result = process.env.SMOKE_FAKE_EXCEPTION
          ? {exceptionDetails: {text: 'deliberate frontend exception'}}
          : {result: {value: request.params.expression === 'document.documentElement.outerHTML'
              ? '<html data-chrono-desk-ready="true"></html>' : {ready: true}}}
      }
      writeSync(4, JSON.stringify({id: request.id, result}) + '\0')
    }
  })
}, Number(process.env.SMOKE_FAKE_STARTUP_MS ?? 0))
