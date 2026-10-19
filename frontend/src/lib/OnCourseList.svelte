<script>
  import {createEventDispatcher} from 'svelte'
  import {fmtTime} from './api.js'
  export let progress = null
  const dispatch = createEventDispatcher()
  let query = ''
  $: rows = progress?.on_course || []
  $: shown = rows.filter(row => {
    const q = query.trim().toLowerCase()
    return !q || `${row.last_name ?? ''} ${row.first_name ?? ''}`.toLowerCase().includes(q) || String(row.number ?? '').includes(q)
  })
</script>

<section class="on-course" aria-label="Участники на дистанции">
  <p class="faint note">Здесь участники с наступившим стартом, без финиша и без DNS, DNF или DSQ. При общем старте ожидаем всех зарегистрированных: если человек не стартовал, отметьте DNS в его карточке.</p>
  {#if progress}
    <label class="search">Поиск участника
      <input class="input" type="search" bind:value={query} placeholder="Фамилия или номер"/>
    </label>
    <div class="roster" role="list" aria-label="Ожидаемые финишёры">
      <div class="head"><span>Номер</span><span>Участник</span><span>Группа</span><span>Старт</span></div>
      {#each shown as row (row.member_id)}
        <div role="listitem">
          <button class="person" on:click={() => dispatch('openMember', row.member_id)}>
            <span class="mono bib">{row.number ?? '—'}</span>
            <span>{row.last_name} {row.first_name}</span>
            <span class="faint">{row.category_name ?? '—'}</span>
            <span class="mono faint">{fmtTime(row.start_time_ms)}</span>
          </button>
        </div>
      {:else}
        <p class="empty">{query.trim() ? 'Никого не нашлось' : 'Ожидаемых финишёров на дистанции нет.'}</p>
      {/each}
    </div>
    {#if progress.counts.not_started}<p class="faint note">У {progress.counts.not_started} участников старт ещё не наступил или не задан. Они не включены в список выше.</p>{/if}
    {#if progress.counts.needs_review}<p class="note">Требуют проверки: {progress.counts.needs_review}. По данным этих участников нельзя уверенно определить завершение дистанции.</p>{/if}
  {:else}
    <p class="faint">Список ожидаемых финишёров пока не загружен.</p>
  {/if}
</section>

<style>
  .note {font-size:13px;line-height:1.5;max-width:900px;margin:0 0 14px}
  .search {display:flex;flex-direction:column;gap:6px;max-width:420px;font-size:13px;margin-bottom:14px}
  .roster {border:1px solid var(--border);border-radius:13px;overflow:auto;margin-bottom:14px}
  .head,.person {display:grid;grid-template-columns:80px minmax(180px,1fr) 180px 140px;align-items:center;gap:12px;padding:13px 18px;min-width:640px}
  .head {background:var(--surface2);font-size:12px;color:var(--faint);font-weight:700}
  .person {width:100%;text-align:left;background:none;border:0;border-top:1px solid var(--border);font:inherit;color:var(--text);cursor:pointer;font-size:14px}
  .person:hover {background:var(--surface2)}
  .bib {font-weight:600;color:var(--accent)}
  .empty {padding:18px;color:var(--faint);margin:0}
</style>
