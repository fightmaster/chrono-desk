<script>
  import {createEventDispatcher} from 'svelte'
  import {fmtTime, imgURL} from './api.js'

  export let eventId
  export let photo            // {bib, bib_source, time_ms, camera_label, best_photo_url, frames:[{timestamp_epoch_ms,url}]}
  export let canOpenMember = false

  const dispatch = createEventDispatcher()

  // The burst frames (fall back to the single best photo).
  $: frames = (photo.frames && photo.frames.length)
    ? photo.frames
    : (photo.best_photo_url ? [{timestamp_epoch_ms: photo.time_ms, url: photo.best_photo_url}] : [])

  // Open on the BEST frame, not the first.
  let idx = 0
  let seeded = false
  $: if (!seeded && frames.length) {
    const b = frames.findIndex(f => f.url === photo.best_photo_url)
    idx = b >= 0 ? b : Math.floor(frames.length / 2)
    seeded = true
  }
  $: cur = frames[idx] || null
  $: frameTime = cur ? cur.timestamp_epoch_ms : photo.time_ms

  function prev() { idx = Math.max(0, idx - 1) }
  function next() { idx = Math.min(frames.length - 1, idx + 1) }
  function onKey(e) {
    if (e.key === 'ArrowLeft') { prev(); e.preventDefault() }
    else if (e.key === 'ArrowRight') { next(); e.preventDefault() }
    else if (e.key === 'Escape') dispatch('close')
  }
</script>

<svelte:window on:keydown={onKey}/>
<div class="overlay" on:click={() => dispatch('close')}></div>
<div class="box">
  <div class="head">
    <div class="info">
      {#if photo.bib}
        <span class="bib mono" class:ocr={photo.bib_source === 'ocr'}>№{photo.bib}</span>
        {#if photo.bib_source === 'ocr'}<span class="tag ocr">OCR · не подтверждён</span>{:else if photo.bib_source === 'manual'}<span class="tag ok">✓ подтверждён</span>{/if}
      {:else}
        <span class="nobib">номер не распознан</span>
      {/if}
      <span class="mono faint">{photo.camera_label || 'камера'}</span>
    </div>
    <button class="x" on:click={() => dispatch('close')}>×</button>
  </div>

  <div class="stage">
    <button class="nav" on:click={prev} disabled={idx === 0} aria-label="Предыдущий кадр">‹</button>
    {#if cur}<img src={imgURL(eventId, cur.url, 1280)} alt="кадр финиша"/>{:else}<div class="noimg">нет кадра</div>{/if}
    <button class="nav" on:click={next} disabled={idx >= frames.length - 1} aria-label="Следующий кадр">›</button>
    <span class="tc mono">{fmtTime(frameTime)}</span>
    <span class="counter mono">{idx + 1}/{frames.length}</span>
  </div>

  <div class="strip">
    {#each frames as f, i (f.url + i)}
      <button class="cell" class:sel={i === idx} on:click={() => idx = i}>
        <img src={imgURL(eventId, f.url, 240)} alt="" loading="lazy"/>
      </button>
    {/each}
  </div>

  <div class="actions">
    <span class="hint faint">← → листать кадры · выберите кадр пересечения линии</span>
    <div class="btns">
      {#if canOpenMember}<button class="btn" on:click={() => dispatch('openMember', photo.bib)}>Открыть карточку №{photo.bib}</button>{/if}
      <button class="btn primary" on:click={() => dispatch('fixTime', frameTime)}>Зафиксировать это время</button>
    </div>
  </div>
</div>

<style>
  .overlay { position: fixed; inset: 0; background: rgba(4, 9, 18, .72); z-index: 80; }
  .box {
    position: fixed; inset: 4vh 4vw; z-index: 81; display: flex; flex-direction: column;
    background: var(--surface); border: 1px solid var(--border2); border-radius: 16px;
    box-shadow: var(--shadow); overflow: hidden;
  }
  .head { display: flex; align-items: center; justify-content: space-between; padding: 12px 16px; border-bottom: 1px solid var(--border); flex-shrink: 0; }
  .info { display: flex; align-items: center; gap: 12px; }
  .bib { font-size: 22px; font-weight: 700; color: var(--accent); }
  .bib.ocr { color: var(--amber); }
  .nobib { font-size: 15px; color: var(--faint); }
  .tag { font-size: 11px; font-weight: 700; padding: 2px 7px; border-radius: 6px; }
  .tag.ocr { color: var(--amber); border: 1px solid var(--amber); }
  .tag.ok { color: var(--ok); border: 1px solid var(--okborder); }
  .x { cursor: pointer; background: none; border: none; color: var(--faint); font-size: 24px; line-height: 1; padding: 4px; }

  .stage { position: relative; flex: 1; min-height: 0; display: flex; align-items: center; justify-content: center; background: #0b1018; }
  .stage img { max-width: 100%; max-height: 100%; object-fit: contain; display: block; }
  .noimg { color: var(--faint); font-size: 14px; }
  .nav {
    position: absolute; top: 50%; transform: translateY(-50%); cursor: pointer;
    width: 48px; height: 64px; border: none; border-radius: 10px;
    background: rgba(6, 10, 16, .55); color: #fff; font-size: 32px; line-height: 1;
  }
  .nav:first-of-type { left: 12px; }
  .nav:nth-of-type(2) { right: 12px; }
  .nav:disabled { opacity: .25; cursor: default; }
  .tc { position: absolute; left: 12px; bottom: 12px; background: rgba(6, 10, 16, .7); color: #ffd34d; font-size: 15px; font-weight: 600; padding: 4px 9px; border-radius: 7px; }
  .counter { position: absolute; right: 12px; bottom: 12px; background: rgba(6, 10, 16, .7); color: #cfe0f2; font-size: 13px; padding: 4px 9px; border-radius: 7px; }

  .strip { display: flex; gap: 6px; padding: 10px 12px; overflow-x: auto; border-top: 1px solid var(--border); flex-shrink: 0; }
  .cell { flex: 0 0 auto; width: 96px; height: 54px; padding: 0; border: 2px solid transparent; border-radius: 7px; overflow: hidden; cursor: pointer; background: #0b1018; }
  .cell.sel { border-color: var(--accent); }
  .cell img { width: 100%; height: 100%; object-fit: cover; display: block; }

  .actions { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 12px 16px; border-top: 1px solid var(--border); flex-shrink: 0; flex-wrap: wrap; }
  .hint { font-size: 12px; }
  .btns { display: flex; gap: 10px; }
  .btn { cursor: pointer; padding: 10px 16px; border-radius: 9px; border: 1px solid var(--border2); background: var(--surface2); color: var(--text); font: inherit; font-size: 13.5px; font-weight: 600; }
  .btn.primary { background: var(--accent); color: var(--onaccent); border-color: var(--accent); }
  .mono { font-family: 'IBM Plex Mono', monospace; }
  .faint { color: var(--faint); }
</style>
