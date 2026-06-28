<script>
  import {createEventDispatcher, onMount} from 'svelte'
  import {call, RU_TIMEZONES} from './api.js'
  import SiteSyncPanel from './SiteSyncPanel.svelte'
  import BroadcastPanel from './BroadcastPanel.svelte'
  import PhotoSources from './PhotoSources.svelte'
  import EditsLog from './EditsLog.svelte'

  export let eventId
  export let currentRace = null
  export let reloadToken = 0

  const dispatch = createEventDispatcher()

  let msg = ''
  let error = ''

  // Photo-finish (Chrono Cam phones over the LAN).
  let photoSourcesOpen = false
  let photoStat = null
  async function loadPhotoStat() {
    try { photoStat = await call('GET', `/api/events/${eventId}/photos/status`) } catch (_) { photoStat = null }
  }
  onMount(loadPhotoStat)

  // CSV flash import (two-step: pick file → set code+tz → load).
  let deviceCode = ''
  let csvTimezone = 'Europe/Moscow'
  let csvFile = null
  let csvResult = null
  let importing = false

  $: tzOptions = RU_TIMEZONES.some(t => t.value === csvTimezone) || !csvTimezone
    ? RU_TIMEZONES
    : [{value: csvTimezone, label: csvTimezone}, ...RU_TIMEZONES]

  function selectCsvFile(e) {
    csvFile = e.target.files[0] ?? null
    csvResult = null
    error = ''
    e.target.value = ''
  }

  async function runCsvImport() {
    if (!csvFile) return
    if (!deviceCode) { error = 'Укажите код считывателя (например, U659)'; return }
    error = ''
    csvResult = null
    importing = true
    try {
      const q = `device=${encodeURIComponent(deviceCode)}&tz=${encodeURIComponent(csvTimezone)}`
      csvResult = await call('POST', `/api/events/${eventId}/rfid-import?${q}`, await csvFile.text(), 'text/csv')
      csvFile = null
      dispatch('changed', {recount: true})
    } catch (err) {
      error = `Импорт CSV: ${err.message}`
    } finally {
      importing = false
    }
  }

  async function exportJson() {
    msg = ''; error = ''
    try {
      const res = await call('POST', `/api/events/${eventId}/export-json`)
      msg = `Экспорт JSON сохранён: ${res.path}`
    } catch (err) { error = `Экспорт JSON: ${err.message}` }
  }

  async function exportExcel() {
    msg = ''; error = ''
    if (!currentRace) { error = 'Выберите дистанцию на экране «Результаты» для экспорта Excel.'; return }
    try {
      const res = await call('POST', `/api/events/${eventId}/races/${currentRace.id}/export-xlsx`)
      msg = `Протокол Excel сохранён: ${res.path}`
    } catch (err) { error = `Экспорт Excel: ${err.message}` }
  }

  async function backup() {
    msg = ''; error = ''
    try {
      const res = await call('POST', `/api/events/${eventId}/backup`)
      msg = `Резервная копия сохранена: ${res.path}`
    } catch (err) { error = `Бэкап: ${err.message}` }
  }
</script>

<div class="screen">
  <h1>Настройки события</h1>
  <p class="faint sub">Конфигурация, импорт/экспорт и синхронизация. Целевая работа (приём финишей и протоколы) — на экранах Live и Результаты.</p>

  {#if error}<p class="error">{error}</p>{/if}
  {#if msg}<p class="ok-text msg">{msg}</p>{/if}

  <div class="cards">
    <!-- Recount -->
    <div class="card recount">
      <div class="info">
        <span class="ctitle">Пересчёт результатов</span>
        <span class="faint">Результаты пересчитываются автоматически при правках участников, отсечек и времени старта. Ручной пересчёт нужен редко — например после массового реимпорта.</span>
      </div>
      <div class="recount-actions">
        <span class="auto ok-text"><span class="adot"></span>Автопересчёт включён</span>
        <button class="btn" on:click={() => dispatch('recount')}>Пересчитать всё сейчас</button>
      </div>
    </div>

    <!-- Sync -->
    <SiteSyncPanel {eventId} on:pulled={() => dispatch('pulled')}/>

    <!-- Read-only LAN results broadcast -->
    <BroadcastPanel {eventId}/>

    <!-- Photo-finish (Chrono Cam) -->
    <div class="card">
      <div class="ctitle mb">Фотофиниш — камеры на телефонах</div>
      <p class="faint pf-sub">Телефоны Chrono Cam снимают финиш автономно. Десктоп подтягивает кадры по локальной сети — на финише никто не стоит. Кадры видны в ленте «Фотофиниш» на экране Live и в карточке участника при правке времени.</p>
      <div class="pf-row">
        <button class="btn primary" on:click={() => photoSourcesOpen = true}>Источники фото…</button>
        {#if photoStat}
          <span class="faint pf-stat">
            Источников: <b>{(photoStat.sources || []).length}</b> ·
            кадров: <b>{photoStat.photos_count ?? 0}</b> ·
            опрос: <b>{photoStat.running ? 'идёт' : 'остановлен'}</b>
          </span>
        {/if}
      </div>
      <p class="faint pf-hint">Как подключить: в приложении на телефоне откройте «Настройки» → включите «Локальная синхронизация», затем впишите показанный адрес (напр. <code class="mono">http://192.168.0.50:8080</code>) здесь в «Источниках».</p>
    </div>

    <!-- CSV import -->
    <div class="card">
      <div class="ctitle mb">Импорт логов с флешки (CSV)</div>
      <div class="csv-row">
        <div class="field">
          <span>Код считывателя</span>
          <input class="input mono" style="width:140px" placeholder="U659" bind:value={deviceCode}/>
        </div>
        <div class="field">
          <span>Часовой пояс</span>
          <select class="input" style="width:200px" bind:value={csvTimezone}>
            {#each tzOptions as t}<option value={t.value}>{t.label}</option>{/each}
          </select>
        </div>
        <label class="btn file">
          {csvFile ? 'Другой файл…' : 'Выбрать файл…'}
          <input type="file" accept=".csv,.txt" on:change={selectCsvFile} hidden/>
        </label>
        <button class="btn primary" disabled={!csvFile || !deviceCode || importing} on:click={runCsvImport}>
          {importing ? 'Загрузка…' : 'Загрузить'}
        </button>
        {#if csvFile}<span class="faint fname">{csvFile.name}</span>{/if}
      </div>

      {#if csvResult}
        <div class="csv-result">
          <p>
            Строк: <b>{csvResult.parsed}</b> ·
            добавлено: <b class="ok-text">{csvResult.inserted}</b> ·
            дублей: <b>{csvResult.duplicates}</b> ·
            пропущено: <b class:bad-text={csvResult.errors.length}>{csvResult.errors.length}</b>
          </p>
          {#if csvResult.errors.length}
            <details>
              <summary>Показать ошибки ({csvResult.errors.length}{csvResult.errors.length >= 20 ? '+, первые 20' : ''})</summary>
              <ul class="errs">
                {#each csvResult.errors as e}
                  <li><b>строка {e.line}:</b> {e.reason}<br/><code class="mono">{e.raw}</code></li>
                {/each}
              </ul>
            </details>
          {/if}
        </div>
      {/if}
    </div>

    <!-- Data -->
    <div class="card">
      <div class="ctitle mb">Данные события</div>
      <div class="data-btns">
        <button class="btn" on:click={exportJson}>Экспорт JSON</button>
        <button class="btn" on:click={exportExcel} title="Протокол выбранной на «Результатах» дистанции">Экспорт Excel</button>
        <button class="btn" on:click={backup}>Резервная копия</button>
      </div>
    </div>

    <!-- Local edits log -->
    <div class="card edits">
      <EditsLog {eventId} {reloadToken}/>
    </div>
  </div>
</div>

{#if photoSourcesOpen}
  <PhotoSources {eventId} on:changed={loadPhotoStat} on:close={() => { photoSourcesOpen = false; loadPhotoStat() }}/>
{/if}

<style>
  .screen { padding: 24px; max-width: 880px; }
  h1 { font-size: 22px; font-weight: 700; margin: 0 0 6px; }
  .sub { font-size: 13.5px; margin: 0 0 22px; }
  .msg { font-size: 13px; margin: 0 0 14px; }

  .cards { display: flex; flex-direction: column; gap: 14px; }
  .ctitle { font-size: 16px; font-weight: 700; }
  .ctitle.mb { display: block; margin-bottom: 14px; }

  .card.recount { display: flex; align-items: center; justify-content: space-between; gap: 16px; flex-wrap: wrap; }
  .recount .info { display: flex; flex-direction: column; gap: 4px; max-width: 520px; }
  .recount .info .faint { font-size: 13.5px; }
  .recount-actions { display: flex; align-items: center; gap: 12px; }
  .auto { display: flex; align-items: center; gap: 7px; font-size: 13px; font-weight: 600; }
  .adot { width: 8px; height: 8px; border-radius: 50%; background: var(--ok); }

  .csv-row { display: flex; align-items: flex-end; gap: 14px; flex-wrap: wrap; }
  .file { justify-content: center; }
  .fname { font-size: 13px; align-self: center; }
  .csv-result { margin-top: 14px; font-size: 13.5px; }
  .csv-result summary { cursor: pointer; color: var(--dim); }
  .errs { margin: 8px 0 0; padding-left: 18px; max-height: 14rem; overflow: auto; }
  .errs li { margin-bottom: 5px; font-size: 12.5px; }
  .errs code { color: var(--amber); font-size: 12px; }

  .data-btns { display: flex; gap: 10px; flex-wrap: wrap; }
  .card.edits { padding-top: 18px; padding-bottom: 18px; }

  .pf-sub { font-size: 13.5px; margin: 0 0 14px; line-height: 1.5; }
  .pf-row { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; }
  .pf-stat { font-size: 13px; }
  .pf-hint { font-size: 12.5px; margin: 12px 0 0; line-height: 1.5; }
  .pf-hint code { color: var(--amber); font-size: 12px; }
</style>
