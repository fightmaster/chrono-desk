<script>
  import {createEventDispatcher, onMount, onDestroy} from 'svelte'
  import {call, fmtTime} from './api.js'
  import PhotoSources from './PhotoSources.svelte'

  export let eventId
  export let timeMs = null          // entered / finish time to match against
  export let bibHint = null         // member number — biases the match (bib-first)
  export let manualUnbound = false  // manual finish with no number bound yet

  const dispatch = createEventDispatcher()
  const TOL_STEPS = [100, 200, 400, 800, 1500, 3000, 5000]

  let tol = 400
  let photos = []
  let sourcesCount = 0
  let photosInBase = 0
  let loading = false
  let error = ''
  let selectedFt = null   // clicked neighbour; null → show best match
  let sourcesOpen = false

  let timer = null
  onDestroy(() => clearTimeout(timer))

  onMount(loadStatus)

  async function loadStatus() {
    try {
      const st = await call('GET', `/api/events/${eventId}/photos/status`)
      sourcesCount = (st.sources || []).length
      photosInBase = st.photos_count || 0
    } catch (_) { /* status is best-effort; matching still works */ }
  }

  // Debounced match — refires as the judge edits the time or the window.
  $: schedule(timeMs, tol, bibHint)
  function schedule() {
    clearTimeout(timer)
    if (timeMs == null) { photos = []; return }
    timer = setTimeout(fetchMatch, 280)
  }

  async function fetchMatch() {
    if (timeMs == null) return
    loading = true
    error = ''
    selectedFt = null
    try {
      const q = `time_ms=${timeMs}&tolerance_ms=${tol}` + (bibHint != null && bibHint !== '' ? `&bib=${encodeURIComponent(bibHint)}` : '')
      photos = await call('GET', `/api/events/${eventId}/photos?${q}`)
    } catch (e) {
      error = e.message
      photos = []
    } finally {
      loading = false
    }
  }

  function onSourcesChanged() { loadStatus(); fetchMatch() }

  // Flatten every matched track's burst into a single time-sorted frame list.
  $: allFrames = (() => {
    const out = []
    for (const p of photos) {
      const frames = (p.frames && p.frames.length)
        ? p.frames
        : (p.best_photo_url ? [{timestamp_epoch_ms: p.time_ms, url: p.best_photo_url}] : [])
      for (const f of frames) {
        out.push({ft: f.timestamp_epoch_ms, url: f.url, bib: p.bib, source: p.bib_source, camera: p.camera_label})
      }
    }
    out.sort((a, b) => a.ft - b.ft)
    // de-dup identical frames
    return out.filter((f, i) => i === 0 || f.ft !== out[i - 1].ft || f.url !== out[i - 1].url)
  })()

  $: best = photos.length ? photos[0] : null
  $: bestFrame = best ? {ft: best.time_ms, url: best.best_photo_url, bib: best.bib, source: best.bib_source, camera: best.camera_label} : null
  $: display = selectedFt != null ? (allFrames.find(f => f.ft === selectedFt) || bestFrame) : bestFrame

  // The ~12 frames closest to the entered time, re-sorted chronologically.
  $: cells = (() => {
    if (timeMs == null || !allFrames.length) return []
    const near = [...allFrames].sort((a, b) => Math.abs(a.ft - timeMs) - Math.abs(b.ft - timeMs)).slice(0, 12)
    near.sort((a, b) => a.ft - b.ft)
    return near
  })()
  // The cell nearest the entered time — marked with the centerline.
  $: centerFt = (timeMs != null && cells.length)
    ? cells.reduce((m, x) => Math.abs(x.ft - timeMs) < Math.abs(m.ft - timeMs) ? x : m).ft
    : null

  $: offset = display && timeMs != null ? display.ft - timeMs : null
  $: bestOff = bestFrame && timeMs != null ? bestFrame.ft - timeMs : null
  $: cameraLabel = best?.camera_label || display?.camera || '—'

  // bib_source is the confidence signal (no numeric OCR confidence in the contract).
  $: srcKind = display?.source || 'none'                       // manual | ocr | none
  $: detBib = display?.bib || ''
  $: ocrUncertain = srcKind === 'ocr' && !!detBib
  $: showBindGuess = manualUnbound && !!detBib

  function fmtOff(ms) {
    if (ms == null) return '—'
    const a = Math.abs(ms), s = ms >= 0 ? '+' : '−'
    return a < 1000 ? `${s}${a} мс` : `${s}${(a / 1000).toFixed(2)} с`
  }
  function offColor(ms) { return ms != null && Math.abs(ms) <= 100 ? 'var(--ok)' : 'var(--amber)' }
  $: tolText = tol < 1000 ? `±${tol} мс` : `±${(tol / 1000).toFixed(tol % 1000 ? 2 : 1)} с`
  function tolStep(dir) {
    const i = TOL_STEPS.indexOf(tol)
    const ni = Math.min(TOL_STEPS.length - 1, Math.max(0, (i < 0 ? 2 : i) + dir))
    tol = TOL_STEPS[ni]
  }
  const tlabel = ms => fmtTime(ms).slice(3) // drop HH for compact cell labels

  function selectFrame(ft) { selectedFt = ft }
  function adopt() { if (display) dispatch('adopt', display.ft) }
  function bindGuess() { if (detBib) dispatch('bindGuess', detBib) }
</script>

<aside class="photo-panel">
  <div class="phead">
    <span class="ptitle">Фотофиниш</span>
    {#if best}
      <button class="camchip" on:click={() => sourcesOpen = true}>
        <span class="cdot"></span><span class="mono">{cameraLabel}</span>
      </button>
    {/if}
    <div class="spacer"></div>
    <button class="srclink" on:click={() => sourcesOpen = true}>источники ↗</button>
  </div>

  <div class="pbody">
    {#if timeMs == null}
      <div class="empty">
        <span class="eicon">⏱</span>
        <span class="etitle">Введите время финиша</span>
        <span class="esub">Кадр с линии финиша подберётся автоматически по времени.</span>
      </div>
    {:else if error}
      <div class="empty">
        <span class="eicon">⚠</span>
        <span class="etitle">Нет связи</span>
        <span class="esub">{error}</span>
        <button class="eaction" on:click={fetchMatch}>Повторить</button>
      </div>
    {:else if !photos.length}
      {#if sourcesCount === 0}
        <div class="empty">
          <span class="eicon">📷</span>
          <span class="etitle">Источники не добавлены</span>
          <span class="esub">Включите «Локальную синхронизацию» в приложении Chrono Cam на телефоне-камере и добавьте его адрес.</span>
          <button class="eaction" on:click={() => sourcesOpen = true}>Добавить источник</button>
        </div>
      {:else}
        <div class="empty">
          <span class="eicon">🔍</span>
          <span class="etitle">Нет кадра в окне {tolText}</span>
          <span class="esub">Расширьте окно поиска или проверьте связь с телефонами. Кадров в базе: {photosInBase}.</span>
          <button class="eaction" on:click={() => tolStep(1)}>Расширить окно</button>
        </div>
      {/if}
    {:else}
      <!-- big frame -->
      <div class="frame big">
        {#if display?.url}
          <img src={display.url} alt="кадр финиша" />
        {:else}
          <div class="noimg">нет изображения</div>
        {/if}
        <div class="ov bibov">
          <span class="ovk">распознан номер</span>
          {#if detBib}
            <span class="bibnum mono">№{detBib}</span>
            <span class="srcbadge" class:manual={srcKind === 'manual'} class:ocr={srcKind === 'ocr'}>
              {srcKind === 'manual' ? '✓ подтверждён' : 'OCR · не подтверждён'}
            </span>
          {:else}
            <span class="bibnone">номер не распознан</span>
          {/if}
        </div>
        <div class="ov offov">
          <span class="ovk">офсет</span>
          <span class="offval mono" style="color:{offColor(offset)}">{fmtOff(offset)}</span>
        </div>
      </div>

      {#if ocrUncertain}
        <div class="warn">
          <span>⚠</span>
          <span>Номер распознан неуверенно. Кадр показан — сверьте номер и при необходимости впишите его в карточке.</span>
        </div>
      {/if}

      <!-- best match + adopt -->
      <div class="bestrow">
        <div class="bestinfo">
          <span class="bk">Лучшее совпадение по линии финиша</span>
          <span class="bv"><span class="mono" style="color:{offColor(bestOff)}">{fmtOff(bestOff)}</span> <span class="faint">от введённого времени</span></span>
        </div>
        <button class="adopt mono" on:click={adopt}>Принять {fmtTime(display.ft)}</button>
      </div>

      <!-- neighbour frames -->
      <div class="cellshead">
        <span class="chk">Соседние кадры · клик выбирает точное время</span>
        <div class="tol">
          <span class="faint">окно</span>
          <div class="tolbox">
            <button on:click={() => tolStep(-1)}>−</button>
            <span class="mono toln">{tolText}</span>
            <button on:click={() => tolStep(1)}>+</button>
          </div>
        </div>
      </div>
      <div class="cells">
        {#each cells as c (c.ft + c.url)}
          <button class="cell" class:sel={display && c.ft === display.ft} on:click={() => selectFrame(c.ft)}>
            <div class="cellimg">
              {#if c.url}<img src={c.url} alt="" loading="lazy"/>{/if}
              {#if c.ft === centerFt}<span class="centerline"></span>{/if}
            </div>
            <span class="celllab mono">{tlabel(c.ft)}</span>
          </button>
        {/each}
      </div>

      {#if showBindGuess}
        <div class="bindguess">
          <span>OCR распознал на кадре <b class="mono">№{detBib}</b> — привязать к этому финишу?</span>
          <button on:click={bindGuess}>Привязать №{detBib}</button>
        </div>
      {/if}
    {/if}
  </div>
</aside>

{#if sourcesOpen}
  <PhotoSources {eventId} on:changed={onSourcesChanged} on:close={() => sourcesOpen = false}/>
{/if}

<style>
  .photo-panel {
    position: fixed; top: 0; bottom: 0; right: 560px;
    width: min(640px, calc(100vw - 568px)); max-width: 640px;
    background: var(--bar); border-left: 1px solid var(--border);
    box-shadow: var(--shadow); z-index: 52;
    display: flex; flex-direction: column; overflow: hidden;
  }
  @media (max-width: 1180px) { .photo-panel { display: none; } }

  .phead { display: flex; align-items: center; gap: 12px; padding: 15px 20px; border-bottom: 1px solid var(--border); flex-shrink: 0; }
  .ptitle { font-size: 16px; font-weight: 700; }
  .spacer { flex: 1; }
  .camchip { display: flex; align-items: center; gap: 7px; padding: 5px 10px; border-radius: 8px; background: var(--surface2); border: 1px solid var(--border); font-size: 12px; color: var(--dim); cursor: pointer; }
  .cdot { width: 7px; height: 7px; border-radius: 50%; background: var(--live); }
  .srclink { background: none; border: none; cursor: pointer; font: inherit; font-size: 12px; color: var(--accent); font-weight: 600; white-space: nowrap; }

  .pbody { flex: 1; overflow-y: auto; padding: 18px 20px; }

  .empty {
    display: flex; flex-direction: column; align-items: center; justify-content: center; text-align: center;
    gap: 12px; padding: 56px 24px; border: 1px dashed var(--border2); border-radius: 14px; background: var(--surface);
  }
  .eicon { font-size: 34px; line-height: 1; }
  .etitle { font-size: 17px; font-weight: 700; }
  .esub { font-size: 13.5px; color: var(--faint); max-width: 360px; line-height: 1.5; }
  .eaction { cursor: pointer; margin-top: 4px; padding: 10px 18px; border-radius: 9px; background: var(--accent); color: var(--onaccent); border: none; font: inherit; font-size: 13.5px; font-weight: 600; }

  .frame { position: relative; width: 100%; border-radius: 12px; overflow: hidden; border: 1px solid var(--border2); background: #0b1018; }
  .frame.big { aspect-ratio: 16/9; }
  .frame img { width: 100%; height: 100%; object-fit: cover; display: block; }
  .noimg { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; color: var(--faint); font-size: 13px; }

  .ov { position: absolute; background: rgba(6, 10, 16, .74); border-radius: 11px; padding: 10px 13px; }
  .ovk { display: block; font-size: 10px; font-weight: 700; color: #9fb3c8; text-transform: uppercase; letter-spacing: .07em; }
  .bibov { left: 12px; top: 12px; display: flex; flex-direction: column; gap: 6px; border: 1px solid rgba(255, 255, 255, .09); }
  .bibnum { font-size: 26px; font-weight: 700; color: #fff; line-height: 1; }
  .srcbadge { font-size: 11px; font-weight: 700; color: var(--amber); }
  .srcbadge.manual { color: var(--ok); }
  .bibnone { font-size: 13px; color: #9fb3c8; }
  .offov { right: 12px; top: 12px; text-align: right; }
  .offval { font-size: 16px; font-weight: 700; }

  .warn { display: flex; align-items: center; gap: 10px; margin-top: 12px; padding: 11px 14px; border-radius: 10px; background: rgba(244, 183, 64, .13); border: 1px solid rgba(244, 183, 64, .42); font-size: 12.5px; line-height: 1.45; }

  .bestrow { display: flex; align-items: center; gap: 12px; margin-top: 14px; padding: 13px 16px; border-radius: 12px; background: var(--surface); border: 1px solid var(--border); flex-wrap: wrap; }
  .bestinfo { display: flex; flex-direction: column; gap: 3px; flex: 1; min-width: 170px; }
  .bk { font-size: 12.5px; color: var(--faint); }
  .bv { font-size: 15px; font-weight: 700; }
  .bv .faint { font-weight: 500; font-size: 13px; }
  .adopt { cursor: pointer; padding: 11px 18px; border-radius: 10px; background: var(--accent); color: var(--onaccent); border: none; font: inherit; font-size: 13.5px; font-weight: 700; white-space: nowrap; }

  .cellshead { display: flex; align-items: center; justify-content: space-between; margin: 18px 0 9px; gap: 10px; flex-wrap: wrap; }
  .chk { font-size: 12px; font-weight: 700; color: var(--faint); text-transform: uppercase; letter-spacing: .05em; }
  .tol { display: flex; align-items: center; gap: 8px; font-size: 11.5px; }
  .tolbox { display: flex; align-items: center; background: var(--input); border: 1px solid var(--border); border-radius: 8px; overflow: hidden; }
  .tolbox button { cursor: pointer; padding: 5px 10px; color: var(--faint); font-weight: 700; background: none; border: none; font: inherit; }
  .toln { font-size: 12px; font-weight: 600; padding: 0 4px; min-width: 56px; text-align: center; }

  .cells { display: flex; gap: 6px; align-items: flex-start; }
  .cell { flex: 1; min-width: 0; padding: 0; background: none; border: none; cursor: pointer; display: flex; flex-direction: column; gap: 4px; }
  .cellimg { position: relative; width: 100%; aspect-ratio: 16/9; border-radius: 7px; overflow: hidden; border: 1px solid var(--border); background: #0b1018; }
  .cell.sel .cellimg { border-color: var(--accent); box-shadow: 0 0 0 2px var(--accent); }
  .cellimg img { width: 100%; height: 100%; object-fit: cover; display: block; }
  .centerline { position: absolute; left: 50%; top: 0; transform: translateX(-50%); width: 2px; height: 100%; background: rgba(255, 255, 255, .65); box-shadow: 0 0 5px rgba(0, 0, 0, .6); }
  .celllab { font-size: 10.5px; color: var(--faint); text-align: center; }

  .bindguess { display: flex; align-items: center; gap: 12px; margin-top: 16px; padding: 13px 16px; border-radius: 12px; background: var(--surface2); border: 1px solid var(--border2); font-size: 13px; color: var(--dim); line-height: 1.45; }
  .bindguess b { color: var(--text); }
  .bindguess button { cursor: pointer; padding: 9px 15px; border-radius: 9px; background: var(--ok); color: #06210F; border: none; font: inherit; font-size: 13px; font-weight: 700; white-space: nowrap; }

  .mono { font-family: 'IBM Plex Mono', monospace; }
  .faint { color: var(--faint); }
</style>
