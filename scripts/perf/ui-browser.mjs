// Task: CHR-DESK-PERF-001; Docs-Impact: LOCAL
// Synthetic names and isolated GET-only copy; two RAFs estimate a frame opportunity, not physical paint.
import {spawn} from 'node:child_process'
import {readFileSync,existsSync,createReadStream,mkdtempSync,rmSync,writeFileSync} from 'node:fs'
import {createServer} from 'node:http'
import {join,normalize,extname,resolve,dirname} from 'node:path'
import {fileURLToPath} from 'node:url'
if (!process.argv[2] || !process.env.CHRONO_DESK_BROWSER) throw Error('Usage: CHRONO_DESK_BROWSER=/path/to/chromium node scripts/perf/ui-browser.mjs /private/serve-ui-copy')
const root=resolve(process.argv[2])
const assertLazy=process.argv.includes('--lazy-history')
const assertSlowLive=process.argv.includes('--slow-live')
const meta=JSON.parse(readFileSync(root+'/server.log','utf8').trim())
const distDir=resolve(dirname(fileURLToPath(import.meta.url)), '../../frontend/dist')
const bootstrap=`<script>window.go={main:{App:{APIBaseURL:async()=>${JSON.stringify(meta.base)},APIToken:async()=> 'isolated-performance-probe'}}};window.__http=[];const original=window.fetch;window.fetch=async(...args)=>{if((args[1]?.method||'GET')!=='GET')throw Error('Diagnostic browser permits GET only');const start=performance.now();let res=await original(...args);const json=res.json.bind(res);res.json=async()=>{const data=await json();window.__http.push({path:new URL(args[0],location.href).pathname+new URL(args[0],location.href).search,start,end:performance.now()});return data};return res};</script>`
const slowLiveBootstrap = assertSlowLive ? `<script>
window.__liveProbe = {enabled:false, calls:{}, active:{}, max:{}, aborted:0};
const liveFetch = window.fetch;
window.fetch = async (...args) => {
  const probe = window.__liveProbe, path = new URL(args[0],location.href).pathname;
  if (!probe.enabled || !/\\/(photos\\/(recent|merged|status)|live\\/feed)$/.test(path)) return liveFetch(...args);
  probe.calls[path]=(probe.calls[path]||0)+1;
  probe.active[path]=(probe.active[path]||0)+1;
  probe.max[path]=Math.max(probe.max[path]||0,probe.active[path]);
  try {
    if (path.endsWith('/photos/recent')) throw Error('Synthetic early photo metadata failure');
    await new Promise((resolve,reject)=>{
      const signal=args[1]?.signal;
      const abort=()=>{clearTimeout(timer);probe.aborted++;reject(new DOMException('View closed','AbortError'))};
      const timer=setTimeout(()=>{signal?.removeEventListener('abort',abort);resolve()},path.includes('/photos/')?8000:50);
      if(signal?.aborted)abort();else signal?.addEventListener('abort',abort,{once:true});
    });
    return await liveFetch(...args);
  } finally {probe.active[path]--}
};
</script>` : ''
const server=createServer((req,res)=>{const name=new URL(req.url,'http://localhost').pathname;const p=normalize(join(distDir,name==='/'?'index.html':name));if(!p.startsWith(distDir+'/')||!existsSync(p)){res.writeHead(404);res.end();return}res.setHeader('Content-Type',({'.html':'text/html','.js':'text/javascript','.css':'text/css','.woff2':'font/woff2'})[extname(p)]||'application/octet-stream');if(name==='/'){res.end(readFileSync(p,'utf8').replace('<head>','<head>'+bootstrap+slowLiveBootstrap));return}createReadStream(p).pipe(res)})
await new Promise(r=>server.listen(0,'127.0.0.1',r));const address=server.address()
const profile=mkdtempSync(root+'/browser-')
const browser=process.env.CHRONO_DESK_BROWSER
const browserArgs=['--headless','--no-sandbox','--disable-gpu','--disable-background-networking','--disable-component-update','--no-first-run','--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1','--user-data-dir='+profile,'--remote-debugging-pipe','about:blank']
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
const send = (method, params = {}, targetSession = session, timeoutMs = 5000) => new Promise((resolveCall, rejectCall) => {
  if (closed) { rejectCall(new Error('Chromium already closed')); return }
  const id = ++nextID
  const timer = setTimeout(() => {
    pending.delete(id); rejectCall(new Error(`Chromium command timeout: ${method} after ${timeoutMs}ms`))
  }, timeoutMs)
  pending.set(id, {resolve: resolveCall, reject: rejectCall, timer})
  child.stdio[3].write(JSON.stringify({id, method, params, ...(targetSession ? {sessionId: targetSession} : {})}) + '\0')
})
const evaluate = async expression => {
  const result = await send('Runtime.evaluate', {expression, returnByValue: true})
  if (result.exceptionDetails) throw new Error(result.exceptionDetails.exception?.description ?? result.exceptionDetails.text)
  return result.result.value
}

const waitFor=async(expression)=>{const until=Date.now()+12000;while(Date.now()<until){if(await evaluate(expression))return;await delay(20)}throw Error('UI condition timed out: '+expression)}
try{
 const {targetId}=await send('Target.createTarget',{url:'about:blank'},undefined,30000)
 session=(await send('Target.attachToTarget',{targetId,flatten:true})).sessionId
 await send('Page.enable');await send('Runtime.enable');await send('Page.navigate',{url:`http://127.0.0.1:${address.port}`})
 await waitFor("!!document.querySelector('.event')")
 await evaluate("document.querySelector('.event').click()")
 await waitFor("!!document.querySelector('.bar .search input')")
 const measures=[]
 for(let n=0;n<15;n++){
  await evaluate(`(()=>{const x=document.querySelector('.bar .search input');x.value=${JSON.stringify(meta.member)};x.dispatchEvent(new Event('input',{bubbles:true}))})()`)
  await waitFor("!!document.querySelector('.bar .search .match')")
  await evaluate(`(()=>{window.__http=[];window.__measure={start:performance.now(),shell:null,fields:null,paint:null};const o=new MutationObserver(()=>{const m=window.__measure;if(m.shell===null&&document.querySelector('.drawer'))m.shell=performance.now();if(m.fields===null&&document.querySelector('.drawer .reg')){m.fields=performance.now();requestAnimationFrame(()=>requestAnimationFrame(()=>{m.paint=performance.now()}));o.disconnect()}});o.observe(document.body,{subtree:true,childList:true});const b=[...document.querySelectorAll('.bar .search .match')].find(x=>x.querySelector('.mname').textContent.trim()===${JSON.stringify(meta.member+' Participant')});if(!b)throw Error('Expected synthetic member missing');b.click()})()`)
  await waitFor('window.__measure?.paint!==null')
  const m=await evaluate('({...window.__measure,rows:document.querySelectorAll(".drawer .srow").length,requests:window.__http})');measures.push(m)
  if(assertLazy){
   const passes=m.requests.filter(r=>r.path.includes('/passes'))
   if(passes.length!==1||!passes[0].path.endsWith('?scope=accepted'))throw Error('Initial drawer fetched full history')
   if(n===0){
    await evaluate("document.querySelector('.onlyhits input').click()")
    await waitFor("window.__http.some(r=>r.path.endsWith('/passes?scope=all'))")
    const allRows=await evaluate("document.querySelectorAll('.drawer .srow').length")
    if(allRows<m.rows)throw Error('Full history lost accepted rows')
    await evaluate("document.querySelector('.onlyhits input').click()")
    await waitFor(`document.querySelectorAll('.drawer .srow').length===${m.rows}`)
    await evaluate("document.querySelector('.onlyhits input').click()")
    await waitFor(`document.querySelectorAll('.drawer .srow').length===${allRows}`)
    if(await evaluate("window.__http.filter(r=>r.path.endsWith('/passes?scope=all')).length")!==1)throw Error('Cached history fetched again')
    m.lazy_history={initial_rows:m.rows,expanded_rows:allRows,full_requests:1}
   }
  }
  await evaluate("document.querySelector('.drawer .x').click()")
  await waitFor("!document.querySelector('.drawer')")
 }
 let liveProbe = null
 if(assertSlowLive){
  await evaluate("window.__liveProbe.enabled=true;document.querySelector('button.live').click()")
  await waitFor("document.querySelectorAll('.row:not(.capture)').length>0")
  if(!await evaluate("Object.entries(window.__liveProbe.active).some(([path,count])=>path.endsWith('/photos/merged')&&count===1)"))throw Error('Feed was blocked by slow photo metadata')
  await delay(4300)
  liveProbe=await evaluate('structuredClone(window.__liveProbe)')
  const feeds=Object.entries(liveProbe.calls).find(([path])=>path.endsWith('/live/feed'))?.[1]
  if(feeds<3)throw Error('Slow photos blocked repeated RFID refreshes')
  for(const [path,count] of Object.entries(liveProbe.calls))if(path.includes('/photos/')&&count!==1)throw Error('Queued photo polling overlapped after an early failure')
  if(Object.values(liveProbe.max).some(count=>count>1))throw Error('Live display requests overlapped')
  await evaluate("[...document.querySelectorAll('.seg-item')].find(button=>button.textContent==='Результаты').click()")
  await waitFor('Object.values(window.__liveProbe.active).every(count=>count===0)')
  const count=await evaluate('JSON.stringify(window.__liveProbe.calls)')
  await delay(2200)
  if(count!==await evaluate('JSON.stringify(window.__liveProbe.calls)'))throw Error('Destroyed Live view kept polling')
  liveProbe=await evaluate('structuredClone(window.__liveProbe)')
  if(liveProbe.aborted!==2)throw Error('Closing Live did not cancel both slow metadata requests')
 }
 const output={scope:'Linux headless Chromium; exact frontend and real localhost API on sanitized event772963; no photos, reader or background replay; not Mac WebKit',samples:measures,exceptions,liveProbe}
 writeFileSync(root+'/browser-result.json',JSON.stringify(output,null,2),{mode:0o600})
 const values=measures.map(m=>m.paint-m.start).sort((a,b)=>a-b)
 console.log(JSON.stringify({samples:values.length,two_raf_p50_ms:values[7],two_raf_p95_ms:values[13],two_raf_max_ms:values[14],exceptions}))
}finally{
 if(!closed)child.kill('SIGTERM');const timer=setTimeout(()=>{if(!closed)child.kill('SIGKILL')},2000);await exited;clearTimeout(timer);server.closeAllConnections();await new Promise(r=>server.close(r));rmSync(profile,{recursive:true,force:true})
}
