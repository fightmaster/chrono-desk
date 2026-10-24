import {spawn} from 'node:child_process'
import {mkdtempSync, rmSync, writeFileSync} from 'node:fs'
import {tmpdir} from 'node:os'
import {join} from 'node:path'
import assert from 'node:assert/strict'
import {checkPresenterLifecycle} from './presenter-lifecycle.mjs'

const url = process.argv[2]
const delay = ms => new Promise(resolve => setTimeout(resolve, ms))
const clients = []
async function openClient() {
  const profile = mkdtempSync(join(tmpdir(), 'presenter-smoke-'))
  const child = spawn(process.env.CHRONO_DESK_BROWSER, ['--headless','--no-sandbox','--disable-gpu','--disable-background-networking','--no-first-run','--remote-debugging-pipe', '--user-data-dir='+profile, 'about:blank'], {stdio:['ignore','ignore','pipe','pipe','pipe']})
  let input='', id=0, session, log=''
  const exited=new Promise(resolve=>child.once('close',resolve))
  const pending=new Map(), exceptions=[]
  child.stderr.on('data', chunk => {log=(log+chunk).slice(-4000)})
  child.stdio[4].setEncoding('utf8')
  child.stdio[4].on('data', chunk => {
    input+=chunk
    for(let end;(end=input.indexOf('\0'))!==-1;) {
      const message=JSON.parse(input.slice(0,end));input=input.slice(end+1)
      if(message.method==='Runtime.exceptionThrown') exceptions.push(message.params.exceptionDetails)
      const call=pending.get(message.id)
      if(call) {pending.delete(message.id);clearTimeout(call.timer);message.error?call.reject(new Error(message.error.message)):call.resolve(message.result)}
    }
  })
  const send=(method,params={},timeout=10000)=>new Promise((resolve,reject)=>{
    const requestID=++id
    const timer=setTimeout(()=>{pending.delete(requestID);reject(new Error(method+' timeout '+log))},timeout)
    pending.set(requestID,{resolve,reject,timer})
    child.stdio[3].write(JSON.stringify({id:requestID,method,params,...(session?{sessionId:session}:{})})+'\0')
  })
  const evaluate=async (expression,timeout=10000)=>{
    const result=await send('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true},timeout)
    if(result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails))
    return result.result.value
  }
  const client={send,evaluate,exceptions,async close(){
    child.kill('SIGTERM');const killer=setTimeout(()=>child.kill('SIGKILL'),2000);await exited;clearTimeout(killer);rmSync(profile,{recursive:true,force:true})
  }}
  clients.push(client)
  const {targetId}=await send('Target.createTarget',{url:'about:blank'})
  session=(await send('Target.attachToTarget',{targetId,flatten:true})).sessionId
  await send('Page.enable');await send('Runtime.enable')
  await send('Page.addScriptToEvaluateOnNewDocument',{source:`
    window.__streamStats={count:0,active:0,max:0};const NativeEventSource=window.EventSource;
    window.EventSource=class extends NativeEventSource {
      constructor(url,options) { super(url,options);const stats=window.__streamStats;stats.count++;stats.active++;stats.max=Math.max(stats.max,stats.active);this.trackedClosed=false; }
      close() {if(!this.trackedClosed){this.trackedClosed=true;window.__streamStats.active--;}super.close();}
    };
    window.__feedStats={count:0,active:0,max:0,legacy:0};const originalFetch=window.fetch.bind(window);
    window.fetch=async (url,options)=>{
      const stats=window.__feedStats;
      if(!String(url).startsWith('/api/live/finishes')) {stats.legacy++;return originalFetch(url,options)}
      stats.count++;stats.active++;stats.max=Math.max(stats.max,stats.active);
      try {
        if(String(url).includes('race_id=race-empty')) {
          const response=await originalFetch(url,{...options,signal:undefined});
          await new Promise(resolve=>setTimeout(resolve,700));return response;
        }
        return await originalFetch(url,options);
      } finally {stats.active--}
    }`})
  await send('Emulation.setDeviceMetricsOverride',{width:1024,height:768,deviceScaleFactor:1,mobile:false})
  await send('Page.navigate',{url:url+'/live'})
  return client
}
async function until(client,expression,timeout=5000) {
  const deadline=performance.now()+timeout
  while(performance.now()<deadline) {if(await client.evaluate(expression)) return;await delay(15)}
  throw new Error('Timed out: '+expression+' '+JSON.stringify({exceptions:client.exceptions,html:await client.evaluate('document.querySelector("#app")?.outerHTML')}))
}
async function change(path) {const response=await fetch(url+path,{method:'POST'});assert.equal(response.status,204);return Number(response.headers.get('X-Fixture-Commit-Ms'))}
try {
  await Promise.all([openClient(),openClient(),openClient()])
  for(const c of clients) await until(c,`document.querySelectorAll('.finish-card').length===10`)
  assert.equal(await clients[0].evaluate(`document.querySelector('.finish-team').textContent`),'Команда: Команда')
  assert.equal(await clients[0].evaluate(`document.querySelector('.finish-city').textContent`),'Город')
  assert.equal(await clients[0].evaluate(`document.querySelector('.finish-places').textContent`),'Мужчины: 60 · Группа M18-39: 60')
  assert.equal(await clients[0].evaluate(`document.querySelector('.finish-club').textContent`),'Клуб: Беговой клуб')
  if (process.env.CHRONO_PRESENTER_SCREENSHOT) {
    const {data}=await clients[0].send('Page.captureScreenshot',{format:'png'})
    writeFileSync(process.env.CHRONO_PRESENTER_SCREENSHOT,Buffer.from(data,'base64'))
  }
  for(const c of clients) {
    await until(c,`window.__streamStats.active===1`)
    await c.evaluate(`window.__cardMutations=0;new MutationObserver(m=>window.__cardMutations+=m.length).observe(document.querySelector('#finish-list'),{subtree:true,childList:true,characterData:true})`)
    const reads=await c.evaluate('window.__feedStats.count')
    await delay(250)
    assert.equal(await c.evaluate('window.__feedStats.count'),reads,'idle SSE polled JSON')
    assert.equal(await c.evaluate('window.__cardMutations'),0,'heartbeats redrew cards')
  }
  const latencies=[], captureLatencies=[]
  for(let number=1000;number<1100;number++) {
    const start=performance.now()
    const committed=await change('/fixture/finish?number='+number)
    await Promise.all(clients.map(async c=>{await until(c,`document.querySelector('.finish-number')?.textContent==='№${number}'`);await c.evaluate('new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve(true))))');latencies.push(Date.now()-committed);captureLatencies.push(performance.now()-start)}))
    await delay(31)
  }
  const c=clients[0]
  await change('/fixture/finish?number=1099')
  await until(c,`document.querySelector('.finish-name')?.textContent==='Исправлен Тест'`)
  assert.equal(await c.evaluate(`document.querySelectorAll('.finish-card').length`),10)
  assert.equal(await c.evaluate(`new Set([...document.querySelectorAll('.finish-card')].map(x=>x.dataset.key)).size`),10)
  await change('/fixture/revoke?number=1099')
  await until(c,`![...document.querySelectorAll('.finish-number')].some(x=>x.textContent==='№1099')`)
  // Manual browsing must retain its anchor while new finishes arrive above it.
  await c.evaluate('window.scrollTo(0,350)')
  const anchor=await c.evaluate(`(()=>{const n=[...document.querySelectorAll('.finish-card')].find(x=>x.getBoundingClientRect().bottom>0);return {key:n.dataset.key,top:n.getBoundingClientRect().top}})()`)
  await change('/fixture/finish?number=2020')
  await until(c,`[...document.querySelectorAll('.finish-number')].some(x=>x.textContent==='№2020')`)
  const top=await c.evaluate(`document.querySelector('[data-key="${anchor.key}"]').getBoundingClientRect().top`)
  assert.ok(Math.abs(top-anchor.top)<3, 'scroll anchor moved')
  assert.equal(await c.evaluate(`document.querySelector('#finish-jump').hidden`),false)
  await change('/fixture/club?number=2020')
  await c.evaluate(`document.querySelector('#finish-refresh').click()`)
  await until(c,`document.querySelector('.finish-club')?.textContent==='Клуб: Обновлённый клуб'`)
  assert.equal(await c.evaluate('window.__streamStats.active'),1,'refresh leaked EventSource')
  await c.evaluate(`window.scrollTo(0,0);document.querySelector('#finish-race').value='race-10k';document.querySelector('#finish-race').dispatchEvent(new Event('change'))`)
  await until(c,`document.querySelectorAll('.finish-card').length===10`)
  // Deliberately return an obsolete filtered response even after cancellation.
  await c.evaluate(`document.querySelector('#finish-race').value='race-empty';document.querySelector('#finish-race').dispatchEvent(new Event('change'))`)
  await until(c,`window.__feedStats.active===1`)
  await c.evaluate(`document.querySelector('#finish-race').value='race-10k';document.querySelector('#finish-race').dispatchEvent(new Event('change'))`)
  await until(c,`document.querySelectorAll('.finish-card').length===10`)
  assert.equal(await c.evaluate('window.__feedStats.max'),1,'overlapping requests')
  assert.equal(await c.evaluate('window.__feedStats.legacy'),0,'live page loaded ranked protocols')
  await c.evaluate(`Object.defineProperty(document,'hidden',{configurable:true,value:true});document.dispatchEvent(new Event('visibilitychange'))`)
  await delay(100)
  assert.equal(await c.evaluate('window.__streamStats.active'),0,'hidden tab retained SSE')
  const pausedCount=await c.evaluate('window.__feedStats.count')
  await delay(650)
  assert.equal(await c.evaluate('window.__feedStats.count'),pausedCount,'hidden tab kept polling')
  await c.evaluate(`delete document.hidden;document.dispatchEvent(new Event('visibilitychange'))`)
  await until(c,`window.__feedStats.count>${pausedCount}`)
  await c.send('Network.enable')
  await c.send('Network.emulateNetworkConditions',{offline:true,latency:0,downloadThroughput:-1,uploadThroughput:-1})
  await until(c,`document.querySelector('#finish-state').classList.contains('disconnected')`)
  assert.equal(await c.evaluate(`document.querySelectorAll('.finish-card').length`),10)
  await c.send('Network.emulateNetworkConditions',{offline:false,latency:0,downloadThroughput:-1,uploadThroughput:-1})
  await c.evaluate(`window.dispatchEvent(new Event('online'))`)
  await until(c,`!document.querySelector('#finish-state').classList.contains('disconnected')`)
  assert.equal(await c.evaluate(`document.querySelectorAll('.finish-new').length`),0,'reconnect highlighted history')
  await change('/fixture/switch')
  await until(c,`document.querySelectorAll('.finish-card').length===1 && document.querySelector('.finish-name')?.textContent==='Другое событие Ivan'`)
  assert.equal(await c.evaluate(`document.querySelector('#finish-race').value`),'','event switch retained stale filter')
  await c.evaluate(`document.querySelector('[data-tab="protocol"]').click()`)
  await until(c,`document.querySelector('[data-tab="protocol"]')?.classList.contains('active') && !document.querySelector('#finish-state')`)
  assert.equal(await c.evaluate('window.__streamStats.active'),0,'Protocol retained Live stream')
  await c.evaluate(`document.querySelector('[data-tab="live"]').click()`)
  await until(c,`document.querySelectorAll('.finish-card').length===1`)
  assert.equal(await c.evaluate('location.pathname'),'/live')
  const lifecycle=await checkPresenterLifecycle(c)
  console.log(JSON.stringify({lifecycle}))
  for(const client of clients) {assert.deepEqual(client.exceptions,[]);assert.equal(await client.evaluate('window.__streamStats.max'),1,'overlapping EventSources')}
  latencies.sort((a,b)=>a-b)
  const p95=latencies[Math.ceil(latencies.length*.95)-1]
  console.log(JSON.stringify({clients:3,samples:latencies.length,syntheticObservationTargetWritesPerSecond:100,p95_ms:Math.round(p95),max_ms:Math.round(latencies.at(-1)),capture_to_dom_p95_ms:Math.round(captureLatencies.sort((a,b)=>a-b)[Math.ceil(captureLatencies.length*.95)-1]),checks:['SSE idle/no redraw','force refresh','one EventSource','deduplication','correction','revocation','filter','scroll','offline/reconnect','event switch','legacy tab roundtrip','stale filter response','visibility pause','one in flight','no ranked protocol queries','no runtime errors']}))
  assert.ok(p95<=1000,`p95 ${p95}ms exceeds 1000ms`)
} finally {await Promise.all(clients.map(c=>c.close()))}
