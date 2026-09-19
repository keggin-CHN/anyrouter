const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const path = require('node:path');
const { test } = require('node:test');
const vm = require('node:vm');

const html = readFileSync(path.join(__dirname, '../web/index.html'), 'utf8');
const script = html.match(/<script>([\s\S]*?)<\/script>/)[1];

// Exercise the shipped dashboard logic without a browser or external dependencies.
function dashboard(respond) {
  const elements = new Map();
  const calls = [];
  const alerts = [];
  const timers = [];
  const element = id => {
    if (!elements.has(id)) elements.set(id, {
      value: '', checked: false, innerHTML: '', innerText: '', style: {},
      scrollHeight: 0, clientHeight: 0, scrollTop: 0,
      reportValidity: () => true,
      classList: { add() {}, remove() {}, toggle() {} },
    });
    return elements.get(id);
  };
  const document = {
    hidden: false, getElementById: element, querySelectorAll: () => [], addEventListener() {},
  };
  const context = vm.createContext({
    console, AbortSignal, document,
    window: { addEventListener() {} },
    alert: message => alerts.push(message),
    setTimeout: (callback, delay) => timers.push({ callback, delay }),
    fetch: async (url, options) => {
      calls.push({ url, options });
      if (respond) return respond(url, options);
      return { ok: true, json: async () => url === '/api/status' ? { is_running: false, states: [] } : { ok: true } };
    },
  });
  vm.runInContext(script, context, { filename: 'web/index.html' });
  vm.runInContext('configData = { selected_models: ["gpt-6-astra"] };', context);
  for (const [id, value] of Object.entries({
    'keys-input': 'test-key', 'cfg-tries': '50', 'cfg-intra-delay': '5',
    'cfg-cooldown': '30', 'cfg-interval': '30', 'cfg-retries': '3',
    'cfg-prompts-input': 'ping', 'cfg-proxy': 'direct', 'cfg-close-behavior': 'silent',
  })) element(id).value = value;
  return { context, calls, alerts, element, timers, document };
}

test('invalid keys or empty model selection never start workers', async () => {
  const noKeys = dashboard();
  noKeys.element('keys-input').value = '  ';
  await noKeys.context.startKeepalive();
  assert.equal(noKeys.calls.length, 0);
  assert.match(noKeys.alerts[0], /API Key/);

  const noModels = dashboard();
  vm.runInContext('configData.selected_models = [];', noModels.context);
  await noModels.context.startKeepalive();
  assert.equal(noModels.calls.length, 0);
  assert.match(noModels.alerts[0], /模型/);
});

test('save failures are visible and prevent the subsequent start request', async () => {
  const page = dashboard(async () => ({ ok: false, status: 500, text: async () => 'disk is full' }));
  await page.context.startKeepalive();
  assert.deepEqual(page.calls.map(call => call.url), ['/api/config']);
  assert.match(page.alerts[0], /disk is full/);
});

test('saving a custom schedule preserves its values and does not start workers', async () => {
  const page = dashboard();
  assert.equal(await page.context.saveAllConfig(false), true);
  assert.deepEqual(page.calls.map(call => call.url), ['/api/config', '/api/status']);
  assert.equal(JSON.parse(page.calls[0].options.body).tries_per_round, 50);
  assert.equal(vm.runInContext('isRunning', page.context), false);
});

test('repeated clicks do not duplicate save or start requests', async () => {
  const page = dashboard();
  await Promise.all([page.context.startKeepalive(), page.context.startKeepalive()]);
  assert.equal(page.calls.filter(call => call.url === '/api/config').length, 1);
  assert.equal(page.calls.filter(call => call.url === '/api/start').length, 1);
});

test('polling waits for completion and pauses when the dashboard is hidden', async () => {
  const page = dashboard();
  let calls = 0;
  let finish;
  page.context.pollWhenVisible(() => { calls++; return new Promise(resolve => { finish = resolve; }); }, 1000);
  const pending = page.timers.shift().callback();
  assert.equal(calls, 1);
  assert.equal(page.timers.length, 0);
  finish();
  await pending;
  assert.equal(page.timers.length, 1);
  page.document.hidden = true;
  await page.timers.shift().callback();
  assert.equal(calls, 1);
  assert.equal(page.timers[0].delay, 5000);
});

test('unchanged logs preserve the DOM and an empty response clears stale logs', () => {
  const page = dashboard();
  const entries = [{ id: 1, timestamp: '2026-09-19T00:00:00Z', text: 'hello', level: 'info' }];
  page.context.renderLogLines(entries);
  assert.match(page.element('log-scroller').innerHTML, /hello/);
  page.element('log-scroller').innerHTML = 'preserved selection';
  page.context.renderLogLines(entries);
  assert.equal(page.element('log-scroller').innerHTML, 'preserved selection');
  page.context.renderLogLines([]);
  assert.equal(page.element('log-scroller').innerHTML, '');
});

test('key strings are escaped for HTML attributes without inline JavaScript interpolation', () => {
  const page = dashboard();
  assert.equal(page.context.escapeHtml(`a'"<>&`), 'a&#39;&quot;&lt;&gt;&amp;');
  assert.match(html, /onclick="copyKeyText\(this\.dataset\.key, this\)"/);
});
