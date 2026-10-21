import {progressSmokeFixture} from './progress-smoke.mjs'

export function checkpointWindowSmokeFixture() {
  const base = progressSmokeFixture()
  const cp = {id:'cp1', race_id:'r1', name:'Старт', type:1, sort:1, board:'B', since_offset_seconds:0, until_ms:null, until_offset_seconds:null, read_selection:'first'}
  let writes = 0
  return {
    async handle(request,response,path) {
      if (path.endsWith('/checkpoints') && request.method === 'GET') {
        response.setHeader('Content-Type','application/json');response.end(JSON.stringify([cp]));return true
      }
      if (path.endsWith('/edits') && request.method === 'POST') {
        let body='';for await (const chunk of request) body+=chunk
        const edit=JSON.parse(body)
        if (request.headers.authorization!=='Bearer progress-smoke-key' || edit.entity!=='checkpoint' || edit.entity_id!=='cp1' || !['until_offset_seconds','read_selection'].includes(edit.field)) throw Error('Unexpected checkpoint edit')
        cp[edit.field]=edit.value;writes++
        response.setHeader('Content-Type','application/json');response.end('{"recount_needed":false}');return true
      }
      return base.handle(request,response,path)
    },
    verify(html) {
      if (!html.includes('data-window-smoke="passed"') || writes!==2 || cp.until_offset_seconds!==300 || cp.read_selection!=='last') throw Error('Checkpoint window controls did not persist the expected edits')
    }
  }
}

export const checkpointWindowSmokeBootstrap = `<script>
window.go={main:{App:{APIBaseURL:async()=>location.origin,APIToken:async()=> 'progress-smoke-key'}}};
window.__windowEdits=0;const originalFetch=window.fetch;window.fetch=async(...args)=>{const response=await originalFetch(...args);if(String(args[0]).endsWith('/edits')&&response.ok)window.__windowEdits++;return response};
window.addEventListener('DOMContentLoaded',async()=>{
 const wait=async fn=>{const end=Date.now()+10000;while(Date.now()<end){const value=fn();if(value)return value;await new Promise(r=>setTimeout(r,15))}throw Error('Checkpoint controls timeout')};
 try {
  (await wait(()=>document.querySelector('button.event'))).click();
  (await wait(()=>[...document.querySelectorAll('button')].find(b=>b.textContent.trim()==='Настройки дистанции'))).click();
  const bound=await wait(()=>document.querySelector('input[aria-label="Учитывать до от старта"]'));
  bound.value='300';bound.dispatchEvent(new Event('change',{bubbles:true}));
  await wait(()=>window.__windowEdits===1);
  const selection=await wait(()=>document.querySelector('select[aria-label="Выбирать чтение"]'));
  selection.value='last';selection.dispatchEvent(new Event('change',{bubbles:true}));
  await wait(()=>window.__windowEdits===2);
  if(document.querySelector('.error'))throw Error(document.querySelector('.error').textContent);
  document.body.dataset.windowSmoke='passed';
 } catch(e){document.body.dataset.windowSmoke='failed';document.body.dataset.windowSmokeError=String(e)}
});
</script>`
