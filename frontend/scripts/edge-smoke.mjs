// Synthetic localhost API and browser actions for the built edge settings UI.
// This never starts a real reader, opens an event database, or contacts a site.
export function edgeSmokeFixture() {
  let running = false
  let bindings = [{board: 'Feibot:U659', source_session_id: 'initial-session'}]
  const calls = {save: 0, start: 0, stop: 0}
  const status = () => ({running: false, port: '', any_running: running, ips: ['127.0.0.1'], readers: [], edge: {running, port: '5085'}})
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
      if (path.endsWith('/edge/config')) {
        if (request.method === 'PUT') {
          let body = ''
          for await (const chunk of request) body += chunk
          bindings = JSON.parse(body).bindings
          calls.save++
        }
        value = {bindings, relay_pending: 0}
      }
      if (path.endsWith('/edge/start')) { running = true; calls.start++; value = status() }
      if (path.endsWith('/edge/stop')) { running = false; calls.stop++; value = status() }
      response.writeHead(200, {'Content-Type': 'application/json'})
      response.end(JSON.stringify(value))
      return true
    },
    verify(html) {
      if (!html.includes('data-edge-smoke="passed"') || calls.save !== 1 || calls.start !== 1 || calls.stop !== 1 || bindings[0]?.source_session_id !== 'changed-session') {
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
  const button = text => [...document.querySelectorAll('details.edge button')].find(b => b.textContent.trim() === text);
  try {
    (await wait(() => document.querySelector('button.event'))).click();
    (await wait(() => document.querySelector('button.live'))).click();
    (await wait(() => document.querySelector('details.edge summary'))).click();
    const input = await wait(() => {
      const inputs = document.querySelectorAll('details.edge .binding input');
      return inputs.length === 2 && !inputs[1].matches(':disabled') && inputs[1];
    });
    input.value = 'changed-session'; input.dispatchEvent(new Event('input', {bubbles:true}));
    await wait(() => button('Запустить edge-вход').disabled);
    button('Сохранить привязки').click();
    await Promise.resolve();
    await wait(() => !button('Сохранить привязки').matches(':disabled'));
    await wait(() => !button('Запустить edge-вход').disabled);
    button('Запустить edge-вход').click();
    await wait(() => button('Остановить edge-вход') && !button('Остановить edge-вход').disabled);
    if (!document.querySelector('details.edge fieldset').disabled) throw new Error('Active bindings remained editable');
    button('Остановить edge-вход').click();
    await wait(() => button('Запустить edge-вход') && !button('Запустить edge-вход').disabled);
    document.body.dataset.edgeSmoke = 'passed';
  } catch (error) {
    document.body.dataset.edgeSmoke = 'failed'; document.body.dataset.edgeSmokeError = String(error);
  }
});
</script>`
