// UX-06 regression: destructive card actions must live behind an overflow menu,
// use the in-page confirm dialog (not window.confirm), expose consequence text,
// restore focus, and keep a single high-weight action per card.
//
// Usage (repo root):
//   node backend/scripts/verify-console-danger.mjs
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
function response(body, status = 200) {
  return { ok: status >= 200 && status < 300, status, json: async () => body };
}

const now = new Date().toISOString();
const liveNode = {
  node_id: 'node-live-2222',
  name: 'live-alpha',
  kind: 'aicli',
  stale: false,
  capabilities: ['runner', 'status', 'sessions', 'window-actions'],
  routes: ['/web', '/web/api'],
  last_heartbeat: now,
  sessions: [{
    session_id: 'sess-live-1',
    title: 'live-session',
    state: 'idle',
    busy: false,
    updated_at: now,
    last_message: 'hello',
  }],
};
const staleNode = {
  node_id: 'node-stale-1111',
  name: 'stale-alpha',
  kind: 'aicli',
  stale: true,
  capabilities: ['runner', 'status', 'sessions'],
  routes: ['/web'],
  last_heartbeat: '2026-10-20T09:00:00Z',
  sessions: [{ session_id: 'sess-stale-1', title: 'old-session', state: 'idle', busy: false, updated_at: '2026-10-20T09:00:00Z' }],
};
const staleNode2 = {
  node_id: 'node-stale-3333',
  name: 'stale-beta',
  kind: 'aicli',
  stale: true,
  capabilities: ['runner', 'status', 'sessions'],
  routes: ['/web'],
  last_heartbeat: '2026-10-19T08:00:00Z',
  sessions: [],
};
const nodes = [liveNode, staleNode, staleNode2];
const devices = [
  { id: 'dev-active-0001', name: 'laptop-alpha', prefix: 'pfx-active', revoked: false, created_at: '2026-10-01T08:00:00Z', last_used: '2026-10-20T09:30:00Z' },
  { id: 'dev-revoked-0002', name: 'old-laptop', prefix: 'pfx-revoked', revoked: true, created_at: '2026-09-01T08:00:00Z', last_used: '0001-01-01T00:00:00Z' },
];
const mounts = [
  {
    slug: 'stale-mcp', name: 'stale mcp', stale: true, disabled: false,
    node_id: staleNode.node_id, public_url: 'https://console.test/m/stale-mcp/mcp',
    tokens: 1, local_path: '/srv/stale', last_seen: '2026-10-20T09:00:00Z', labels: {},
  },
  {
    slug: 'live-mcp', name: 'live mcp', stale: false, disabled: false,
    node_id: liveNode.node_id, public_url: 'https://console.test/m/live-mcp/mcp',
    tokens: 2, local_path: '/srv/live', last_seen: now, labels: {},
  },
];

(async () => {
  const dom = new JSDOM(html, {
    url: 'https://console.test/#tokens',
    runScripts: 'outside-only',
    pretendToBeVisual: true,
  });
  const { window } = dom;
  const doc = window.document;
  const requests = [];
  let nativeConfirms = 0;
  window.confirm = () => { nativeConfirms++; return false; };

  window.fetch = async (input, init) => {
    const url = typeof input === 'string' ? input : (input && input.url) || String(input);
    const method = ((init && init.method) || 'GET').toUpperCase();
    requests.push(method + ' ' + url);
    if (method === 'DELETE' && url.includes('/api/devices/')) { return response({ ok: true }); }
    if (method === 'DELETE' && url.includes('/api/nodes?stale=1')) { return response({ ok: true }); }
    if (method === 'DELETE' && /\/api\/nodes\/[^/?]+$/.test(url)) { return response({ ok: true }); }
    if (method === 'DELETE' && /\/api\/mounts\/[^/?]+$/.test(url)) { return response({ ok: true }); }
    if (url.includes('/window?action=close')) { return response({ status: 'queued' }); }
    if (url.includes('/api/nodes')) { return response({ nodes }); }
    if (url.includes('/api/devices')) { return response({ devices }); }
    if (/\/api\/mounts(\?|$)/.test(url)) { return response({ mounts }); }
    if (url.includes('/api/mounts/logs')) { return response({ entries: [] }); }
    if (url.includes('/api/connectors')) { return response({ items: [] }); }
    if (url.includes('/api/push/config')) { return response({ enabled: false }); }
    if (url.includes('/api/notify/policy')) {
      return response({ policy: { version: 1, global: {}, sessions: {}, channels: {} }, meta: {} });
    }
    if (url.includes('/api/presence')) { return response({ ok: true }); }
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
  window.open = () => null;

  const modal = doc.getElementById('confirm-modal');
  const title = doc.getElementById('confirm-title');
  const message = doc.getElementById('confirm-message');
  const cancel = doc.getElementById('confirm-cancel');
  const confirm = doc.getElementById('confirm-confirm');

  async function goto(hash) {
    window.location.hash = hash;
    await sleep(120);
  }
  function openMenu(card) {
    const more = card.querySelector('[data-more]');
    more.click();
    return { more, menu: card.querySelector('.s-menu') };
  }
  async function cancelDialog() {
    cancel.click();
    await sleep(10);
  }

  window.eval(appjs);
  await sleep(60);
  doc.dispatchEvent(new window.Event('DOMContentLoaded', { bubbles: true }));
  await sleep(160);

  // ── confirm dialog structural accessibility ──────────────────────
  const dialog = modal.querySelector('[role="dialog"]');
  check('确认框是模态对话框', !!dialog && dialog.getAttribute('aria-modal') === 'true');
  check('确认框有标题与后果说明关联',
    dialog.getAttribute('aria-labelledby') === 'confirm-title' &&
    dialog.getAttribute('aria-describedby') === 'confirm-message');
  check('确认框默认收起', modal.hidden === true);
  check('确认按钮使用描边危险样式', confirm.classList.contains('danger-outline'));
  check('danger-outline 使用现有设计变量',
    css.includes('.btn.danger-outline') && css.includes('var(--danger-ink)') && !css.includes('--color-danger'));
  check('确认文案保留换行并限高', css.includes('#confirm-message') &&
    /#confirm-message[^}]*white-space:\s*pre-line/s.test(css) &&
    /#confirm-message[^}]*max-height/s.test(css));

  // ── device token card: destructive action behind ⋯ ──────────────
  const deviceCards = () => [...doc.querySelectorAll('#device-list .device-card')];
  check('令牌卡片不再常驻实心红按钮',
    deviceCards().every((card) => !card.querySelector('.btn.danger')), String(deviceCards().length));
  const activeCard = deviceCards().find((card) => card.textContent.includes('laptop-alpha'));
  check('令牌卡片有 ⋯ 菜单入口',
    !!activeCard && !!activeCard.querySelector('[data-more]') &&
    activeCard.querySelector('[data-more]').getAttribute('aria-label') === '更多操作');

  let opened = openMenu(activeCard);
  await sleep(10);
  check('令牌菜单打开并标记展开',
    opened.menu.hidden === false && opened.more.getAttribute('aria-expanded') === 'true');
  check('令牌菜单使用视口定位', !!opened.menu.style.left && !!opened.menu.style.top);
  check('令牌菜单打开后焦点进入首项', opened.menu.contains(doc.activeElement));
  const deviceAction = opened.menu.querySelector('[data-action]');
  check('有效令牌菜单提供撤销入口', deviceAction.textContent === '撤销令牌');
  deviceAction.click();
  await sleep(10);
  check('撤销令牌弹出站内确认框', modal.hidden === false);
  check('确认框标题说明操作', title.textContent === '撤销令牌？', title.textContent);
  check('确认文案包含对象与后果',
    message.textContent.includes('laptop-alpha') && message.textContent.includes('无法再注册'), message.textContent);
  check('确认框默认聚焦取消', doc.activeElement === cancel);
  check('取消前尚未发出删除请求',
    !requests.some((line) => line.startsWith('DELETE /api/devices/')), requests.join(' | '));
  await cancelDialog();
  check('取消后确认框收起', modal.hidden === true);
  check('取消后焦点归还 ⋯', doc.activeElement === opened.more);
  check('取消不会执行删除', !requests.some((line) => line.startsWith('DELETE /api/devices/')));

  opened = openMenu(activeCard);
  await sleep(5);
  opened.menu.querySelector('[data-action]').click();
  await sleep(5);
  doc.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  await sleep(10);
  check('Escape 关闭确认框且不执行操作',
    modal.hidden === true && !requests.some((line) => line.startsWith('DELETE /api/devices/')));
  check('Escape 关闭后焦点归还 ⋯', doc.activeElement === opened.more);

  opened = openMenu(activeCard);
  await sleep(5);
  opened.menu.querySelector('[data-action]').click();
  await sleep(5);
  confirm.click();
  await sleep(100);
  check('确认后关闭确认框', modal.hidden === true);
  check('确认后调用撤销接口',
    requests.some((line) => line === 'DELETE /api/devices/dev-active-0001'), requests.join(' | '));
  check('撤销成功有 toast', [...doc.querySelectorAll('.toast')].some((t) => t.textContent.includes('令牌已撤销')));
  check('原生 confirm 未被调用', nativeConfirms === 0, String(nativeConfirms));

  const revokedCard = deviceCards().find((card) => card.textContent.includes('old-laptop'));
  check('已撤销令牌不再显示红色按钮', !revokedCard.querySelector('.btn.danger'));
  opened = openMenu(revokedCard);
  await sleep(5);
  const revokedAction = opened.menu.querySelector('[data-action]');
  check('已撤销令牌菜单提供删除记录', revokedAction.textContent === '删除记录');
  revokedAction.click();
  await sleep(5);
  check('删除记录要求二次确认', modal.hidden === false && message.textContent.includes('不可恢复'));
  confirm.click();
  await sleep(100);
  check('删除记录携带 purge 参数',
    requests.some((line) => line === 'DELETE /api/devices/dev-revoked-0002?purge=1'), requests.join(' | '));

  // ── offline node snapshot: remove + bulk clear ───────────────────
  await goto('#nodes');
  const offlineCards = () => [...doc.querySelectorAll('#node-offline-list .card')];
  check('离线快照卡片不再常驻实心红按钮',
    offlineCards().length === 2 && offlineCards().every((card) => !card.querySelector('.btn.danger')));
  const staleCard = offlineCards().find((card) => card.textContent.includes('stale-alpha'));
  opened = openMenu(staleCard);
  await sleep(5);
  const removeItem = opened.menu.querySelector('[data-remove]');
  check('离线快照提供移除菜单项', removeItem && removeItem.textContent === '移除该节点');
  removeItem.click();
  await sleep(5);
  check('移除离线快照要求确认', modal.hidden === false && title.textContent.includes('移除离线快照节点'));
  check('移除文案包含节点名', message.textContent.includes('stale-alpha'), message.textContent);
  await cancelDialog();
  check('移除取消后焦点归还 ⋯', doc.activeElement === opened.more);

  const clearStale = doc.getElementById('clear-stale');
  check('清除全部改为描边按钮', clearStale.classList.contains('ghost') && !clearStale.classList.contains('danger'));
  check('清除全部显示受影响数量', clearStale.textContent === '清除全部（2）', clearStale.textContent);
  check('清除全部有可访问名称', (clearStale.getAttribute('aria-label') || '').includes('2 个'));
  clearStale.click();
  await sleep(10);
  check('清除全部要求确认', modal.hidden === false && title.textContent === '清除全部离线快照？');
  check('清除全部列出影响范围',
    message.textContent.includes('stale-alpha') && message.textContent.includes('stale-beta') &&
    message.textContent.includes('2 个离线节点'), message.textContent);
  confirm.click();
  await sleep(120);
  check('确认后调用批量清理接口',
    requests.some((line) => line === 'DELETE /api/nodes?stale=1'), requests.join(' | '));

  // ── MCP mount: delete only for offline entries ───────────────────
  await goto('#mcp');
  const mountCards = () => [...doc.querySelectorAll('#mcp-list .card')];
  const staleMount = mountCards().find((card) => card.textContent.includes('stale mcp'));
  const liveMount = mountCards().find((card) => card.textContent.includes('live mcp'));
  check('离线 MCP 卡片提供 ⋯', !!staleMount && !!staleMount.querySelector('[data-more]'));
  check('在线 MCP 卡片没有删除菜单', !!liveMount && !liveMount.querySelector('[data-more]'));
  check('MCP 卡片不再常驻实心红按钮',
    mountCards().every((card) => !card.querySelector('.btn.danger')));
  opened = openMenu(staleMount);
  await sleep(5);
  const deleteMount = opened.menu.querySelector('[data-delete]');
  check('离线 MCP 菜单提供删除', deleteMount && deleteMount.textContent === '删除离线挂载');
  deleteMount.click();
  await sleep(5);
  check('删除 MCP 挂载要求确认',
    modal.hidden === false && message.textContent.includes('持有该 URL 的客户端会得到 404'));
  confirm.click();
  await sleep(120);
  check('确认后删除 MCP 挂载',
    requests.some((line) => line === 'DELETE /api/mounts/stale-mcp'), requests.join(' | '));

  // ── session window close: menu + in-page confirm ─────────────────
  await goto('#sessions');
  const sessionCard = [...doc.querySelectorAll('.session-card')]
    .find((card) => card.textContent.includes('live-session'));
  check('会话卡片窗口操作仍在 ⋯ 内', !!sessionCard);
  opened = openMenu(sessionCard);
  await sleep(5);
  const closeWindow = opened.menu.querySelector('[data-window-close]');
  check('关窗口菜单项可见', closeWindow && closeWindow.hidden === false);
  closeWindow.click();
  await sleep(5);
  check('关窗口弹出站内确认框', modal.hidden === false && title.textContent === '关闭窗口进程？');
  check('关窗口文案说明后果', message.textContent.includes('/exit'));
  await cancelDialog();
  check('关窗口取消后焦点归还 ⋯', doc.activeElement === opened.more);
  opened = openMenu(sessionCard);
  await sleep(5);
  opened.menu.querySelector('[data-window-close]').click();
  await sleep(5);
  confirm.click();
  await sleep(120);
  check('确认后排队关窗口',
    requests.some((line) => line.includes('/window?action=close') && line.startsWith('POST ')), requests.join(' | '));

  check('全部危险路径未回退到 window.confirm', nativeConfirms === 0, String(nativeConfirms));
  check('页面无横向滚动样式保持不变', css.includes('overflow-x: hidden'));

  window.close();
  console.log(`\ntotal=${failures ? 'failed' : 'ok'} failed=${failures}`);
  process.exit(failures ? 1 : 0);
})();