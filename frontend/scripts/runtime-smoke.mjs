import {spawn, spawnSync} from 'node:child_process'
import {createReadStream, existsSync, statSync, readFileSync, mkdtempSync, rmSync} from 'node:fs'
import {createServer} from 'node:http'
import {dirname, extname, join, normalize, resolve} from 'node:path'
import {fileURLToPath} from 'node:url'
import {tmpdir} from 'node:os'
import {edgeSmokeFixture, edgeSmokeBootstrap} from './edge-smoke.mjs'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const distDir = resolve(scriptDir, '..', 'dist')
const edgeFixture = process.argv.includes('--edge') ? edgeSmokeFixture() : null

if (!existsSync(join(distDir, 'index.html'))) {
  throw new Error('frontend/dist is missing; run npm run build first')
}

const configuredBrowser = process.env.CHRONO_DESK_BROWSER
const candidates = configuredBrowser
  ? [configuredBrowser]
  : [
      'google-chrome',
      'chromium',
      'chromium-browser',
      '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
    ]

const browser = candidates.find(candidate => {
  if (candidate.includes('/')) return existsSync(candidate)
  const result = spawnSync(candidate, ['--version'], {stdio: 'ignore'})
  return result.status === 0
})

if (!browser) {
  throw new Error('No supported Chromium browser found; set CHRONO_DESK_BROWSER')
}

const contentTypes = {
  '.css': 'text/css; charset=utf-8',
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.woff2': 'font/woff2'
}

const requests = []
const server = createServer(async (request, response) => {
  const requestedPath = new URL(request.url ?? '/', 'http://127.0.0.1').pathname
  requests.push(`${request.method} ${requestedPath}`)
  if (edgeFixture && await edgeFixture.handle(request, response, requestedPath)) return
  const relativePath = requestedPath === '/' ? 'index.html' : requestedPath.slice(1)
  const filePath = normalize(join(distDir, relativePath))

  if (!filePath.startsWith(`${distDir}/`) || !existsSync(filePath) || !statSync(filePath).isFile()) {
    response.writeHead(404)
    response.end('not found')
    return
  }

  response.writeHead(200, {'Content-Type': contentTypes[extname(filePath)] ?? 'application/octet-stream'})
  if (edgeFixture && relativePath === 'index.html') {
    response.end(readFileSync(filePath, 'utf8').replace('<head>', '<head>' + edgeSmokeBootstrap))
    return
  }
  createReadStream(filePath).pipe(response)
})

await new Promise((resolveListen, rejectListen) => {
  server.once('error', rejectListen)
  server.listen(0, '127.0.0.1', resolveListen)
})

const address = server.address()
if (!address || typeof address === 'string') {
  server.close()
  throw new Error('Could not determine frontend smoke-test address')
}

const profile = mkdtempSync(join(tmpdir(), 'chrono-desk-smoke-'))
const browserArgs = [
  '--headless',
  '--no-sandbox',
  '--disable-gpu',
  '--enable-logging=stderr',
  '--disable-background-networking',
  '--disable-component-update',
  '--disable-quic',
  '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1',
  // Non-local browser background requests terminate at this local test server;
  // the fixture server never proxies or forwards a request to the Internet.
  '--proxy-server=http://127.0.0.1:' + address.port,
  '--proxy-bypass-list=127.0.0.1;localhost',
  '--no-first-run',
  '--user-data-dir=' + profile,
  '--remote-debugging-pipe',
  'about:blank'
]

// Inspect completion explicitly. Full Chrome need not terminate a --dump-dom
// process when the SPA has completed its asynchronous HTTP/UI actions.
// The inherited pipes need no WebSocket dependency or listening debug port.
const child = spawn(browser, browserArgs, {stdio: ['ignore', 'ignore', 'pipe', 'pipe', 'pipe']})
let documentHtml = ''
let browserLog = ''
let closed = false, nextID = 0, session, input = ''
const pending = new Map(), exceptions = []
const delay = ms => new Promise(resolveDelay => setTimeout(resolveDelay, ms))
const failPending = error => {
  for (const call of pending.values()) { clearTimeout(call.timer); call.reject(error) }
  pending.clear()
}
const exited = new Promise(resolveExit => {
  child.once('close', (code, signal) => {
    closed = true
    failPending(new Error(`Chromium closed before command completion: ${code}/${signal}`))
    resolveExit()
  })
})
child.once('error', failPending)
child.stdio[3].on('error', failPending)
child.stdio[4].on('error', failPending)
child.stderr.setEncoding('utf8')
child.stderr.on('data', chunk => { browserLog = (browserLog + chunk).slice(-16000) })
child.stdio[4].setEncoding('utf8')
child.stdio[4].on('data', chunk => {
  input += chunk
  for (let end; (end = input.indexOf('\0')) !== -1;) {
    const frame = input.slice(0, end)
    input = input.slice(end + 1)
    if (!frame) continue
    let message
    try { message = JSON.parse(frame) } catch (error) { failPending(error); continue }
    if (message.method === 'Runtime.exceptionThrown') {
      exceptions.push(message.params.exceptionDetails.exception?.description ?? message.params.exceptionDetails.text)
    }
    const call = pending.get(message.id)
    if (!call) continue
    pending.delete(message.id); clearTimeout(call.timer)
    if (message.error) call.reject(new Error(message.error.message)); else call.resolve(message.result)
  }
})
const send = (method, params = {}, targetSession = session) => new Promise((resolveCall, rejectCall) => {
  if (closed) { rejectCall(new Error('Chromium already closed')); return }
  const id = ++nextID
  const timer = setTimeout(() => {
    pending.delete(id); rejectCall(new Error(`Chromium command timeout: ${method}`))
  }, 5000)
  pending.set(id, {resolve: resolveCall, reject: rejectCall, timer})
  child.stdio[3].write(JSON.stringify({id, method, params, ...(targetSession ? {sessionId: targetSession} : {})}) + '\0')
})
const evaluate = async expression => {
  const result = await send('Runtime.evaluate', {expression, returnByValue: true})
  if (result.exceptionDetails) throw new Error(result.exceptionDetails.exception?.description ?? result.exceptionDetails.text)
  return result.result.value
}

try {
  const {targetId} = await send('Target.createTarget', {url: 'about:blank'})
  session = (await send('Target.attachToTarget', {targetId, flatten: true})).sessionId
  await send('Page.enable')
  await send('Runtime.enable')
  await send('Page.navigate', {url: `http://127.0.0.1:${address.port}`})
  const deadline = Date.now() + 15000
  while (Date.now() < deadline) {
    const state = await evaluate(`({ready: !!document.querySelector('[data-chrono-desk-ready="true"]'), edge: document.body?.dataset.edgeSmoke, error: document.body?.dataset.edgeSmokeError})`)
    if (exceptions.length || state.edge === 'failed') throw new Error(state.error || exceptions.join('\n'))
    if (state.ready && (!edgeFixture || state.edge === 'passed')) {
      documentHtml = await evaluate('document.documentElement.outerHTML')
      break
    }
    await delay(50)
  }
  if (!documentHtml) throw new Error('Timed out waiting for the rendered shell/UI actions')
} catch (error) {
  throw new Error(`${error.message}\n${browserLog}\nRequests: ${JSON.stringify(requests)}`)
} finally {
  // Join only this smoke's browser before removing its private profile.
  if (!closed) child.kill('SIGTERM')
  const killTimer = setTimeout(() => { if (!closed) child.kill('SIGKILL') }, 2000)
  await exited
  clearTimeout(killTimer)
  server.closeAllConnections()
  await new Promise(resolveClose => server.close(resolveClose))
  rmSync(profile, {recursive: true, force: true})
}

if (!documentHtml.includes('data-chrono-desk-ready="true"')) {
  throw new Error(`Chrono Desk shell did not mount\n${browserLog}\nRequests: ${JSON.stringify(requests)}\n${documentHtml.slice(-8000)}`)
}

if (exceptions.length || /Uncaught (Error|TypeError|SyntaxError)/.test(browserLog)) {
  throw new Error(`Uncaught frontend exception\n${exceptions.join('\n')}\n${browserLog}`)
}

if (edgeFixture) edgeFixture.verify(documentHtml)

console.log('Chrono Desk frontend ' + (edgeFixture ? 'edge save/start/stop' : 'runtime') + ' smoke passed')
