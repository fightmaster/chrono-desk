import {spawn, spawnSync} from 'node:child_process'
import {createReadStream, existsSync, statSync} from 'node:fs'
import {createServer} from 'node:http'
import {dirname, extname, join, normalize, resolve} from 'node:path'
import {fileURLToPath} from 'node:url'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const distDir = resolve(scriptDir, '..', 'dist')

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

const server = createServer((request, response) => {
  const requestedPath = new URL(request.url ?? '/', 'http://127.0.0.1').pathname
  const relativePath = requestedPath === '/' ? 'index.html' : requestedPath.slice(1)
  const filePath = normalize(join(distDir, relativePath))

  if (!filePath.startsWith(`${distDir}/`) || !existsSync(filePath) || !statSync(filePath).isFile()) {
    response.writeHead(404)
    response.end('not found')
    return
  }

  response.writeHead(200, {'Content-Type': contentTypes[extname(filePath)] ?? 'application/octet-stream'})
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

const browserArgs = [
  '--headless',
  '--no-sandbox',
  '--disable-gpu',
  '--enable-logging=stderr',
  '--virtual-time-budget=3000',
  '--dump-dom',
  `http://127.0.0.1:${address.port}`
]

const child = spawn(browser, browserArgs, {stdio: ['ignore', 'pipe', 'pipe']})
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

await new Promise(resolveClose => server.close(resolveClose))

if (exitCode !== 0) {
  throw new Error(`Chromium exited with ${exitCode}\n${browserLog}`)
}

if (!documentHtml.includes('data-chrono-desk-ready="true"')) {
  throw new Error(`Chrono Desk shell did not mount\n${browserLog}`)
}

if (/Uncaught (Error|TypeError|SyntaxError)/.test(browserLog)) {
  throw new Error(`Uncaught frontend exception\n${browserLog}`)
}

console.log('Chrono Desk frontend runtime smoke passed')
