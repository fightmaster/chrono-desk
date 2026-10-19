// Synthetic operational roster. No physical input, event database or site access.
export function progressSmokeFixture() {
  let finished = false, legacy = false, cardReads = 0, writes = 0
  const counts = () => ({total:88, finished:finished ? 77 : 76,on_course:finished ? 1 : 2,dns:5,dnf:4,dsq:1,not_started:0,needs_review:0,reserve:0})
  const people = [
    {member_id:'runner-1',number:12,first_name:'Первый',last_name:'Ожидаемый',category_name:'Мужчины',start_time_ms:1000},
    {member_id:'runner-2',number:13,first_name:'Второй',last_name:'Ожидаемый',category_name:'Мужчины',start_time_ms:1000}
  ]
  const races = [{id:'r1',name:'Первая дистанция',started_at_ms:1000},{id:'r2',name:'Вторая дистанция',started_at_ms:1000}]
  return {
    async handle(request,response,path) {
      if (path === '/__test/finish') {finished=true;response.end('{}');return true}
      if (path === '/__test/legacy') {legacy=true;response.end('{}');return true}
      if (!path.startsWith('/api/')) return false
      if (request.method !== 'GET') {writes++;response.writeHead(405);response.end('{}');return true}
      if (request.headers.authorization !== 'Bearer progress-smoke-key') {response.writeHead(401);response.end('{}');return true}
      let value=[]
      if (path === '/api/version') value={}
      else if (path === '/api/events') value=[{id:'100',name:'Synthetic roster',date:'2026-10-20',race_count:2,member_count:88}]
      else if (path.endsWith('/races')) value=races
      else if (path.endsWith('/categories')) value=[]
      else if (path.endsWith('/members')) value=[]
      else if (path.endsWith('/live/status')) value={running:false,any_running:false,readers:[]}
      else if (path.endsWith('/passes')) {
        cardReads++
        const p=people.find(p=>path.includes('/'+p.member_id+'/'))
        value={member_id:p.member_id,number:p.number,first_name:p.first_name,last_name:p.last_name,status:0,passes:[],manual_results:[],member:{id:p.member_id,race_id:'r1',number:p.number,first_name:p.first_name,last_name:p.last_name}}
      } else if (path.endsWith('/protocol')) {
        const race=path.includes('/r2/') ? races[1] : races[0]
        if (race.id==='r2') await new Promise(resolve=>setTimeout(resolve,250))
        value={race_id:race.id,race_name:race.name,format:'FixedDistance',rows:[],counts:{total:88,started:83,finished:finished?77:76,dns:5,dnf:4,dsq:1}}
        if (!legacy) value.progress={counts:race.id==='r1'?counts():{total:0,finished:0,on_course:0,dns:0,dnf:0,dsq:0,not_started:0,needs_review:0,reserve:0},on_course:race.id==='r1'?(finished?[people[1]]:people):[]}
      }
      response.writeHead(200,{'Content-Type':'application/json'});response.end(JSON.stringify(value));return true
    },
    verify(html) {
      if (!html.includes('data-progress-smoke="passed"') || cardReads!==1 || writes || !finished || !legacy) throw Error('Roster actions incomplete or unexpected timing write')
    }
  }
}
export const progressSmokeBootstrap = `<script>
window.go={main:{App:{APIBaseURL:async()=>location.origin,APIToken:async()=> 'progress-smoke-key'}}};
window.addEventListener('DOMContentLoaded',async()=>{
 const wait=async fn=>{const end=Date.now()+10000;while(Date.now()<end){const r=fn();if(r)return r;await new Promise(r=>setTimeout(r,15))}throw Error('Roster smoke UI timeout')};
 const button=text=>[...document.querySelectorAll('button')].find(b=>b.textContent.trim()===text);
 const roster=()=>[...document.querySelectorAll('.on-course .person')];
 try {
  (await wait(()=>document.querySelector('button.event'))).click();
  (await wait(()=>button('На дистанции · 2'))).click();
  await wait(()=>roster().length===2);
  const summary=document.querySelector('.dmeta').textContent;
  if(!summary.includes('Финишировали: 76')||!summary.includes('DNS: 5')||!summary.includes('Сошли: 4')||summary.includes('из 83'))throw Error('Ambiguous finish denominator');
  const input=document.querySelector('.on-course input');input.value='13';input.dispatchEvent(new Event('input',{bubbles:true}));
  await wait(()=>roster().length===1&&roster()[0].textContent.includes('Второй'));
  input.value='';input.dispatchEvent(new Event('input',{bubbles:true}));await wait(()=>roster().length===2);
  roster()[0].click();await wait(()=>document.querySelector('.drawer .reg'));document.querySelector('.drawer .x').click();await wait(()=>!document.querySelector('.drawer'));
  await fetch('/__test/finish');await wait(()=>button('На дистанции · 1'));
  await wait(()=>roster().length===1&&roster()[0].textContent.includes('Второй'));
  button('Вторая дистанция').click();await wait(()=>button('На дистанции · 0'));await wait(()=>document.querySelector('.on-course .empty')?.textContent.includes('нет'));
  button('Первая дистанция').click();await wait(()=>button('На дистанции · 1'));
  button('Вторая дистанция').click();await new Promise(r=>setTimeout(r,20));button('Первая дистанция').click();
  await new Promise(r=>setTimeout(r,400));if(!button('На дистанции · 1')||!document.querySelector('.dname').textContent.includes('Первая'))throw Error('Old distance replaced current roster');
  await fetch('/__test/legacy');button('Вторая дистанция').click();await wait(()=>document.querySelector('.on-course')?.textContent.includes('пока не загружен'));
  if(document.querySelector('.dmeta').textContent.includes('На дистанции: 0'))throw Error('Old API invented zero');
  document.body.dataset.progressSmoke='passed';
 } catch(e) {document.body.dataset.progressSmoke='failed';document.body.dataset.progressSmokeError=String(e)}
});
</script>`
