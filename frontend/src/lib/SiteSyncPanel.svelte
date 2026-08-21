<script>
  import {createEventDispatcher} from 'svelte'
  import {call, fmtDateTime} from './api.js'

  export let eventId

  const dispatch = createEventDispatcher()

  let configOpen = false
  let baseUrl = ''
  let token = ''
  let tokenSet = false
  let lastSyncedAt = null
  let storage = null
  let overwrite = true
  let siteWins = false
  let busy = ''
  let error = ''
  let result = null
  let pullResult = null
  let saved = false
  let loaded = false

  async function loadConfig() {
    error = ''
    try {
      const cfg = await call('GET', `/api/events/${eventId}/sync-config`)
      baseUrl = cfg.base_url || ''
      tokenSet = cfg.token_set
      lastSyncedAt = cfg.last_synced_at
      storage = cfg.storage || null
      loaded = true
    } catch (e) { error = e.message }
  }

  function fmtBytes(bytes) {
    if (!Number.isFinite(bytes) || bytes < 0) return '—'
    if (bytes < 1024) return `${bytes} Б`
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} КБ`
    return `${(bytes / (1024 * 1024)).toFixed(1)} МБ`
  }
  $: eventId && !loaded && loadConfig()

  async function saveConfig() {
    error = ''; saved = false
    try {
      await call('PUT', `/api/events/${eventId}/sync-config`,
        JSON.stringify({base_url: baseUrl.trim(), token: token.trim()}))
      token = ''; saved = true
      await loadConfig()
    } catch (e) { error = e.message }
  }

  async function push() {
    error = ''; result = null; pullResult = null
    busy = 'Отправка на сайт…'
    try {
      result = await call('POST', `/api/events/${eventId}/sync`, JSON.stringify({overwrite}))
      await loadConfig()
    } catch (e) { error = `Отправка: ${e.message}` } finally { busy = '' }
  }

  async function pull() {
    error = ''; result = null; pullResult = null
    if (!confirm(siteWins
      ? 'Получить с сайта и взять значения сайта поверх локальных правок?'
      : 'Получить с сайта (локальные правки сохранятся поверх)?')) return
    busy = 'Получение с сайта…'
    try {
      pullResult = await call('POST', `/api/events/${eventId}/sync-pull`, JSON.stringify({overwrite: siteWins}))
      dispatch('pulled')
    } catch (e) { error = `Получение: ${e.message}` } finally { busy = '' }
  }
</script>

<div class="card">
  <div class="head">
    <div class="info">
      <span class="ctitle">Синхронизация с сайтом run5</span>
      <span class="faint">
        {#if lastSyncedAt}Последняя синхронизация: {fmtDateTime(lastSyncedAt)}. {/if}Локальные правки побеждают при реимпорте.
      </span>
    </div>
    <button class="btn link cfg-toggle" on:click={() => configOpen = !configOpen}>
      {configOpen ? 'свернуть настройки' : 'настройки подключения'}
    </button>
  </div>

  {#if error}<p class="error">{error}</p>{/if}

  {#if configOpen}
    <div class="cfg">
      <div class="field"><span>Адрес сайта</span>
        <input class="input" placeholder="https://run5.example" bind:value={baseUrl}/></div>
      <div class="field"><span>Токен синхронизации</span>
        <input class="input" type="password" placeholder={tokenSet ? '•••••• (задан)' : 'вставьте токен'} bind:value={token}/></div>
      <button class="btn" on:click={saveConfig}>Сохранить</button>
      {#if saved}<span class="ok-text saved">сохранено</span>{/if}
      {#if storage}
        <span class="storage faint" title="Размеры снимаются без checkpoint WAL">
          SQLite: {fmtBytes(storage.database_bytes)} · WAL: {fmtBytes(storage.wal_bytes)} ·
          всего: {fmtBytes(storage.total_bytes)}
        </span>
      {/if}
    </div>
  {/if}

  <div class="actions">
    <label class="check" title="Применить на сайте только явно сделанные офлайн-правки. Чужие точки и отметки не удаляются и не включаются обратно.">
      <input type="checkbox" bind:checked={overwrite}/> применять локальные правки на сайте
    </label>
    <button class="btn primary" disabled={!!busy || !baseUrl || !tokenSet} on:click={push}>Отправить на сайт →</button>
  </div>

  <div class="actions">
    <label class="check" title="Взять значения сайта поверх локальных правок.">
      <input type="checkbox" bind:checked={siteWins}/> значения сайта важнее локальных правок
    </label>
    <button class="btn" disabled={!!busy || !baseUrl || !tokenSet} on:click={pull}>← Получить с сайта</button>
    {#if busy}<span class="amber-text">{busy}</span>{/if}
  </div>

  {#if result}
    <div class="result faint">
      Отправлено — логи: <b>{result.sent?.rfid_logs ?? '—'}</b> ·
      правки логов: <b>{result.sent?.rfid_log_edits ?? 0}</b> ·
      ручные: <b>{result.sent?.manual_results ?? 0}</b> ·
      правки участников: <b>{result.sent?.member_edits ?? 0}</b> ·
      новые: <b>{result.sent?.new_members ?? 0}</b>
    </div>
  {/if}
  {#if pullResult}
    <div class="result faint">
      Получено{pullResult.site_wins ? ' (значения сайта)' : ' (локальные правки сохранены)'} —
      участники: <b>{pullResult.imported?.members ?? 0}</b> ·
      логи: <b>{pullResult.imported?.rfid_logs ?? 0}</b> ·
      правок переиграно: <b>{pullResult.imported?.local_edits_reapplied ?? 0}</b>.
    </div>
  {/if}
</div>

<style>
  .head { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; flex-wrap: wrap; }
  .info { display: flex; flex-direction: column; gap: 4px; }
  .ctitle { font-size: 16px; font-weight: 700; }
  .info .faint { font-size: 13.5px; }
  .cfg-toggle { font-size: 13px; white-space: nowrap; }
  .cfg { display: flex; gap: 14px; align-items: flex-end; flex-wrap: wrap; margin-top: 14px; }
  .cfg .input { min-width: 16rem; }
  .saved { font-size: 13px; align-self: center; }
  .storage { width: 100%; font-size: 12.5px; }
  .actions { display: flex; gap: 14px; align-items: center; flex-wrap: wrap; margin-top: 12px; }
  .check { display: flex; align-items: center; gap: 8px; font-size: 13.5px; color: var(--dim); cursor: pointer; }
  .check input { accent-color: var(--accent); }
  .result { margin-top: 12px; font-size: 13px; }
</style>
