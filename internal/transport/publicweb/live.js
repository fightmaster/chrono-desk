// Read-only presenter view. Each response replaces the bounded committed snapshot.
export function createFinishFeed({mount, onMeta, onTab, fetcher = fetch, interval = 500}) {
  let active = false, busy = false, generation = 0, timer, statusTimer, controller
  let race = '', eventKey = '', initialized = false, connected = false, lastSuccess = 0, failures = 0
  let list, selector, state, updated, empty, jump, optionsSignature = ''
  const cards = new Map(), highlights = new Set()

  function schedule(delay) {
    clearTimeout(timer)
    if (active && !document.hidden) timer = setTimeout(request, delay)
  }

  function updateStatus() {
    if (!active) return
    state.textContent = connected ? 'Связь с Chrono Desk установлена' : 'Нет обновлений — проверяем связь с Chrono Desk'
    state.classList.toggle('disconnected', !connected)
    updated.textContent = lastSuccess
      ? `Обновлено ${new Date(lastSuccess).toLocaleTimeString('ru-RU')} · ${Math.max(0, Math.floor((Date.now() - lastSuccess) / 1000))} с назад`
      : 'Ожидаем данные Chrono Desk'
  }

  function clearRows() {
    cards.clear()
    list.replaceChildren()
    initialized = false
  }

  function resetRequest() {
    generation++
    clearTimeout(timer)
    controller?.abort()
    if (!busy) schedule(0)
  }

  function visibility() {
    initialized = false
    resetRequest()
  }

  function online() {
    failures = 0
    initialized = false
    resetRequest()
  }

  function scroll() { if (jump) jump.hidden = window.scrollY < 120 }

  function reconcile(data) {
    const changedEvent = eventKey && eventKey !== data.event_key
    if (changedEvent) {
      clearRows()
      race = ''
      selector.value = ''
      optionsSignature = ''
    }
    eventKey = data.event_key
    onMeta(data)
    const options = JSON.stringify(data.races)
    if (options !== optionsSignature) {
      selector.replaceChildren(new Option('Все дистанции', ''))
      for (const option of data.races) selector.add(new Option(option.name, option.id))
      selector.value = race
      optionsSignature = options
    }
    // A filtered reply from a newly published event must be reloaded unfiltered.
    if (changedEvent && data.filtered) { resetRequest(); return }
    const rows = [...new Map(data.rows.map(row => [row.key, row])).values()]
    const atTop = window.scrollY < 120
    const anchor = atTop ? null : [...list.children].find(node => node.getBoundingClientRect().bottom > 0)
    const anchorTop = anchor?.getBoundingClientRect().top
    const keys = new Set(rows.map(row => row.key))
    for (const [key, card] of cards) {
      if (!keys.has(key)) { card.remove(); cards.delete(key) }
    }
    rows.forEach((row, index) => {
      let card = cards.get(row.key)
      if (!card) {
        card = document.createElement('article')
        card.className = 'finish-card'
        card.dataset.key = row.key
        card.innerHTML = '<div class="finish-heading"><span class="finish-number"></span><span class="finish-name"></span></div><div class="finish-details"><span class="finish-race"></span><span class="finish-time"></span></div><div class="finish-places"></div><div class="finish-extra"><div class="finish-club"></div><div class="finish-team"></div><div class="finish-city"></div></div>'
        cards.set(row.key, card)
        if (initialized && connected) {
          card.classList.add('finish-new')
          const highlight = setTimeout(() => { card.classList.remove('finish-new'); highlights.delete(highlight) }, 3000)
          highlights.add(highlight)
        }
      }
      card.querySelector('.finish-number').textContent = row.number == null ? 'Без номера' : `№${row.number}`
      card.querySelector('.finish-name').textContent = [row.last_name, row.first_name].filter(Boolean).join(' ') || 'Участник'
      card.querySelector('.finish-race').textContent = row.race_name
      card.querySelector('.finish-time').textContent = row.clean_time
      const extra = card.querySelector('.finish-extra')
      const club = extra.querySelector('.finish-club'), team = extra.querySelector('.finish-team'), city = extra.querySelector('.finish-city')
      club.textContent = row.club ? `Клуб: ${row.club}` : ''
      club.hidden = !row.club
      team.textContent = row.team ? `Команда: ${row.team}` : ''
      team.hidden = !row.team || row.team === row.club
      city.textContent = row.city || ''
      city.hidden = !row.city
      extra.hidden = !row.club && !row.team && !row.city
      const places = []
      const gender = {male: 'Мужчины', female: 'Женщины'}[row.gender]
      if (gender && row.gender_place != null) places.push(`${gender}: ${row.gender_place}`)
      if (row.category_name || row.category_place != null) {
        places.push('Группа' + (row.category_name ? ` ${row.category_name}` : '') + (row.category_place != null ? `: ${row.category_place}` : ''))
      }
      const placeLine = card.querySelector('.finish-places')
      placeLine.textContent = places.join(' · ')
      placeLine.hidden = !places.length
      if (list.children[index] !== card) list.insertBefore(card, list.children[index] || null)
    })
    if (anchor?.isConnected) window.scrollBy(0, anchor.getBoundingClientRect().top - anchorTop)
    empty.hidden = rows.length > 0
    empty.textContent = data.published ? 'Подтверждённых финишей пока нет' : 'Трансляция результатов выключена'
    initialized = data.published
    scroll()
  }

  async function request() {
    if (!active || document.hidden || busy) return
    busy = true
    const current = generation
    controller = new AbortController()
    const deadline = setTimeout(() => controller?.abort(), 1500)
    let delay = interval
    try {
      const response = await fetcher('/api/live/finishes' + (race ? '?race_id=' + encodeURIComponent(race) : ''), {
        cache: 'no-store', signal: controller.signal,
      })
      if (current !== generation || !active) return
      if (response.status === 400 && race) {
        race = ''; selector.value = ''; clearRows(); generation++
        return
      }
      if (!response.ok) throw new Error('finish feed unavailable')
      const data = await response.json()
      if (current !== generation || !active) return
      if (!Array.isArray(data.rows) || !Array.isArray(data.races) || typeof data.published !== 'boolean') throw new Error('invalid finish feed')
      const wasConnected = connected
      if (!wasConnected) initialized = false
      if (!data.published) { clearRows(); eventKey = ''; optionsSignature = '' }
      reconcile(data)
      connected = true
      lastSuccess = Date.now()
      failures = 0
    } catch (_) {
      if (current === generation && active) {
        connected = false
        initialized = false
        delay = [1000, 2000, 5000][Math.min(failures++, 2)]
      }
    } finally {
      clearTimeout(deadline)
      busy = false
      if (active) { updateStatus(); schedule(current === generation ? delay : 0) }
    }
  }

  return {
    start() {
      if (active) return
      active = true; generation++; race = ''; eventKey = ''; initialized = false; connected = false; lastSuccess = 0; failures = 0; optionsSignature = ''
      cards.clear()
      mount.innerHTML = '<div class="seg"><button class="seg-item" data-tab="awards">Призёры</button><button class="seg-item" data-tab="protocol">Протокол</button><button class="seg-item active" data-tab="live">Финиши</button></div><h2 class="finish-title">Последние 10 финишей</h2><select class="dist" id="finish-race" aria-label="Дистанция"><option value="">Все дистанции</option></select><div id="finish-state" class="finish-state" role="status"></div><div id="finish-updated" class="finish-updated"></div><div id="finish-list" aria-label="Последние финишировавшие"></div><div id="finish-empty" class="empty">Загрузка финишей…</div><button id="finish-jump" class="finish-jump" hidden>К новым ↑</button>'
      list = mount.querySelector('#finish-list'); selector = mount.querySelector('#finish-race')
      state = mount.querySelector('#finish-state'); updated = mount.querySelector('#finish-updated')
      empty = mount.querySelector('#finish-empty'); jump = mount.querySelector('#finish-jump')
      mount.querySelectorAll('[data-tab]').forEach(button => button.addEventListener('click', () => onTab(button.dataset.tab)))
      selector.addEventListener('change', () => { race = selector.value; clearRows(); resetRequest() })
      jump.addEventListener('click', () => window.scrollTo({top: 0, behavior: 'smooth'}))
      document.addEventListener('visibilitychange', visibility)
      window.addEventListener('online', online)
      window.addEventListener('scroll', scroll, {passive: true})
      statusTimer = setInterval(updateStatus, 1000)
      updateStatus(); schedule(0)
    },
    stop() {
      if (!active) return
      active = false; generation++; controller?.abort()
      clearTimeout(timer); clearInterval(statusTimer)
      for (const highlight of highlights) clearTimeout(highlight)
      highlights.clear()
      document.removeEventListener('visibilitychange', visibility)
      window.removeEventListener('online', online)
      window.removeEventListener('scroll', scroll)
    },
  }
}
