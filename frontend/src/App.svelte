<script>
  import {onMount, onDestroy} from 'svelte'
  import {APIBaseURL} from '../wailsjs/go/main/App.js'
  import {setBase, call} from './lib/api.js'
  import {theme} from './lib/theme.js'
  import EventsList from './lib/EventsList.svelte'
  import WorkspaceHeader from './lib/WorkspaceHeader.svelte'
  import ResultsScreen from './lib/ResultsScreen.svelte'
  import EventSettings from './lib/EventSettings.svelte'
  import LiveScreen from './lib/LiveScreen.svelte'
  import MemberDrawer from './lib/MemberDrawer.svelte'

  let error = ''
  let busy = ''

  let view = 'events' // events | results | settings | live
  let events = []
  let currentEvent = null
  let races = []
  let categories = []
  let members = []
  let currentRace = null
  let protocol = null
  let reloadToken = 0 // bumped after recount/refresh so open child views reload

  // Participant drawer (opens over any in-event screen).
  let drawer = null // { memberId } | { capture: {id, time_ms} }

  // «Зафиксировать время» captures: wall-clock finishes the judge recorded
  // before assigning a number. Persisted server-side (pending_captures) so a
  // restart doesn't lose them; binding a number in the drawer turns one into a
  // manual finish (the existing endpoint) and deletes the capture.
  let captures = []

  // Light live-status poll to drive the header's pinned LIVE indicator from any
  // screen (the Live screen has its own faster feed poll while mounted).
  let liveStatus = {running: false, port: ''}
  let statusTimer = null
  let appVersion = null

  onMount(async () => {
    try {
      setBase(await APIBaseURL())
      appVersion = await call('GET', '/api/version').catch(() => null)
      await loadEvents()
    } catch (e) {
      error = `API недоступен: ${e}`
    }
  })
  onDestroy(() => clearInterval(statusTimer))

  async function pollStatus() {
    if (!currentEvent) return
    try {
      liveStatus = await call('GET', `/api/events/${currentEvent.id}/live/status`)
    } catch (_) { /* header indicator is best-effort */ }
  }

  async function loadEvents() {
    // The list endpoint now carries race_count/member_count directly (no N+1).
    events = await call('GET', '/api/events')
  }

  async function openEvent(ev) {
    currentEvent = ev
    currentRace = null
    protocol = null
    drawer = null
    await loadRaces()
    categories = await call('GET', `/api/events/${ev.id}/categories`)
    await loadMembers()
    await loadCaptures()
    if (races.length) await openRace(currentRace ?? races[0])
    view = 'results'
    await pollStatus()
    clearInterval(statusTimer)
    statusTimer = setInterval(pollStatus, 3000)
  }

  function backToEvents() {
    clearInterval(statusTimer)
    currentEvent = null
    protocol = null
    drawer = null
    view = 'events'
    loadEvents()
  }

  async function loadMembers() {
    members = await call('GET', `/api/events/${currentEvent.id}/members`)
  }

  async function loadRaces() {
    races = await call('GET', `/api/events/${currentEvent.id}/races`)
    if (currentRace) currentRace = races.find(r => r.id === currentRace.id) ?? null
  }

  async function openRace(race) {
    currentRace = race
    protocol = await call('GET', `/api/events/${currentEvent.id}/races/${race.id}/protocol`)
  }

  async function refreshProtocol() {
    if (currentRace) {
      protocol = await call('GET', `/api/events/${currentEvent.id}/races/${currentRace.id}/protocol`)
    }
    await loadMembers()
    reloadToken++
  }

  async function recount() {
    if (!currentEvent) return
    error = ''
    busy = 'Пересчёт…'
    try {
      await call('POST', `/api/events/${currentEvent.id}/recount`)
      await refreshProtocol()
    } catch (err) {
      error = `Пересчёт: ${err.message}`
    } finally {
      busy = ''
    }
  }

  // Any edit: recount if it changes a derivative, otherwise just re-read.
  // reloadRaces is set by race-level edits (start time / grouping flag) so the
  // displayed race fields refresh too.
  async function onEdited(e) {
    const d = e.detail || {}
    if (d.reloadRaces) await loadRaces()
    if (d.recount) await recount()
    else await refreshProtocol()
  }

  async function importEventFile(e) {
    const file = e.target.files[0]
    if (!file) return
    error = ''
    busy = 'Импорт события…'
    try {
      const stats = await call('POST', '/api/events/import', await file.text())
      if (stats.local_edits_reapplied > 0) {
        alert(`Импорт выполнен. Поверх применено локальных правок: ${stats.local_edits_reapplied}`)
      }
      await loadEvents()
      const ev = events.find(x => x.id === stats.event_id)
      if (ev) await openEvent(ev)
    } catch (err) {
      error = `Импорт события: ${err.message}`
    } finally {
      busy = ''
      e.target.value = ''
    }
  }

  // ---- drawer ----
  function openMember(memberId) { drawer = {memberId} }
  function openCapture(capture) { drawer = {capture} }
  function closeDrawer() { drawer = null }

  async function loadCaptures() {
    if (!currentEvent) return
    try {
      captures = await call('GET', `/api/events/${currentEvent.id}/captures`)
    } catch (_) { /* best-effort; an empty list is fine */ }
  }
  async function addCapture(timeMs) {
    if (!currentEvent) return
    try {
      const c = await call('POST', `/api/events/${currentEvent.id}/captures`, JSON.stringify({time_ms: timeMs}))
      captures = [c, ...captures]
    } catch (e) {
      error = `Захват времени: ${e.message}`
    }
  }
  async function removeCapture(id) {
    if (currentEvent) {
      try { await call('DELETE', `/api/events/${currentEvent.id}/captures/${id}`) } catch (_) { /* keep UI in sync anyway */ }
    }
    captures = captures.filter(c => c.id !== id)
  }
  // A capture became a persisted manual finish (number bound in the drawer):
  // drop the now-redundant pending capture.
  function captureBound(e) { removeCapture(e.detail.captureId) }

  function navigate(v) { view = v }
</script>

<div class="root" data-theme={$theme}>
  <div class="titlebar">
    <span class="tb-name">Chrono Desk</span>
    {#if appVersion}<span class="tb-ver" title="коммит {appVersion.commit}{appVersion.date ? ` · ${appVersion.date}` : ''}">v{appVersion.version}+{appVersion.build}</span>{/if}
    <div class="tb-dots"><span></span><span></span><span></span></div>
  </div>

  {#if error}<p class="banner error">{error}</p>{/if}
  {#if busy}<p class="banner busy">{busy}</p>{/if}

  {#if view === 'events'}
    <EventsList {events} on:open={e => openEvent(e.detail)} on:import={e => importEventFile(e.detail)}/>
  {:else}
    <WorkspaceHeader event={currentEvent} {view} {members} {races} {liveStatus}
                     on:back={backToEvents}
                     on:navigate={e => navigate(e.detail)}
                     on:select={e => openMember(e.detail)}/>

    {#if view === 'results'}
      <ResultsScreen eventId={currentEvent.id} {races} {categories} {currentRace} {protocol} {reloadToken}
                     on:selectRace={e => openRace(e.detail)}
                     on:openMember={e => openMember(e.detail)}
                     on:changed={onEdited}/>
    {:else if view === 'settings'}
      <EventSettings eventId={currentEvent.id} {currentRace} {reloadToken}
                     on:recount={recount}
                     on:changed={onEdited}
                     on:pulled={() => openEvent(currentEvent)}/>
    {:else if view === 'live'}
      <LiveScreen eventId={currentEvent.id} {members} {captures} {liveStatus}
                  on:status={e => liveStatus = e.detail}
                  on:capture={e => addCapture(e.detail)}
                  on:removeCapture={e => removeCapture(e.detail)}
                  on:openMember={e => openMember(e.detail)}
                  on:openCapture={e => openCapture(e.detail)}
                  on:changed={onEdited}/>
    {/if}
  {/if}

  {#if drawer}
    <MemberDrawer eventId={currentEvent.id} {races} {categories} {members} {reloadToken}
                  memberId={drawer.memberId ?? null}
                  capture={drawer.capture ?? null}
                  on:changed={onEdited}
                  on:captureBound={captureBound}
                  on:capture={e => addCapture(e.detail)}
                  on:close={closeDrawer}/>
  {/if}
</div>

<style>
  .root {
    min-height: 100vh;
    background: var(--bg);
    color: var(--text);
    text-align: left;
  }
  .titlebar {
    height: 38px; background: var(--bar);
    border-bottom: 1px solid var(--border);
    display: flex; align-items: center; justify-content: center;
    position: relative;
  }
  .tb-name { font-size: 12.5px; font-weight: 600; color: var(--dim); letter-spacing: .02em; }
  .tb-ver { font-size: 11px; color: var(--faint); margin-left: 8px; letter-spacing: .02em; }
  .tb-dots { position: absolute; right: 14px; display: flex; gap: 9px; }
  .tb-dots span { width: 12px; height: 12px; border-radius: 50%; background: var(--border2); }

  .banner { margin: 0; padding: 10px 24px; font-size: 13.5px; font-weight: 600; }
  .banner.error { color: var(--bad); background: var(--okbg); background: rgba(242,109,109,.12); }
  .banner.busy { color: var(--amber); }
</style>
