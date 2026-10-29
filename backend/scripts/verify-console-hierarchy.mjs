// UX-07 regression: one page title, one manual refresh entry, bounded event
// strip, descriptive panel headings, and a max content width.
//
// Usage (repo root):
//   node backend/scripts/verify-console-hierarchy.mjs
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
const sessions = Array.from({ length: 6 }, (_, i) => ({
  session_id: 'sess-' + i,
  title: 'session-' + i,
  state: 'running',
  busy: true,
  updated_at: now,
}));
const node = {
  node_id: 'node-hierarchy-1',
  name: 'hierarchy-node',
  kind: 'aicli',
  stale: false,
  capabilities: ['runner', 'status', 'sessions'],
  routes: ['/web', '/web/api'],
  last_heartbeat: now,
  sessions,
};
const mounts = [{
  slug: 'demo', name: 'demo mount', stale: false, disabled: false,
  node_id: node.node_id, public_url: 'https://console.test/m/demo/mcp',
  tokens: 1, labels: {},
}];

(async () => {
  const calls = { nodes: 0, devices: 0, mounts: 0, connectors: 0, policy: 0 };
  const sourceRefs = [];
  const dom = new JSDOM(html, {
    url: 'https://console.test/#tokens',
    runScripts: 'outside-only',
    pretendToBeVisual: true,
  });
  const { window } = dom;
  const doc = window.document;

  window.fetch = async (input) => {
    const url = typeof input === 'string' ? input : (input && input.url) || String(input);
    if (url.includes('/api/nodes')) { calls.nodes++; return response({ nodes: [node] }); }
    if (url.includes('/api/devices')) { calls.devices++; return response({ devices: [] }); }
    if (url.includes('/api/mounts/logs')) { return response({ entries: [] }); }
    if (/\/api\/mounts(\?|$)/.test(url)) { calls.mounts++; return response({ mounts }); }
    if (url.includes('/api/connectors')) { calls.connectors++; return response({ items: [] }); }
    if (url.includes('/api/push/config')) { return response({ enabled: false }); }
    if (url.includes('/api/notify/policy')) {
      calls.policy++;
      return response({ policy: { version: 1, global: {}, sessions: {}, channels: {} }, meta: {} });
    }
    if (url.includes('/api/presence')) { return response({ ok: true }); }
    return response({});
  };
  class FakeEventSource {
    constructor(url) {
      this.url = url;
      this.listeners = {};
      sourceRefs.push(this);
    }
    addEventListener(name, fn) { (this.listeners[name] ||= []).push(fn); }
    removeEventListener() {}
    close() {}
    emit(name, payload) {
      (this.listeners[name] || []).forEach((fn) => fn({ data: JSON.stringify(payload) }));
    }
  }
  window.EventSource = FakeEventSource;
  if (!window.matchMedia) {
    window.matchMedia = () => ({ matches: false, addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {} });
  }
  if (!window.scrollTo) { window.scrollTo = () => {}; }
  if (!window.Element.prototype.scrollIntoView) { window.Element.prototype.scrollIntoView = () => {}; }
  window.open = () => null;

  window.eval(appjs);
  await sleep(60);
  doc.dispatchEvent(new window.Event('DOMContentLoaded', { bubbles: true }));
  await sleep(220);

  async function goto(hash) {
    window.location.hash = hash;
    await sleep(120);
  }
  const strip = () => doc.getElementById('event-strip');
  const chips = () => [...strip().querySelectorAll('.event-chip')];
  const moreBtn = () => strip().querySelector('.event-strip-more');
  const clearBtn = () => strip().querySelector('.event-strip-clear');
  const source = () => sourceRefs[sourceRefs.length - 1];

  // ── heading hierarchy ────────────────────────────────────────────
  const title = doc.getElementById('view-title');
  check('页面标题使用唯一 H1', title && title.tagName === 'H1', title && title.tagName);
  const visibleH1 = [...doc.querySelectorAll('h1')].filter((el) => !el.closest('[hidden]'));
  check('当前页面只有一个可见 H1', visibleH1.length === 1,
    visibleH1.map((el) => el.textContent).join(' | '));
  check('会话列表标题不再与页面同名', doc.querySelector('.im-list-title').textContent === '全部会话');
  check('MCP 面板标题是描述性的', doc.querySelector('[data-view="mcp"] h2').textContent === '已注册挂载');
  check('日志面板标题是描述性的', doc.querySelector('[data-view="logs"] h2').textContent === '请求与审计');
  check('连接器面板标题是描述性的', doc.querySelector('[data-view="connectors"] h2').textContent === '已注册连接器');

  // ── refresh entry ────────────────────────────────────────────────
  for (const id of ['mcp-refresh', 'logs-refresh', 'connectors-refresh', 'refresh-nodes']) {
    check('页面内重复刷新入口已移除：' + id, !doc.getElementById(id));
  }
  check('顶栏仍保留唯一刷新按钮', !!doc.getElementById('refresh'));

  const before = { ...calls };
  doc.getElementById('refresh').click();
  await sleep(120);
  check('令牌页刷新只请求设备',
    calls.devices === before.devices + 1 && calls.nodes === before.nodes,
    JSON.stringify({ before, after: calls }));

  await goto('#mcp');
  const m0 = calls.mounts;
  doc.getElementById('refresh').click();
  await sleep(120);
  check('MCP 页刷新只请求挂载', calls.mounts === m0 + 1, String(m0) + ' -> ' + String(calls.mounts));

  await goto('#dashboard');
  const n0 = calls.nodes;
  doc.getElementById('refresh').click();
  await sleep(120);
  check('仪表盘刷新请求节点', calls.nodes === n0 + 1, String(n0) + ' -> ' + String(calls.nodes));

  // ── content width ────────────────────────────────────────────────
  check('内容区设置最大宽度', /\.views\s*\{[^}]*max-width:\s*1440px/s.test(css));
  check('内容区超宽屏居中', /\.views\s*\{[^}]*margin-inline:\s*auto/s.test(css));

  // ── connector wording ────────────────────────────────────────────
  const connectorView = doc.querySelector('section.view[data-view="connectors"]');
  const hint = connectorView.querySelector('.connectors-hint');
  const advanced = connectorView.querySelector('details.connector-advanced');
  check('连接器首屏不暴露 Backend= 实现术语', !hint.textContent.includes('Backend='), hint.textContent);
  check('连接器首屏不暴露 Schema 术语', !hint.textContent.includes('Schema'), hint.textContent);
  check('连接器技术字段折叠在高级信息中', !!advanced && advanced.open === false &&
    advanced.textContent.includes('gateway_node') && advanced.textContent.includes('Schema'));

  // ── event strip ──────────────────────────────────────────────────
  check('事件条默认单行且限高', /\.event-strip\s*\{[^}]*flex-wrap:\s*nowrap/s.test(css) &&
    /\.event-strip\s*\{[^}]*max-height:\s*44px/s.test(css));
  check('事件条展开后允许换行', /\.event-strip\.expanded\s*\{[^}]*flex-wrap:\s*wrap/s.test(css));

  for (let i = 0; i < 5; i++) {
    source().emit('session-event', {
      node_id: node.node_id,
      session_id: 'sess-' + i,
      state: 'running',
      ts: new Date(Date.now() - i * 1000).toISOString(),
    });
  }
  await sleep(30);
  check('事件条最多显示 3 个 chip', chips().length === 3, String(chips().length));
  check('事件条提供剩余数量入口', moreBtn() && moreBtn().textContent === '还有 2 条',
    moreBtn() && moreBtn().textContent);
  check('事件条保留隐藏入口', clearBtn() && clearBtn().textContent === '隐藏动态',
    clearBtn() && clearBtn().textContent);
  check('事件条默认未展开', !strip().classList.contains('expanded'));

  moreBtn().click();
  await sleep(10);
  check('展开后显示全部事件', chips().length === 5 && strip().classList.contains('expanded'),
    String(chips().length));
  check('展开按钮可再次收起', moreBtn().textContent === '收起', moreBtn().textContent);

  source().emit('session-event', {
    node_id: node.node_id,
    session_id: 'sess-0',
    state: 'completed',
    ts: new Date().toISOString(),
  });
  await sleep(20);
  check('已完成事件不进入事件条',
    !chips().some((chip) => chip.textContent.includes('已完成')) &&
    !chips().some((chip) => chip.textContent.includes('session-0')),
    chips().map((chip) => chip.textContent).join(' | '));

  clearBtn().click();
  await sleep(10);
  check('隐藏动态后事件条收起', strip().hidden === true && chips().length === 0);

  source().emit('session-event', {
    node_id: node.node_id,
    session_id: 'sess-5',
    state: 'completed',
    ts: new Date().toISOString(),
  });
  await sleep(20);
  check('仅收到已完成事件时事件条保持隐藏', strip().hidden === true);

  window.close();
  console.log(`\ntotal=${failures ? 'failed' : 'ok'} failed=${failures}`);
  process.exit(failures ? 1 : 0);
})();