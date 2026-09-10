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
  '--virtual-time-budget=' + (edgeFixture ? 10000 : 3000),
  '--dump-dom',
  `http://127.0.0.1:${address.port}`
]

const child = spawn(browser, browserArgs, {stdio: ['ignore', 'pipe', 'pipe']})
const watchdog = setTimeout(() => child.kill('SIGKILL'), 20000)
let documentHtml = ''
let browserLog = ''

child.stdout.setEncoding('utf8')
child.stderr.setEncoding('utf8')
child.stdout.on('data', chunk => { documentHtml += chunk })
child.stderr.on('data', chunk => { browserLog += chunk })

const exitCode = await new Promise((resolveExit, rejectExit) => {
  child.once('error', rejectExit)
  child.once('close', resolveExit)
})
clearTimeout(watchdog)

await new Promise(resolveClose => server.close(resolveClose))
rmSync(profile, {recursive: true, force: true})

if (exitCode !== 0) {
  throw new Error(`Chromium exited with ${exitCode}\n${browserLog}`)
}

if (!documentHtml.includes('data-chrono-desk-ready="true"')) {
  throw new Error(`Chrono Desk shell did not mount\n${browserLog}\nRequests: ${JSON.stringify(requests)}\n${documentHtml.slice(-8000)}`)
}

if (/Uncaught (Error|TypeError|SyntaxError)/.test(browserLog)) {
  throw new Error(`Uncaught frontend exception\n${browserLog}`)
}

if (edgeFixture) edgeFixture.verify(documentHtml)

console.log('Chrono Desk frontend ' + (edgeFixture ? 'edge save/start/stop' : 'runtime') + ' smoke passed')
