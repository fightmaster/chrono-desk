<script>
  import {createEventDispatcher} from 'svelte'
  import {call, fmtTime} from './api.js'

  export let eventId

  const dispatch = createEventDispatcher()

  let open = false
  let baseUrl = ''
  let token = ''
  let tokenSet = false
  let lastSyncedAt = null
  let overwrite = true   // push: overwrite site fields
  let siteWins = false   // pull: take site values over local edits
  let busy = ''
  let error = ''
  let result = null
  let pullResult = null
  let saved = false

  async function loadConfig() {
    error = ''
    try {
      const cfg = await call('GET', `/api/events/${eventId}/sync-config`)
      baseUrl = cfg.base_url || ''
      tokenSet = cfg.token_set
      lastSyncedAt = cfg.last_synced_at
    } catch (e) {
      error = e.message
    }
  }

  function toggle() {
    open = !open
    if (open) loadConfig()
  }

  async function saveConfig() {
    error = ''
    saved = false
    try {
      await call('PUT', `/api/events/${eventId}/sync-config`,
        JSON.stringify({base_url: baseUrl.trim(), token: token.trim()}))
      token = ''
      saved = true
      await loadConfig()
    } catch (e) {
      error = e.message
    }
  }

  async function push() {
    error = ''
    result = null
    pullResult = null
    busy = 'Отправка на сайт…'
    try {
      result = await call('POST', `/api/events/${eventId}/sync`, JSON.stringify({overwrite}))
      await loadConfig()
    } catch (e) {
      error = `Отправка: ${e.message}`
    } finally {
      busy = ''
    }
  }

  async function pull() {
    error = ''
    result = null
    pullResult = null
    if (!confirm(siteWins
      ? 'Получить с сайта и взять значения сайта поверх локальных правок?'
      : 'Получить с сайта (локальные правки сохранятся поверх)?')) return
    busy = 'Получение с сайта…'
    try {
      pullResult = await call('POST', `/api/events/${eventId}/sync-pull`, JSON.stringify({overwrite: siteWins}))
      dispatch('pulled')
    } catch (e) {
      error = `Получение: ${e.message}`
    } finally {
      busy = ''
    }
  }
</script>

{#if open}
  <div class="sync">
    <div class="head">
      <h4>Синхронизация с сайтом run5</h4>
      <button class="link" on:click={toggle}>свернуть</button>
    </div>
    {#if error}<p class="error">{error}</p>{/if}

    <div class="cfg">
      <label>Адрес сайта
        <input placeholder="https://run5.example" bind:value={baseUrl}/>
      </label>
      <label>Токен синхронизации
        <input type="password" placeholder={tokenSet ? '•••••• (задан)' : 'вставьте токен'} bind:value={token}/>
      </label>
      <button class="btn" on:click={saveConfig}>Сохранить</button>
      {#if saved}<span class="ok">сохранено</span>{/if}
    </div>

    <div class="actions">
      <label class="check" title="Перетереть на сайте поля, правленные офлайн (статус/старт/метка/номер/категория). Логи и ручные результаты сливаются всегда.">
        <input type="checkbox" bind:checked={overwrite}/>
        перезаписывать значения на сайте
      </label>
      <button class="btn primary" disabled={!!busy || !baseUrl || !tokenSet} on:click={push}>
        Отправить на сайт →
      </button>
    </div>

    <div class="actions">
      <label class="check" title="Взять значения сайта поверх локальных правок. Иначе локальные правки сохраняются (переигрываются поверх).">
        <input type="checkbox" bind:checked={siteWins}/>
        значения сайта важнее локальных правок
      </label>
      <button class="btn" disabled={!!busy || !baseUrl || !tokenSet} on:click={pull}>
        ← Получить с сайта
      </button>
      {#if busy}<span class="busy">{busy}</span>{/if}
      {#if lastSyncedAt}<span class="hint">последняя отправка: {fmtTime(lastSyncedAt)}</span>{/if}
    </div>

    {#if result}
      <div class="result">
        Отправлено — логи: <b>{result.sent?.rfid_logs ?? '—'}</b> ·
        ручные: <b>{result.sent?.manual_results ?? 0}</b> ·
        правки участников: <b>{result.sent?.member_edits ?? 0}</b> ·
        новые: <b>{result.sent?.new_members ?? 0}</b>
        {#if result.response}
          <details>
            <summary>Ответ сайта</summary>
            <pre>{JSON.stringify(result.response, null, 2)}</pre>
          </details>
        {/if}
      </div>
    {/if}

    {#if pullResult}
      <div class="result">
        Получено с сайта{pullResult.site_wins ? ' (значения сайта)' : ' (локальные правки сохранены)'} —
        участники: <b>{pullResult.imported?.members ?? 0}</b> ·
        логи: <b>{pullResult.imported?.rfid_logs ?? 0}</b> ·
        правок переиграно: <b>{pullResult.imported?.local_edits_reapplied ?? 0}</b>.
        Запустите пересчёт, чтобы обновить результаты.
      </div>
    {/if}
  </div>
{:else}
  <button class="btn" on:click={toggle}>Синхронизация с сайтом</button>
{/if}

<style>
  .sync { border: 1px solid #4a5568; border-radius: 6px; padding: 0.8rem 1rem; margin-bottom: 1rem; background: #232b38; }
  .head { display: flex; justify-content: space-between; align-items: center; }
  .head h4 { margin: 0; }
  .cfg, .actions { display: flex; gap: 0.8rem; align-items: end; flex-wrap: wrap; margin-top: 0.7rem; }
  label { display: flex; flex-direction: column; gap: 0.2rem; color: #9aa5b1; font-size: 0.85rem; }
  label.check { flex-direction: row; align-items: center; gap: 0.35rem; }
  input { background: #1a202c; color: #e2e8f0; border: 1px solid #4a5568; border-radius: 3px; padding: 0.3rem 0.4rem; }
  .cfg input { min-width: 16rem; }
  .btn { padding: 0.4rem 0.9rem; border-radius: 4px; border: 1px solid #4a5568; background: #2d3748; color: inherit; cursor: pointer; }
  .btn.primary { background: #2b6cb0; border-color: #2b6cb0; }
  .btn:disabled { opacity: 0.5; cursor: not-allowed; }
  .link { background: none; border: none; color: #63b3ed; cursor: pointer; }
  .ok { color: #81c784; font-size: 0.85rem; }
  .busy { color: #ffb74d; }
  .hint { color: #9aa5b1; font-size: 0.85rem; }
  .error { color: #e57373; }
  .result { margin-top: 0.7rem; font-size: 0.95rem; }
  .result pre { background: #1a202c; padding: 0.5rem; border-radius: 4px; overflow: auto; max-height: 12rem; font-size: 0.8rem; }
</style>
