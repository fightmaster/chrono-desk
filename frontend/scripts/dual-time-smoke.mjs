import {progressSmokeFixture} from './progress-smoke.mjs'

export function dualTimeSmokeFixture() {
  const base = progressSmokeFixture()
  const race = {id:'r1',name:'Первая дистанция',format:'FixedDistance',started_at_ms:1000,timing_basis:'legacy',shift_assigned_starts:false}
  const edits = []
  return {
    async handle(request,response,path) {
      let value
      if (path.endsWith('/races')) value=[race]
      else if (path.endsWith('/protocol')) value={race_id:'r1',race_name:race.name,format:'FixedDistance',timing_basis:race.timing_basis,counts:{total:1,finished:1},rows:[{member_id:'runner',first_name:'Тест',last_name:'Участник',status:'ok',place:1,number:1,gun_time_ms:120035,chip_time_ms:115000,chip_start_source:'observation'}]}
      else if (path.endsWith('/recount') && request.method==='POST') value={logs_replayed:2,results_created:2}
      else if (path.endsWith('/edits') && request.method==='POST') {
        let text='';for await(const chunk of request)text+=chunk
        const edit=JSON.parse(text)
        if(request.headers.authorization!=='Bearer progress-smoke-key'||edit.entity!=='race'||edit.entity_id!=='r1')throw Error('Invalid timing command')
        if(edit.field==='timing_basis' && (edit.value!=='chip'||!edit.confirm_legacy_assigned))throw Error('Missing explicit classification')
        if(!['timing_basis','shift_assigned_starts'].includes(edit.field))throw Error('Unexpected field')
        edits.push(edit);race[edit.field]=edit.value;value={recount_needed:true}
      } else return base.handle(request,response,path)
      response.writeHead(200,{'Content-Type':'application/json'});response.end(JSON.stringify(value));return true
    },
    verify(html) {
      if(!html.includes('data-dual-smoke="passed"')||edits.length!==2||race.timing_basis!=='chip'||race.shift_assigned_starts!==1)throw Error('Dual-time settings were not saved')
    }
  }
}
export const dualTimeSmokeBootstrap=`<script>
window.go={main:{App:{APIBaseURL:async()=>location.origin,APIToken:async()=> 'progress-smoke-key'}}};
window.addEventListener('DOMContentLoaded',async()=>{
 const wait=async fn=>{const end=Date.now()+10000;while(Date.now()<end){const value=fn();if(value)return value;await new Promise(r=>setTimeout(r,15))}throw Error('Dual time UI timeout: '+fn.toString()+' visible='+document.body.innerText.slice(-2500))};
 const button=text=>[...document.querySelectorAll('button')].find(b=>b.textContent.trim()===text);
 try {
  (await wait(()=>document.querySelector('button.event'))).click();
  (await wait(()=>button('Полный протокол'))).click();
  await wait(()=>document.querySelector('.ptime')?.textContent.includes('Gun 00:02:00.035')&&document.querySelector('.ptime')?.textContent.includes('Chip 00:01:55'));
  (await wait(()=>button('Настройки дистанции'))).click();
  const policy=await wait(()=>document.querySelector('[aria-label="Официальное время"]'));
  policy.value='chip';policy.dispatchEvent(new Event('change',{bubbles:true}));
  (await wait(()=>document.querySelector('[aria-label="Подтвердить назначенные старты"]'))).click();
  button('Применить режим').click();
  await new Promise(r=>setTimeout(r,200));
  document.querySelector('[aria-label="Сдвигать назначенные старты"]').click();
  await new Promise(r=>setTimeout(r,200));
  if(document.querySelector('.error'))throw Error(document.querySelector('.error').textContent);
  document.body.dataset.dualSmoke='passed';
 }catch(e){document.body.dataset.dualSmoke='failed';document.body.dataset.dualSmokeError=String(e)}
});
</script>`
