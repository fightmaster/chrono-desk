<script>
  import {createEventDispatcher, onDestroy, onMount} from 'svelte'
  import {call} from './api.js'

  export let eventId

  const dispatch = createEventDispatcher()
  const pageSize = 50
  let members = []
  let page = {items: [], total: 0, limit: pageSize, offset: 0}
  let offset = 0
  let view = 'unresolved'
  let loading = false
  let error = ''
  let selected = null
  let choice = 'current'
  let reason = ''
  let actor = 'Оператор Chrono Desk'
  let timer

  $: tracked = members.filter(m => m.packet_tracked)
  $: counts = {
    total: tracked.length,
    issued: tracked.filter(m => m.issued).length,
    reserve: tracked.filter(m => m.reserve).length,
    dns: tracked.filter(m => m.status === 1).length,
    dnf: tracked.filter(m => m.status === 2).length,
    dsq: tracked.filter(m => m.status === 3).length,
  }

  async function load(quiet = false) {
    if (loading) return
    loading = true
    if (!quiet) error = ''
    try {
      const [roster, conflicts] = await Promise.all([
        call('GET', `/api/events/${eventId}/members`),
        call('GET', `/api/events/${eventId}/packet-issuance/conflicts?view=${view}&limit=${pageSize}&offset=${offset}`),
      ])
      members = roster
      page = conflicts
      if (offset >= page.total && offset > 0) { offset = Math.max(0, offset - pageSize); await load() }
    } catch (e) {
      if (!quiet) error = e.message
    } finally { loading = false }
  }

  $: if (eventId) { view; offset; load() }
  onMount(() => { timer = setInterval(() => load(true), 5000) })
  onDestroy(() => clearInterval(timer))

  function begin(item, nextChoice) {
    selected = item
    choice = nextChoice
    reason = ''
  }

  async function resolve() {
    if (!selected || !reason.trim() || !actor.trim()) return
    loading = true; error = ''
    try {
      await call('POST', `/api/events/${eventId}/packet-issuance/conflicts/${selected.operation_id}/resolve`, JSON.stringify({
        choice, reason: reason.trim(), actor: actor.trim(), evidence: selected.evidence,
      }))
      selected = null
      await load()
      dispatch('changed')
    } catch (e) { error = e.message } finally { loading = false }
  }

  function person(row) {
    if (!row?.person) return 'резерв'
    return `${row.person.lastName || ''} ${row.person.firstName || ''}`.trim() || 'без имени'
  }
</script>

<section class="packet-review">
  <div class="review-head">
    <div>
      <b>Состояние выдачи</b>
      <p class="faint">Данные локального журнала Desk; обновляются каждые 5 секунд.</p>
    </div>
    <button class="btn" disabled={loading} on:click={() => load()}>Обновить</button>
  </div>
  <div class="stats">
    <span>Участников: <b>{counts.total}</b></span><span class="issued">Выдано: <b>{counts.issued}</b></span>
    <span>Резерв: <b>{counts.reserve}</b></span><span>DNS: <b>{counts.dns}</b></span>
    <span>DNF: <b>{counts.dnf}</b></span><span>DSQ: <b>{counts.dsq}</b></span>
  </div>

  <div class="review-head conflicts-head">
    <b>Конфликты</b>
    <div class="tabs">
      <button class:active={view === 'unresolved'} on:click={() => { view = 'unresolved'; offset = 0 }}>Требуют разбора</button>
      <button class:active={view === 'all'} on:click={() => { view = 'all'; offset = 0 }}>Все</button>
    </div>
  </div>
  <p class="faint">По {pageSize} записей на странице. Решение записывается как обычная неизменяемая операция и будет отправлено на сайт и планшеты.</p>
  {#if error}<p class="error">{error}</p>{/if}
  {#if !page.items.length}<p class="ok-text">{view === 'unresolved' ? 'Неразобранных конфликтов нет.' : 'Конфликтов пока нет.'}</p>{/if}
  {#each page.items as item (item.operation_id)}
    <article class="conflict" class:resolved={item.resolved}>
      <div class="conflict-title"><b>{item.claimed_actor || 'Неизвестный волонтёр'}</b><span class="mono">{item.code || 'conflict'}</span></div>
      {#each item.changes as change}
        <div class="change"><span>№{change.before.bib || '—'} · {person(change.before)}</span><b>→</b><span>№{change.after.bib || '—'} · {person(change.after)}</span></div>
      {/each}
      {#if !item.resolved}
        <div class="conflict-actions">
          <button class="btn" on:click={() => begin(item, 'current')}>Сохранить текущее</button>
          <button class="btn primary" disabled={!item.proposal_applicable} on:click={() => begin(item, 'proposal')}>Применить изменение</button>
        </div>
        {#if !item.proposal_applicable}<p class="error">Предложение сейчас небезопасно: сначала исправьте или освободите занятый номер.</p>{/if}
      {:else}<p class="ok-text">Конфликт разобран.</p>{/if}
    </article>
  {/each}
  {#if page.total > pageSize}
    <div class="pager"><button class="btn" disabled={offset === 0} on:click={() => offset = Math.max(0, offset - pageSize)}>← Назад</button>
      <span>{offset + 1}–{Math.min(offset + pageSize, page.total)} из {page.total}</span>
      <button class="btn" disabled={offset + pageSize >= page.total} on:click={() => offset += pageSize}>Далее →</button></div>
  {/if}
</section>

{#if selected}
  <div class="decision">
    <b>{choice === 'proposal' ? 'Применить предложенное изменение' : 'Сохранить текущее состояние'}</b>
    <ol>
      <li>Не закрывайте конфликт сохранением текущего состояния, если данные планшета верны.</li>
      <li>Проверьте физические пакеты.</li>
      <li>Если целевой номер занят ошибочно, сначала перенесите его владельца или освободите номер в резерв.</li>
      <li>После исправления снова откройте конфликт.</li>
      <li>Примените предложенное изменение и укажите причину.</li>
    </ol>
    <label>Кто принял решение<input class="input" maxlength="160" bind:value={actor}/></label>
    <label>Причина<textarea class="input" maxlength="500" bind:value={reason}></textarea></label>
    <div class="conflict-actions"><button class="btn" on:click={() => selected = null}>Отмена</button><button class="btn primary" disabled={loading || !reason.trim() || !actor.trim()} on:click={resolve}>Записать решение</button></div>
  </div>
{/if}

<style>
  .packet-review { margin-top: 14px; padding-top: 14px; border-top: 1px solid var(--line); }
  .review-head,.conflict-title,.conflict-actions,.pager { display:flex; align-items:center; justify-content:space-between; gap:12px; }
  .review-head p { margin:3px 0 0; }
  .stats { display:flex; flex-wrap:wrap; gap:8px; margin:10px 0 18px; }
  .stats span { padding:6px 9px; border:1px solid var(--border); border-radius:8px; background:var(--surface2); }
  .stats .issued { border-color:var(--live); }
  .tabs { display:flex; gap:6px; }
  .tabs button { border:1px solid var(--border); background:var(--surface2); color:var(--text); border-radius:8px; padding:6px 9px; cursor:pointer; }
  .tabs button.active { border-color:var(--accent); color:var(--accent); }
  .conflict { border:1px solid #b7791f; border-radius:9px; padding:12px; margin:9px 0; background:rgba(183,121,31,.07); }
  .conflict.resolved { border-color:var(--border); opacity:.75; }
  .conflict-title .mono { font-size:11px; color:var(--faint); }
  .change { display:grid; grid-template-columns:1fr auto 1fr; gap:10px; margin:8px 0; }
  .conflict-actions { justify-content:flex-end; margin-top:10px; }
  .pager { justify-content:center; margin-top:10px; }
  .decision { position:fixed; z-index:90; inset:auto 24px 24px auto; width:min(560px,calc(100vw - 48px)); padding:18px; border:1px solid #b7791f; border-radius:12px; background:var(--surface); box-shadow:var(--shadow); }
  .decision ol { padding-left:22px; color:var(--dim); }
  .decision label { display:block; margin-top:9px; font-weight:600; }
  .decision input,.decision textarea { display:block; width:100%; margin-top:4px; }
  .decision textarea { min-height:70px; }
</style>
