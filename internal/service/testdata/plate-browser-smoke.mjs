// Node 22+ and an existing Chromium binary; no package install or Internet.
// Only the isolated edge_browser_test.go fixture's loopback API is permitted.
import assert from 'node:assert/strict'
import {spawn} from 'node:child_process'
import {mkdtemp, rm, writeFile} from 'node:fs/promises'
import {tmpdir} from 'node:os'
import {join} from 'node:path'

const [base, phase, reference, artifacts] = process.argv.slice(2)
const origin = new URL(base)
assert.equal(origin.hostname, '127.0.0.1')
assert.equal(origin.protocol, 'http:')
assert.ok(['configure', 'review', 'verify'].includes(phase))
assert.equal(typeof WebSocket, 'function', 'Node 22+ is required')
const key = 'synthetic-chain-key'
assert.equal((await fetch(base + '/api/status')).status, 401)
assert.equal((await fetch(base + '/api/status', {
  headers: {Authorization: 'Basic ' + Buffer.from('local:' + key).toString('base64')}
})).status, 200)

const profile = await mkdtemp(join(tmpdir(), 'plate-browser-profile-'))
const child = spawn(process.env.EDGE_BROWSER_BINARY, [
  '--headless', '--no-sandbox', '--disable-gpu', '--no-first-run',
  '--disable-background-networking', '--disable-component-update', '--disable-quic',
  '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1, EXCLUDE localhost',
  '--proxy-server=http://127.0.0.1:9', '--proxy-bypass-list=127.0.0.1;localhost',
  '--remote-debugging-address=127.0.0.1', '--remote-debugging-port=0',
  '--user-data-dir=' + profile, 'about:blank'
], {stdio: ['ignore', 'ignore', 'pipe']})
let browserLog = '', closed = false
const exited = new Promise(resolve => child.once('close', code => { closed = true; resolve(code) }))
child.stderr.on('data', chunk => { browserLog = (browserLog + chunk).slice(-16000) })
const watchdog = setTimeout(() => child.kill('SIGKILL'), 45000)
let socket
const delay = ms => new Promise(resolve => setTimeout(resolve, ms))
async function wait(check, label) {
  const deadline = Date.now() + 8000
  let lastError
  while (Date.now() < deadline) {
    try { const result = await check(); if (result) return result } catch (error) { lastError = error }
    await delay(30)
  }
  throw new Error('Timed out: ' + label + (lastError ? ': ' + lastError.message : ''))
}

try {
  const endpoint = await wait(() => browserLog.match(/DevTools listening on (ws:\/\/127\.0\.0\.1:[^\s]+)/)?.[1], 'browser CDP startup')
  socket = new WebSocket(endpoint)
  await new Promise((resolve, reject) => { socket.addEventListener('open', resolve, {once:true}); socket.addEventListener('error', reject, {once:true}) })
  let next = 0, session
  const pending = new Map(), errors = [], blocked = [], requests = []
  const send = (method, params = {}, targetSession = session) => new Promise((resolve, reject) => {
    const id = ++next
    const timer = setTimeout(() => { pending.delete(id); reject(new Error('CDP timeout: ' + method)) }, 5000)
    pending.set(id, {resolve, reject, timer})
    socket.send(JSON.stringify({id, method, params, ...(targetSession ? {sessionId:targetSession} : {})}))
  })
  socket.addEventListener('message', event => {
    const message = JSON.parse(event.data)
    if (message.id) {
      const call = pending.get(message.id)
      if (!call) return
      pending.delete(message.id); clearTimeout(call.timer)
      if (message.error) call.reject(new Error(message.error.message)); else call.resolve(message.result)
      return
    }
    if (message.method === 'Runtime.exceptionThrown') errors.push(message.params.exceptionDetails.text)
    if (message.method === 'Fetch.requestPaused') {
      const {requestId, request} = message.params
      let allowed = request.url.startsWith(base + '/')
      if (allowed && request.method === 'POST' && new URL(request.url).pathname === '/actions') {
        allowed = ['pause', 'resume', 'configure', 'confirm_clock'].includes(new URLSearchParams(request.postData).get('action'))
      }
      requests.push(request.method + ' ' + new URL(request.url).pathname)
      if (!allowed) blocked.push(request.url)
      send(allowed ? 'Fetch.continueRequest' : 'Fetch.failRequest', allowed ? {requestId} : {requestId, errorReason:'BlockedByClient'}, message.sessionId).catch(error => errors.push(error.message))
    }
  })
  const {targetId} = await send('Target.createTarget', {url:'about:blank'}, undefined)
  session = (await send('Target.attachToTarget', {targetId, flatten:true}, undefined)).sessionId
  await send('Page.enable'); await send('Runtime.enable'); await send('Network.enable')
  await send('Network.setExtraHTTPHeaders', {headers:{'X-Feibot-Token':key}})
  await send('Fetch.enable', {patterns:[{urlPattern:'*', requestStage:'Request'}]})
  await send('Emulation.setTouchEmulationEnabled', {enabled:true})
  const evaluate = async expression => {
    const result = await send('Runtime.evaluate', {expression, awaitPromise:true, returnByValue:true})
    if (result.exceptionDetails) throw new Error(result.exceptionDetails.exception?.description || result.exceptionDetails.text)
    return result.result.value
  }
  const state = () => evaluate(`fetch('/api/status').then(r => r.json())`)
  const navigate = async path => {
    await send('Page.navigate', {url:base + path})
    await wait(() => evaluate(`document.readyState === 'complete' && location.pathname === ${JSON.stringify(path)} && !!document.querySelector('h1')`), 'page ' + path)
  }
  const viewport = async width => send('Emulation.setDeviceMetricsOverride', {width, height:800, deviceScaleFactor:1, mobile:true})
  const layout = async label => {
    const result = await evaluate(`({width:innerWidth, scroll:document.documentElement.scrollWidth, heading:document.querySelector('h1')?.textContent})`)
    assert.ok(result.scroll <= result.width + 1, label + ': horizontal overflow ' + JSON.stringify(result))
  }
  const screenshot = async label => {
    const {data} = await send('Page.captureScreenshot', {format:'png', captureBeyondViewport:false})
    await writeFile(join(artifacts, phase + '-' + label + '.png'), Buffer.from(data, 'base64'), {flag:'wx', mode:0o600})
  }
  const form = action => `document.querySelector('[name="action"][value="${action}"]').closest('form')`
  const tap = async selector => {
    const point = await evaluate(`(() => {const e=document.querySelector(${JSON.stringify(selector)}); if(!e || e.matches(':disabled')) throw Error('Missing/disabled control'); e.scrollIntoView({block:'center'}); const r=e.getBoundingClientRect(); return {x:r.x+r.width/2,y:r.y+r.height/2};})()`)
    await send('Input.dispatchTouchEvent', {type:'touchStart', touchPoints:[point]})
    await send('Input.dispatchTouchEvent', {type:'touchEnd', touchPoints:[]})
  }
  const action = async name => {
    const revision = (await state()).revision
    await evaluate(`(() => { const f=${form(name)}; const b=f.querySelector('button:not([type="button"])'); if(b.matches(':disabled') || !f.reportValidity()) throw Error('Invalid action form'); f.requestSubmit(b); })()`)
    await wait(() => evaluate(`location.pathname === '/' && document.readyState === 'complete' && Number(document.querySelector('[name=revision]')?.value) > ${revision}`), 'action ' + name)
  }
  await viewport(360); await navigate('/')
  for (const width of [360, 390, 768]) {
    await viewport(width); await layout('home ' + width)
  }
  await viewport(360)
  assert.ok(await evaluate(`document.querySelector('#startup-guide')?.textContent.includes('после включения')`))
  assert.equal(await evaluate(`document.body.textContent.includes('0001-01-01')`), false, 'missing capture time was rendered as a real date')
  await screenshot('home-360')

  if (phase === 'configure') {
    if (!(await state()).paused) await action('pause')
    const stale = await evaluate(`({revision:${form('configure')}.elements.revision.value, csrf:${form('configure')}.elements.csrf_token.value})`)
    await evaluate(`(() => {const f=${form('configure')}; for(const [k,v] of Object.entries({event_id:'200',device_code:'plate-phone',board:'plate-mobile',timezone:'UTC'})) {f.elements[k].value=v; f.elements[k].dispatchEvent(new Event('input',{bubbles:true}));}})()`)
    await action('configure')
    let current = await state()
    assert.equal(current.settings.source.event_id, 200)
    assert.equal(current.settings.source.board, 'plate-mobile')
    assert.equal(current.settings.source.device_code, 'plate-phone')
    const oldStatus = await evaluate(`fetch('/actions',{method:'POST',body:new URLSearchParams({action:'resume',revision:${JSON.stringify(stale.revision)},csrf_token:${JSON.stringify(stale.csrf)}})}).then(r=>r.status)`)
    assert.equal(oldStatus, 409)
    const csrfStatus = await evaluate(`fetch('/actions',{method:'POST',body:new URLSearchParams({action:'resume',revision:${JSON.stringify(String(current.revision))}})}).then(r=>r.status)`)
    assert.equal(csrfStatus, 403)
    assert.equal((await state()).paused, true)
    await tap('form.clock-form .phone-time')
    const chosen = await evaluate(`${form('confirm_clock')}.elements.time.value`)
    assert.ok(Math.abs(Date.parse(chosen) - Date.now()) < 3000, 'phone time button did not populate current time')
    await action('confirm_clock')
    assert.equal((await state()).clock_error, '')
    await action('resume')
    assert.equal(await evaluate(`${form('configure')}.querySelector('fieldset').disabled`), true)
    current = await state()
    await writeFile(join(artifacts, 'saved-settings.json'), JSON.stringify(current.settings), {flag:'wx', mode:0o600})
    await layout('configured active input')
    await screenshot('configured-360')
  } else {
    const {readFile} = await import('node:fs/promises')
    const saved = JSON.parse(await readFile(join(artifacts, 'saved-settings.json'), 'utf8'))
    let current = await state()
    assert.deepEqual(current.settings, saved, 'restart changed saved selection/settings')
    assert.equal(current.journal.captured, 2)
    if (phase === 'review') {
      assert.equal(current.paused, false)
      assert.equal(current.journal.unresolved, 1)
      await action('pause')
      const exported = await evaluate(`fetch('/api/export').then(r=>r.json())`)
      assert.equal(exported.records.length, 2)
      const raw = exported.records.find(r => !r.observation)
      assert.ok(raw)
      await navigate('/review'); await layout('unresolved review')
      await screenshot('undated-360')
      await evaluate(`(() => {const f=document.querySelector('form[action="/review/preview"]'); const values={mode:'single',from_sequence:${JSON.stringify(raw.sequence)},through_sequence:${JSON.stringify(raw.sequence)},reference_sequence:${JSON.stringify(raw.sequence)},reference_time:${JSON.stringify(reference)},note:'Synthetic browser fixture: date verified against generated reader frame'}; for(const [k,v] of Object.entries(values)) f.elements[k].value=v; f.requestSubmit();})()`)
      await wait(() => evaluate(`location.pathname === '/review/preview' && !!document.querySelector('form[action="/review/apply"]')`), 'date preview')
      assert.equal((await state()).journal.unresolved, 1, 'preview mutated raw history')
      await layout('date preview'); await screenshot('preview-360')
      await tap('form[action="/review/apply"] input[type=checkbox]')
      const revision = (await state()).revision
      await evaluate(`document.querySelector('form[action="/review/apply"]').requestSubmit()`)
      await wait(async () => (await state()).revision > revision, 'apply explicit date review')
      await navigate('/')
      current = await state()
      assert.equal(current.journal.unresolved, 0)
      const resolved = await evaluate(`fetch('/api/export').then(r=>r.json())`)
      for (const previous of exported.records) {
        const next = resolved.records.find(r => r.sequence === previous.sequence)
        assert.equal(next.raw, previous.raw, 'review rewrote raw bytes')
        assert.deepEqual(next.sample, previous.sample, 'review rewrote clock evidence')
        if (previous.observation) assert.deepEqual(next.observation, previous.observation)
      }
      assert.equal(resolved.records.find(r => r.sequence === raw.sequence).observation.time, Date.parse(reference))
    } else {
      assert.equal(current.paused, true, 'restart reset local input pause')
      assert.equal(await evaluate(`document.body.textContent.includes('В этом запуске ещё не было сохранённых сообщений')`), true)
      assert.equal(current.journal.unresolved, 0)
      for (const destination of current.destinations) {
        assert.equal(destination.acknowledged, 0)
        assert.equal(destination.pending + destination.retry, 2, 'restart lost independent pending queue')
      }
      assert.equal((await evaluate(`fetch('/api/export').then(r=>r.json())`)).records.length, 2)
    }
  }
  const actions = await evaluate(`fetch('/api/actions').then(r=>r.json())`)
  assert.ok(actions.length > 0)
  assert.ok(actions.every(a => !['host_clock', 'host_ntp', 'reader_clock'].includes(a.action)), 'unexpected hardware command')
  assert.deepEqual(errors, [], 'browser JavaScript/transport errors')
  assert.deepEqual(blocked, [], 'unexpected external or hardware request')
  assert.ok(requests.includes('GET /app.js'), 'real local script was not loaded')
  console.log(JSON.stringify({phase, browser:(await send('Browser.getVersion', {}, undefined)).product, requests:requests.length, mobileWidths:[360,390,768], result:'PASS'}))
  await send('Browser.close', {}, undefined)
} catch (error) {
  console.error(browserLog)
  throw error
} finally {
  clearTimeout(watchdog)
  socket?.close()
  if (!closed) child.kill('SIGTERM')
  await Promise.race([exited, delay(2000)])
  if (!closed) { child.kill('SIGKILL'); await exited }
  await rm(profile, {recursive:true, force:true})
}
