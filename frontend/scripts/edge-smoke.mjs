// Synthetic localhost API and browser actions for the built edge settings UI.
// This never starts a real reader, opens an event database, or contacts a site.
export function edgeSmokeFixture() {
  let running = false
  let combined = false
  let port = ''
  let bindings = []
  let captures = []
  let nextCaptureID = 40
  let failedCaptureDelete = false
  const calls = {save: 0, start: 0, stop: 0, relay: 0, startWithoutBindings: 0}
  let relay = {endpoint: '', enabled: false, revision: 0}
  const status = () => ({running: false, port: '', any_running: running, ips: ['127.0.0.1'], readers: [], edge: {running, port, combined, received: 0, inserted: 0, duplicates: 0, errors: 0, last_error: running ? 'Проверочная ошибка привязки события' : ''}})
  return {
    async handle(request, response, path) {
      if (!path.startsWith('/api/')) return false
      let value = []
      if (request.headers.authorization !== 'Bearer edge-smoke-key') {
        response.writeHead(401); response.end('{}'); return true
      }
      if (path === '/api/version') value = {}
      if (path === '/api/events') value = [{id: '100', name: 'Synthetic edge UI', date: '2026-09-10', race_count: 0, member_count: 0}]
      if (path.endsWith('/live/status')) value = status()
      if (path.endsWith('/photos/status')) value = {photos_count: 0, finishes_count: 0}
      if (path.endsWith('/captures')) {
        if (request.method === 'POST') {
          let body = ''
          for await (const chunk of request) body += chunk
          value = {id: ++nextCaptureID, time_ms: JSON.parse(body).time_ms}
          captures.unshift(value)
        } else value = captures
      }
      if (request.method === 'DELETE' && path.includes('/captures/')) {
        if (!failedCaptureDelete) {
          failedCaptureDelete = true
          response.writeHead(500, {'Content-Type': 'application/json'})
          response.end(JSON.stringify({error: 'Проверочная ошибка удаления'}))
          return true
        }
        captures = captures.filter(capture => capture.id !== Number(path.split('/').pop()))
        value = {ok: true}
      }
      if (path.endsWith('/edge/config')) {
        if (request.method === 'PUT') {
          let body = ''
          for await (const chunk of request) body += chunk
          bindings = JSON.parse(body).bindings
          bindings = bindings.map(b => b.board.startsWith('Feibot:') && !b.source_session_id ? {...b, source_session_id: '100'} : b)
          calls.save++
        }
        value = {bindings, relay_pending: 0, automatic_feibot: calls.save === 0}
      }
      if (path.endsWith('/edge/relay')) {
        if (request.method === 'PUT') {
          let body = ''
          for await (const chunk of request) body += chunk
          const requested = JSON.parse(body)
          if (requested.revision !== relay.revision || (requested.endpoint !== relay.endpoint && !requested.confirm_pending)) {
            response.writeHead(409); response.end('{}'); return true
          }
          relay = {endpoint: requested.endpoint, enabled: requested.enabled, tls_bundle: requested.tls_bundle || '', revision: relay.revision + 1}
          calls.relay++
        }
        value = {config: relay, running: relay.enabled, progress: {pending: 3, acked: 0, attempts: 1, last_error: 'Проверочная ошибка связи'}, last_error: ''}
      }
      if (path.endsWith('/edge/start')) {
        let body = ''
        for await (const chunk of request) body += chunk
        const requested = JSON.parse(body)
        combined = requested.combined; port = requested.port
        running = true; calls.start++; value = status()
      }
      if (path.endsWith('/edge/stop')) { running = false; calls.stop++; value = status() }
      if (path.endsWith('/live/start')) {
        let body = ''
        for await (const chunk of request) body += chunk
        const requested = JSON.parse(body)
        if (Object.keys(requested).join(',') !== 'port') throw new Error('Ordinary Start supplied Edge provisioning')
        port = requested.port
        combined = true
        if (!bindings.length && !calls.save) calls.startWithoutBindings++
        running = true; calls.start++; value = status()
      }
      if (path.endsWith('/live/stop')) { running = false; calls.stop++; value = status() }
      response.writeHead(200, {'Content-Type': 'application/json'})
      response.end(JSON.stringify(value))
      return true
    },
    verify(html) {
      if (!failedCaptureDelete || captures.length !== 1 || captures[0].id !== 44 || !html.includes('Отметка №1 · ручной финиш')) {
        throw new Error('Manual capture numbering did not survive deletion and event reopen')
      }
      if (!html.includes('data-edge-smoke="passed"') || calls.save !== 2 || calls.start !== 1 || calls.startWithoutBindings !== 1 || calls.stop !== 1 || calls.relay !== 2 || relay.enabled || relay.endpoint !== 'tls://hub.test:44004' || relay.tls_bundle !== '/synthetic/desk-only' || bindings[0]?.board !== 'Feibot:U659A' || bindings[0]?.source_session_id !== '100' || bindings[1]?.board !== 'Feibot:U660' || bindings[1]?.source_session_id !== '100' || combined !== true || port !== '5084') {
        throw new Error(`Edge UI did not complete save/start/stop: ${JSON.stringify(calls)}\n${html.slice(-4000)}`)
      }
    }
  }
}

export const edgeSmokeBootstrap = `<script>
window.go = {main: {App: {APIBaseURL: async () => location.origin, APIToken: async () => 'edge-smoke-key'}}};
// Poll scheduling is covered separately; this smoke exercises explicit UI actions.
window.setInterval = () => 0;
window.addEventListener('DOMContentLoaded', async () => {
  const wait = async predicate => {
    for (let attempt = 0; attempt < 600; attempt++) {
      const result = predicate(); if (result) return result;
      await new Promise(resolve => setTimeout(resolve, 10));
    }
    throw new Error('Edge smoke timed out waiting for UI');
  };
  const edgeButton = text => [...document.querySelectorAll('details.edge button')].find(b => b.textContent.trim() === text);
  const button = text => [...document.querySelectorAll('button')].find(b => b.textContent.trim() === text);
  try {
    (await wait(() => document.querySelector('button.event'))).click();
    (await wait(() => document.querySelector('button.live'))).click();
    const captureRows = () => [...document.querySelectorAll('.row.capture')];
    const captureCount = () => document.querySelector('.capture-count strong')?.textContent;
    const captureButton = await wait(() => button('⏱ Зафиксировать время'));
    captureButton.click(); captureButton.click(); captureButton.click();
    await wait(() => captureRows().length === 3 && captureCount() === '3');
    if (captureRows().map(row => row.querySelector('.name').textContent).join('|') !== 'Отметка №3 · ручной финиш|Отметка №2 · ручной финиш|Отметка №1 · ручной финиш') throw new Error('Capture numbers used storage IDs');
    captureRows()[1].click();
    await wait(() => document.querySelector('.dtitle')?.textContent === 'Отметка №2 · ручной финиш');
    document.querySelector('.drawer .x').click();
    await wait(() => !document.querySelector('.drawer'));
    captureRows()[1].querySelector('.del').click();
    await wait(() => document.querySelector('.banner.error')?.textContent.includes('Проверочная ошибка удаления'));
    if (captureRows().length !== 3 || captureCount() !== '3') throw new Error('Failed deletion changed capture count');
    captureRows()[1].querySelector('.del').click();
    await wait(() => captureRows().length === 2 && captureCount() === '2');
    if (captureRows().map(row => row.querySelector('.name').textContent).join('|') !== 'Отметка №2 · ручной финиш|Отметка №1 · ручной финиш') throw new Error('Middle deletion did not renumber captures');
    captureRows()[0].click();
    await wait(() => document.querySelector('.dtitle')?.textContent === 'Отметка №2 · ручной финиш');
    document.querySelector('.drawer .x').click();
    await wait(() => !document.querySelector('.drawer'));
    captureRows()[0].querySelector('.del').click();
    await wait(() => captureRows().length === 1 && captureCount() === '1');
    captureRows()[0].querySelector('.del').click();
    await wait(() => captureRows().length === 0 && captureCount() === '0');
    captureButton.click();
    await wait(() => captureRows()[0]?.querySelector('.name').textContent === 'Отметка №1 · ручной финиш' && captureCount() === '1');
    document.querySelector('button.back').click();
    (await wait(() => document.querySelector('button.event'))).click();
    (await wait(() => document.querySelector('button.live'))).click();
    await wait(() => captureRows()[0]?.querySelector('.name').textContent === 'Отметка №1 · ручной финиш' && captureCount() === '1');
    const advanced = await wait(() => document.querySelector('details.edge'));
    if (advanced.open || button('Добавить Feibot')) throw new Error('Ordinary Feibot requires an Edge setup visit');
    const guidance = await wait(() => document.querySelector('.edge-status'));
    if (guidance.closest('details') || !guidance.textContent.includes('копировать сессию')) throw new Error('Ordinary workflow guidance hidden');
    // Closed details can retain layout boxes in Chromium; test actual paint
    // visibility, not getClientRects(), for its content-visibility boundary.
    if ([...advanced.querySelectorAll('input, button')].some(element => element.checkVisibility())) throw new Error('Advanced source inputs visible before Start');
    (await wait(() => button('Запустить приём') && !button('Запустить приём').disabled && button('Запустить приём'))).click();
    await wait(() => button('Остановить все входы') && !button('Остановить все входы').disabled);
    const inputError = await wait(() => guidance.querySelector('[role="alert"]'));
    if (inputError.closest('details') || !inputError.checkVisibility() || advanced.open) throw new Error('Live admission error hidden in advanced controls');
    (await wait(() => document.querySelector('details.edge summary'))).click();
    if (!document.querySelector('details.edge fieldset').disabled) throw new Error('Active bindings remained editable');
    button('Остановить все входы').click();
    await wait(() => button('Запустить приём') && !button('Запустить приём').disabled && !document.querySelector('details.edge fieldset').disabled);
    edgeButton('Добавить явный источник').click();
    const input = await wait(() => document.querySelector('details.edge .binding input'));
    input.value = 'Feibot:U659A'; input.dispatchEvent(new Event('input', {bubbles:true}));
    edgeButton('Сохранить источники').click();
    await Promise.resolve();
    await wait(() => !edgeButton('Сохранить источники').matches(':disabled'));
    const restriction = await wait(() => guidance.querySelector('[role="status"]'));
    if (restriction.closest('details')) throw new Error('Explicit-only policy warning hidden');
    edgeButton('Добавить явный источник').click();
    const added = await wait(() => document.querySelectorAll('details.edge .binding')[1]);
    const board = added.querySelector('input');
    board.value = 'Feibot:U660'; board.dispatchEvent(new Event('input', {bubbles:true}));
    await wait(() => added.textContent.includes('Сессия Feibot: событие 100'));
    edgeButton('Сохранить источники').click();
    await wait(() => !edgeButton('Сохранить источники').matches(':disabled'));
    const relaySection = await wait(() => document.querySelector('section[aria-label="Досылка sidecar в Hub"]'));
    const relayAddress = await wait(() => {
      const element = relaySection.querySelector('input[type="text"], input.input');
      return element && !element.matches(':disabled') && element;
    });
    relayAddress.value = 'tls://hub.test:44004'; relayAddress.dispatchEvent(new Event('input', {bubbles:true}));
    const bundle = await wait(() => relaySection.querySelectorAll('input.input')[1]);
    bundle.value = '/synthetic/desk-only'; bundle.dispatchEvent(new Event('input', {bubbles:true}));
    relaySection.querySelector('input[type="checkbox"]').click();
    await wait(() => button('Сохранить досылку').disabled);
    (await wait(() => relaySection.querySelectorAll('input[type="checkbox"]')[1])).click();
    await wait(() => !button('Сохранить досылку').matches(':disabled'));
    button('Сохранить досылку').click();
    await wait(() => relaySection.textContent.includes('Отправитель: включён') && !button('Сохранить досылку').matches(':disabled'));
    relaySection.querySelector('input[type="checkbox"]').click();
    button('Сохранить досылку').click();
    await wait(() => relaySection.textContent.includes('Отправитель: остановлен') && !button('Сохранить досылку').matches(':disabled'));
    if (!relaySection.textContent.includes('ожидают: 3')) throw new Error('Pause hid pending queue');
    document.body.dataset.edgeSmoke = 'passed';
  } catch (error) {
    document.body.dataset.edgeSmoke = 'failed'; document.body.dataset.edgeSmokeError = String(error);
  }
});
</script>`
