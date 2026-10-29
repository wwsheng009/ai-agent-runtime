// Verify the token/node metadata UX: searchable token list, status filter,
// visible token prefix with copy, copyable full node id, and date-safe wrapping.
//
// Usage (repo root):
//   node backend/scripts/verify-console-tokens.mjs
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.resolve(HERE, '..', '..');
const CONSOLE = path.join(REPO, 'backend', 'cmd', 'gateway', 'console');
const require = createRequire(path.join(REPO, 'frontend', 'package.json'));
const { JSDOM } = require('jsdom');
const html = fs.readFileSync(path.join(CONSOLE, 'index.html'), 'utf8');
const appjs = fs.readFileSync(path.join(CONSOLE, 'app.js'), 'utf8');
const css = fs.readFileSync(path.join(CONSOLE, 'style.css'), 'utf8');
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

let failures = 0;
function check(name, ok, extra) {
  if (!ok) { failures++; }
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? '  :: ' + extra : ''}`);
}
function response(body) {
  return { ok: true, status: 200, json: async () => body };
}
function makeDevice(i, revoked) {
  const n = String(i).padStart(2, '0');
  return {
    id: 'dev-record-' + n + '-0123456789abcdef',
    name: i === 3 ? 'special-alpha' : 'laptop-' + n,
    prefix: 'pfx-' + n + '-abcd',
    revoked,
    created_at: '2026-10-01T08:00:00Z',
    last_used: i === 0 || i === 3 ? '2026-10-20T09:30:00Z' : '0001-01-01T00:00:00Z',
  };
}
function makeNode(id, name) {
  return {
    node_id: id,
    name,
    kind: 'aicli',
    stale: false,
    capabilities: ['runner', 'status', 'sessions'],
    routes: ['/web', '/web/api'],
    last_heartbeat: new Date().toISOString(),
    sessions: [],
  };
}

(async () => {
  const devices = Array.from({ length: 20 }, (_, i) => makeDevice(i, i % 5 === 0));
  const nodes = [
    makeNode('node-alpha-0123456789abcdef', 'alpha-node'),
    makeNode('node-beta-fedcba9876543210', 'beta-node'),
  ];
  const dom = new JSDOM(html, {
    url: 'https://console.test/#tokens',
    runScripts: 'outside-only',
    pretendToBeVisual: true,
  });
  const { window } = dom;
  const doc = window.document;
  let copied = '';
  Object.defineProperty(window.navigator, 'clipboard', {
    configurable: true,
    value: { writeText: async (text) => { copied = String(text); } },
  });
  window.fetch = async (input) => {
    const url = typeof input === 'string' ? input : (input && input.url) || String(input);
    if (url.includes('/api/nodes')) { return response({ nodes }); }
    if (url.includes('/api/devices')) { return response({ devices }); }
    if (url.includes('/api/connectors')) { return response({ items: [] }); }
    if (url.includes('/api/push/config')) { return response({ enabled: false }); }
    if (url.includes('/api/presence')) { return response({ ok: true }); }
    if (url.includes('/api/notify/policy')) {
      return response({ policy: { version: 1, global: {}, sessions: {}, channels: {} }, meta: {} });
    }
    return response({});
  };
  class FakeEventSource {
    constructor(url) { this.url = url; }
    addEventListener() {}
    removeEventListener() {}
    close() {}
  }
  window.EventSource = FakeEventSource;
  if (!window.matchMedia) {
    window.matchMedia = () => ({ matches: false, addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {} });
  }
  if (!window.scrollTo) { window.scrollTo = () => {}; }
  if (!window.Element.prototype.scrollIntoView) { window.Element.prototype.scrollIntoView = () => {}; }
  if (!window.open) { window.open = () => null; }
  window.eval(appjs);
  await sleep(40);
  doc.dispatchEvent(new window.Event('DOMContentLoaded', { bubbles: true }));
  await sleep(120);

  const list = doc.getElementById('device-list');
  const cards = () => [...list.querySelectorAll('.device-card')];
  check('令牌列表渲染 20 条', cards().length === 20, String(cards().length));
  check('令牌计数可见', doc.getElementById('device-count').textContent === '20 个令牌' &&
    doc.getElementById('device-count').hidden === false);
  check('令牌标识有可见字段名', cards()[0].querySelector('.device-token-label').textContent === '令牌标识');
  check('令牌标识有复制入口', cards()[0].querySelector('.device-prefix-copy').textContent === '复制' &&
    cards()[0].querySelector('.device-prefix-copy').getAttribute('aria-label').includes('pfx-00-abcd'));
  cards()[0].querySelector('.device-prefix-copy').click();
  await sleep(20);
  check('复制的是令牌标识而不是完整令牌', copied === 'pfx-00-abcd', copied);

  const firstMeta = cards()[0].querySelector('.card-meta');
  check('令牌元信息有字段名', firstMeta.textContent.includes('记录 ID') &&
    firstMeta.textContent.includes('签发于') && firstMeta.textContent.includes('最近使用'),
    firstMeta.textContent);
  check('日期放在不换行的时间片段', firstMeta.querySelectorAll('.meta-time').length >= 2);
  check('卡片元信息不再用 break-all', !css.includes('.card-meta') || !/\.card-meta[^}]*word-break: break-all/s.test(css));
  check('时间片段禁止内部断行', css.includes('.card-meta .meta-time') && css.includes('white-space: nowrap'));

  const search = doc.getElementById('device-search');
  search.value = 'alpha';
  search.dispatchEvent(new window.Event('input', { bubbles: true }));
  check('令牌按名称搜索', cards().length === 1 && cards()[0].textContent.includes('special-alpha'));
  check('令牌搜索结果计数', doc.getElementById('device-count').textContent === '1 / 20 个令牌',
    doc.getElementById('device-count').textContent);
  search.value = 'pfx-17';
  search.dispatchEvent(new window.Event('input', { bubbles: true }));
  check('令牌可按标识搜索', cards().length === 1 && cards()[0].textContent.includes('pfx-17-abcd'));

  search.value = '';
  search.dispatchEvent(new window.Event('input', { bubbles: true }));
  const status = doc.getElementById('device-status-filter');
  status.value = 'revoked';
  status.dispatchEvent(new window.Event('change', { bubbles: true }));
  check('令牌按状态过滤', cards().length === 4 && cards().every((card) => card.textContent.includes('已撤销')),
    String(cards().length));
  check('令牌状态过滤计数', doc.getElementById('device-count').textContent === '4 / 20 个令牌',
    doc.getElementById('device-count').textContent);
  status.value = '';
  status.dispatchEvent(new window.Event('change', { bubbles: true }));

  search.value = 'zzz-no-match';
  search.dispatchEvent(new window.Event('input', { bubbles: true }));
  check('令牌空结果有说明', cards().length === 0 && list.textContent.includes('没有匹配的令牌'));
  search.value = '';
  search.dispatchEvent(new window.Event('input', { bubbles: true }));

  const nodeCards = [...doc.querySelectorAll('#node-live-list .card')];
  check('节点卡片渲染', nodeCards.length === 2, String(nodeCards.length));
  const copyNode = [...nodeCards[0].querySelectorAll('.card-actions button')]
    .find((btn) => btn.textContent === '复制节点 ID');
  check('节点卡片提供复制完整 ID', !!copyNode && copyNode.title.includes('node-alpha-0123456789abcdef'));
  copyNode.click();
  await sleep(20);
  check('复制节点 ID 写入完整值', copied === 'node-alpha-0123456789abcdef', copied);

  check('详情/令牌长值不使用 break-all', !css.includes('word-break: break-all'));
  window.close();
  console.log(`\ntotal=${failures ? 'failed' : 'ok'} failed=${failures}`);
  process.exit(failures ? 1 : 0);
})();
