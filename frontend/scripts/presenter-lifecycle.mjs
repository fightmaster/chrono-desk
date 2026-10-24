import assert from 'node:assert/strict'

// Real DOM with controlled transports: exercise compatibility and callbacks
// which an ordinary browser cannot reliably deliver after a source is closed.
export async function checkPresenterLifecycle(client) {
  const result = await client.evaluate(`(async () => {
    const {createFinishFeed} = await import('/live.js');
    const root = document.createElement('section');document.body.append(root);
    const delay = ms => new Promise(resolve => setTimeout(resolve,ms));
    const check = (condition,message) => {if(!condition) throw new Error(message)};
    const until = async (condition,timeout=2000) => {
      const end=Date.now()+timeout;
      while(Date.now()<end) {if(condition()) return;await delay(5)}
      throw new Error('lifecycle condition timeout');
    };
    const base={published:true,filtered:false,event_key:'controlled',event_name:'Initial',event_date:'2026-10-25',rows:[],races:[{id:'one',name:'Race'}]};
    let legacyReads=0;
    const oldFeed=createFinishFeed({mount:root,onMeta:()=>{},onTab:()=>{},interval:5,
      fetcher:async()=>{legacyReads++;return new Response(JSON.stringify(base))},
      streamFactory:()=>{throw new Error('old server unexpectedly selected SSE')},
    });
    oldFeed.start();await until(()=>legacyReads>=3);oldFeed.stop();
    const stoppedLegacyReads=legacyReads;await delay(30);
    check(legacyReads===stoppedLegacyReads,'stopped legacy feed kept polling');
    const sources=[];
    class Source extends EventTarget {
      constructor(){super();this.closed=false;this.readyState=1;sources.push(this)}
      close(){this.closed=true;this.readyState=2}
      emit(kind,data){this.dispatchEvent(new MessageEvent(kind,{data}))}
    }
    let payload={...base,stream_version:1},reads=0,meta=[];
    const feed=createFinishFeed({mount:root,onMeta:data=>meta.push(data.event_name),onTab:()=>{},interval:5,
      fetcher:async()=>{reads++;return new Response(JSON.stringify(payload))},
      streamFactory:()=>new Source(),
    });
    try {
      feed.start();await until(()=>sources.length===1);
      const first=sources[0];first.onopen?.();
      first.emit('snapshot',JSON.stringify(payload));
      const initialMeta=meta.length;
      for(let i=0;i<10;i++) first.emit('heartbeat','{}');
      await delay(30);
      check(reads===1,'healthy SSE retained a polling loop');
      check(meta.length===initialMeta,'heartbeat or duplicate snapshot rerendered');
      first.readyState=0;first.onerror?.();
      check(root.querySelector('#finish-state').classList.contains('disconnected'),'error did not mark stale data');
      await delay(30);check(reads===1,'recoverable error competed with native reconnect');
      first.readyState=1;first.onopen?.();first.emit('snapshot',JSON.stringify(payload));
      payload={...payload,event_name:'Fresh GET'};
      root.querySelector('#finish-refresh').click();await until(()=>sources.length===2);
      check(first.closed,'Refresh left old EventSource open');
      first.emit('snapshot',JSON.stringify({...payload,event_name:'Stale callback'}));
      check(meta.at(-1)==='Fresh GET','closed-source callback replaced fresh data');
      const second=sources[1];second.emit('snapshot','{malformed');
      await until(()=>sources.length===3,3000);
      check(second.closed,'malformed frame retained its source');
      check(sources.filter(source=>!source.closed).length===1,'multiple live sources');
      const beforeWatchdog=reads;
      // A silent connection can remain OPEN indefinitely without an error.
      // The real15s watchdog must replace it with a fresh GET/source.
      await until(()=>reads>beforeWatchdog,18000);
      check(sources[2].closed,'stale watchdog did not close the old source');
      await until(()=>sources.length===4);
      const beforeHidden=reads;
      Object.defineProperty(document,'hidden',{configurable:true,value:true});
      document.dispatchEvent(new Event('visibilitychange'));
      await delay(40);
      check(reads===beforeHidden,'hidden controlled feed made requests');
      check(sources.at(-1).closed,'hidden controlled feed kept streaming');
      delete document.hidden;document.dispatchEvent(new Event('visibilitychange'));
      await until(()=>sources.length===5);
      feed.stop();const stoppedReads=reads;await delay(30);
      check(reads===stoppedReads,'stopped SSE feed made requests');
      check(sources.every(source=>source.closed),'stopped feed retained EventSource');
      for(let i=0;i<10;i++) {
        const count=sources.length;
        feed.start();await until(()=>sources.length>count);feed.stop();
      }
      check(sources.every(source=>source.closed),'repeated navigation retained sources');
      return {legacyReads,streamSources:sources.length,checks:['old server fallback','heartbeat no render','native reconnect ownership','stale source callback','forced GET refresh','malformed frame','15s watchdog','hidden pause','repeat navigation cleanup']};
    } finally {
      feed.stop();oldFeed.stop();root.remove();
      if(Object.hasOwn(document,'hidden')) {delete document.hidden;document.dispatchEvent(new Event('visibilitychange'))}
    }
  })()`,25000)
  assert.ok(result.legacyReads >= 3)
  assert.ok(result.streamSources >= 15)
  return result
}
