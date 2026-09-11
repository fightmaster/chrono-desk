// Task-local actual RUN5 browser workflow; Node built-ins and existing Chromium only.
import assert from 'node:assert/strict'
import {spawn} from 'node:child_process'
import {mkdtemp, rm, writeFile} from 'node:fs/promises'
import {tmpdir} from 'node:os'
import {join} from 'node:path'

const [base, spki, artifacts] = process.argv.slice(2)
const origin = new URL(base)
assert.equal(origin.hostname, 'app.chrono.localhost')
assert.equal(origin.protocol, 'https:')
assert.match(spki, /^[A-Za-z0-9+/]{43}=$/)
const profile = await mkdtemp(join(tmpdir(), 'management-browser-'))
const child = spawn(process.env.EDGE_BROWSER_BINARY, [
  '--headless', '--no-sandbox', '--disable-gpu', '--no-first-run',
  '--disable-background-networking', '--disable-component-update', '--disable-quic',
  '--host-resolver-rules=MAP app.chrono.localhost 127.0.0.1, MAP * ~NOTFOUND, EXCLUDE 127.0.0.1',
  '--proxy-server=http://127.0.0.1:9', '--proxy-bypass-list=app.chrono.localhost;127.0.0.1',
  '--ignore-certificate-errors-spki-list=' + spki,
  '--remote-debugging-pipe', '--user-data-dir=' + profile, 'about:blank'
], {stdio:['ignore', 'ignore', 'pipe', 'pipe', 'pipe']})
let closed = false, next = 0, session, input = ''
const pending = new Map(), exceptions = []
const fail = error => { for (const call of pending.values()) {clearTimeout(call.timer); call.reject(error)}; pending.clear() }
const exited = new Promise(resolve => child.once('close', () => {closed = true; fail(new Error('browser exited')); resolve()}))
child.once('error', fail)
child.stderr.resume() // Never print pages, credentials or browser request headers.
child.stdio[3].on('error', fail)
child.stdio[4].on('error', fail)
const send = (method, params = {}, target = session) => new Promise((resolve, reject) => {
  const id = ++next
  const timer = setTimeout(() => {pending.delete(id); reject(new Error('CDP timeout: ' + method))}, 8000)
  pending.set(id, {resolve, reject, timer})
  child.stdio[3].write(JSON.stringify({id, method, params, ...(target ? {sessionId:target} : {})}) + '\0')
})
child.stdio[4].setEncoding('utf8')
child.stdio[4].on('data', chunk => {
  input += chunk
  for (let end; (end = input.indexOf('\0')) !== -1;) {
    const raw = input.slice(0, end); input = input.slice(end + 1)
    if (!raw) continue
    const message = JSON.parse(raw)
    if (message.id) {
      const call = pending.get(message.id)
      if (!call) continue
      pending.delete(message.id); clearTimeout(call.timer)
      message.error ? call.reject(new Error(message.error.message)) : call.resolve(message.result)
    } else if (message.method === 'Runtime.exceptionThrown') {
      exceptions.push(message.params.exceptionDetails.text)
    } else if (message.method === 'Fetch.requestPaused') {
      const {requestId, request} = message.params
      const allowed = request.url.startsWith(base + '/')
      send(allowed ? 'Fetch.continueRequest' : 'Fetch.failRequest', allowed ? {requestId} : {requestId, errorReason:'BlockedByClient'}, message.sessionId).catch(fail)
    }
  }
})
const watchdog = setTimeout(() => {fail(new Error('browser workflow deadline')); child.kill('SIGKILL')}, 75000)
const evaluate = async expression => {
  const result = await send('Runtime.evaluate', {expression, awaitPromise:true, returnByValue:true})
  if (result.exceptionDetails) throw new Error('browser evaluation failed: ' + result.exceptionDetails.text)
  return result.result.value
}
const wait = async (expression, label) => {
  const deadline = Date.now() + 12000
  while (Date.now() < deadline) {
    if (await evaluate(expression)) return
    await new Promise(resolve => setTimeout(resolve, 50))
  }
  throw new Error('browser wait: ' + label)
}
const ready = () => wait(`document.readyState === 'complete' && !!document.body`, 'document')
const viewport = width => send('Emulation.setDeviceMetricsOverride', {width, height:850, deviceScaleFactor:1, mobile:true})
const shot = async label => {
  const {data} = await send('Page.captureScreenshot', {format:'png', captureBeyondViewport:false})
  await writeFile(join(artifacts, label + '.png'), Buffer.from(data, 'base64'), {flag:'wx', mode:0o600})
}
const layout = async label => {
  const result = await evaluate(`({width:innerWidth, scroll:document.documentElement.scrollWidth})`)
  assert.ok(result.scroll <= result.width + 1, label + ': horizontal overflow ' + JSON.stringify(result))
}
try {
  const {targetId} = await send('Target.createTarget', {url:'about:blank'})
  session = (await send('Target.attachToTarget', {targetId, flatten:true})).sessionId
  await send('Page.enable'); await send('Runtime.enable'); await send('Network.enable')
  await send('Fetch.enable', {patterns:[{urlPattern:'*', requestStage:'Request'}]})
  await viewport(390)
  await send('Page.navigate', {url:base + '/login'})
  await wait(`!!document.querySelector('[name=email]')`, 'login form')
  await evaluate(`(() => {const f=document.querySelector('[name=email]').form; f.elements.email.value='fixture@example.invalid'; f.elements.password.value='synthetic-password'; f.requestSubmit();})()`)
  await wait(`location.pathname !== '/login'`, 'login redirect')
  await send('Page.navigate', {url:base + '/edge-devices'})
  await wait(`!!document.querySelector('[name=name]')`, 'device list')
  await ready()
  for (const width of [360,390,768]) {await viewport(width); await layout('device list ' + width)}
  await viewport(390)
  await evaluate(`document.querySelector('[data-drawer-toggle]').click()`)
  await wait(`document.querySelector('#sidebar-multi-level-sidebar').getBoundingClientRect().left >= 0`, 'mobile drawer opens')
  await shot('menu-390')
  await evaluate(`document.querySelector('[data-testid=edge-devices-navigation]').click()`)
  await wait(`!!document.querySelector('[name=name]') && !document.querySelector('[drawer-backdrop]')`, 'menu navigation')
  await evaluate(`(() => {const e=document.querySelector('[name=name]');e.value='Browser plate';e.form.requestSubmit();})()`)
  await wait(`document.querySelectorAll('main code, .max-w-5xl code').length === 2`, 'one-time key')
  await ready()
  await layout('one-time key')
  assert.equal(await evaluate(`!!document.querySelector('script[src*="yandex"]') || document.documentElement.outerHTML.includes('webvisor')`), false)
  // This UI test supplies synthetic device telemetry to the actual API. Real
  // sidecar execution, durable outcomes and response loss have a separate chain.
  const pair = await evaluate(`Array.from(document.querySelectorAll('.max-w-5xl code')).map(e => e.textContent)`)
  assert.match(pair[0], /^[a-f0-9-]{36}$/); assert.match(pair[1], /^[a-f0-9]{64}$/)
  const heartbeat = {version:1,request_id:'a'.repeat(32),boot_id:'b'.repeat(32),profile:'plate',build:'browser-fixture',revision:1,event_id:'200',session_id:'browser-session',board:'plate-test',paused:true,capabilities:['pause_input','resume_input','configure_event','configure_destinations'],status:{reader_state:'unknown',pending:0,database_bytes:4096,clock_quality:'unknown'},results:[]}
  const status = await evaluate(`fetch('/api/edge/devices/${pair[0]}/heartbeat',{method:'POST',headers:{'Content-Type':'application/json',Authorization:'Bearer '+${JSON.stringify(pair[1])}},body:JSON.stringify(${JSON.stringify(heartbeat)})}).then(r=>r.status)`)
  assert.equal(status, 200)
  await send('Page.navigate', {url:base + '/edge-devices/' + pair[0]})
  await wait(`!!document.querySelector('[name="payload[event_id]"]')`, 'command forms')
  await ready()
  assert.equal(await evaluate(`document.documentElement.outerHTML.includes(${JSON.stringify(pair[1])})`), false)
  for (const width of [360,390,768]) {await viewport(width); await layout('device detail ' + width)}
  await viewport(390); await shot('detail-390')
  await evaluate(`(() => {const f=document.querySelector('[name="payload[event_id]"]').form;f.elements['payload[event_id]'].value='200';f.elements['payload[session_id]'].value='browser-new-session';f.elements['payload[timezone]'].value='UTC';f.requestSubmit();})()`)
  await wait(`document.body?.textContent.includes('Ожидает heartbeat')`, 'command queued')
  await evaluate(`document.querySelector('form[action$="/revoke"]').requestSubmit()`)
  await wait(`document.body?.textContent.includes('Ключ отозван') && !document.querySelector('form[action$="/commands"]')`, 'revoked controls hidden')
  assert.equal(exceptions.length, 0, 'uncaught browser exceptions: ' + exceptions.join(', '))
  console.log('Chrono management browser: login, phone menu, registration, telemetry display, command enqueue and revoke passed at 360/390/768 widths')
} finally {
  clearTimeout(watchdog)
  if (!closed) child.kill('SIGTERM')
  const kill = setTimeout(() => {if (!closed) child.kill('SIGKILL')}, 2000)
  await exited; clearTimeout(kill)
  await rm(profile, {recursive:true, force:true})
}
