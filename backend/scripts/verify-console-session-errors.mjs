// Verify the session failure paths that are not covered by the chat transcript test:
// no-Web capability state, user-facing load errors with diagnostics, and desktop
// auto-selection without marking the session read.
//
// Usage (repo root):
//   node backend/scripts/verify-console-session-errors.mjs
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
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

let failures = 0;
function check(name, ok, extra) {
  if (!ok) { failures++; }
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? '  :: ' + extra : ''}`);
}
function response(body, status = 200) {
  return { ok: status >= 200 && status < 300, status, json: async () => body, text: async () => JSON.stringify(body) };
}

function makeNode(overrides) {
  return Object.assign({
    node_id: 'node-test',
    name: 'test-node',
    kind: 'aicli',
    stale: false,
    capabilities: ['runner', 'status', 'sessions', 'screen', 'events', 'input'],
    routes: ['/web', '/web/api'],
    last_heartbeat: new Date().toISOString(),
    sessions: [],
  }, overrides || {});
}
function makeSession(overrides) {
  return Object.assign({
    session_id: 'session-test',
    title: '测试会话',
    state: 'idle',
    busy: false,
    updated_at: new Date().toISOString(),
    preview: '测试预览',
  }, overrides || {});
}
function screenOK(text = 'RECOVERED') {
  return {
    available: true,
    messages: [{ role: 'assistant', content: text }],
    message_window: { total: 1, start: 0, end: 1, limit: 10, has_more: false },
  };
}

async function boot(node, opts = {}) {
  const dom = new JSDOM(html, {
    url: opts.url || 'https://console.test/',
    runScripts: 'outside-only',
    pretendToBeVisual: true,
  });
  const { window } = dom;
  const doc = window.document;
  let screenCalls = 0;
  window.fetch = async (input) => {
    const url = typeof input === 'string' ? input : (input && input.url) || String(input);
    if (url.includes('/api/nodes')) { return response({ nodes: [node] }); }
    if (url.includes('/api/devices')) { return response({ devices: [] }); }
    if (url.includes('/api/connectors')) { return response({ items: [] }); }
    if (url.includes('/api/push/config')) { return response({ enabled: false }); }
    if (url.includes('/api/presence')) { return response({ ok: true }); }
    if (url.includes('/web/api/screen')) {
      screenCalls++;
      return opts.screen ? opts.screen(screenCalls) : response(screenOK());
    }
    if (url.includes('/web/api/statusbar')) {
      return response({ available: true, full_line: 'test · 1%', segments: [] });
    }
    if (url.includes('/web/api/input')) { return response({ status: 'queued' }); }
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
  let copied = '';
  Object.defineProperty(window.navigator, 'clipboard', {
    configurable: true,
    value: { writeText: async (text) => { copied = String(text); } },
  });
  window.eval(appjs);
  await sleep(40);
  doc.dispatchEvent(new window.Event('DOMContentLoaded', { bubbles: true }));
  await sleep(100);
  return { dom, window, doc, screenCalls: () => screenCalls, copied: () => copied };
}

(async () => {
  {
    const session = makeSession({ session_id: 's-no-web', title: '无网页会话' });
    const node = makeNode({
      node_id: 'node-no-web',
      name: 'no-web-node',
      capabilities: ['runner', 'status', 'sessions'],
      routes: [],
      sessions: [session],
    });
    const { doc, window } = await boot(node);
    const card = doc.querySelector('.session-card[data-draft-key="node-no-web|s-no-web"]');
    check('无 Web 会话卡片渲染', !!card);
    check('卡片提前显示网页未启用', card.querySelector('.s-access').textContent === '网页未启用' &&
      !card.querySelector('.s-access').hidden);
    check('无 Web 卡片的主打开按钮隐藏', card.querySelector('[data-open]').hidden === true);
    card.click();
    await sleep(40);
    check('无 Web 卡片点击进入详情而非报错', doc.getElementById('session-modal').hidden === false);
    check('详情展示访问方式', doc.getElementById('sd-access').textContent === '网页会话未启用',
      doc.getElementById('sd-access').textContent);
    check('详情隐藏不可用的打开按钮', doc.getElementById('session-modal-open').hidden === true);
    check('详情提供技术诊断', doc.getElementById('sd-diagnostics').textContent.includes('node-no-web'));
    check('详情提供复制诊断按钮', !!doc.getElementById('sd-copy-diagnostics'));
    window.close();
  }

  {
    const session = makeSession({ session_id: 's-load-fail', title: '加载失败会话' });
    const node = makeNode({ node_id: 'node-load-fail', name: 'load-fail-node', sessions: [session] });
    let failScreen = true;
    const { doc, window, screenCalls, copied } = await boot(node, {
      screen: () => failScreen ? response({ reason: 'backend unavailable' }, 500) : response(screenOK('RECOVERED')),
    });
    const open = doc.querySelector('[data-open]');
    open.click();
    await sleep(80);
    const notice = doc.getElementById('chat-notice');
    check('会话加载失败进入可恢复态', notice.hidden === false && screenCalls() > 0);
    check('错误提示不含裸 HTTP', !notice.textContent.includes('HTTP 500'), notice.textContent);
    check('错误提示不含 --web', !notice.textContent.includes('--web'), notice.textContent);
    const labels = [...notice.querySelectorAll('button')].map((b) => b.textContent);
    check('错误态提供重试与复制诊断', labels.includes('重试') && labels.includes('复制诊断'), labels.join(','));
    const diagBtn = [...notice.querySelectorAll('button')].find((b) => b.textContent === '复制诊断');
    diagBtn.click();
    await sleep(20);
    check('诊断包含 HTTP 细节', copied().includes('HTTP 500') && copied().includes('aicli-gateway 会话诊断'));
    failScreen = false;
    const retryBtn = [...notice.querySelectorAll('button')].find((b) => b.textContent === '重试');
    retryBtn.click();
    await sleep(120);
    check('重试成功后恢复消息', doc.getElementById('chat-notice').hidden &&
      doc.getElementById('chat-list').textContent.includes('RECOVERED'),
      doc.getElementById('chat-list').textContent.trim().slice(0, 80));
    window.close();
  }

  {
    const session = makeSession({ session_id: 's-auto', title: '自动选中的会话', attention: 'approval', state: 'review', busy: true });
    const node = makeNode({ node_id: 'node-auto', name: 'auto-node', sessions: [session] });
    const { doc, window } = await boot(node, { url: 'https://console.test/#sessions' });
    check('桌面进入会话页自动选中首条', doc.getElementById('chat-modal').hidden === false);
    check('自动选中不标记已读', doc.getElementById('nav-badge-sessions').textContent === '1' &&
      doc.getElementById('nav-badge-sessions').hidden === false);
    check('自动选中卡片高亮', doc.querySelector('.session-card').classList.contains('is-active'));
    check('自动选中后右栏占位隐藏', doc.getElementById('im-chat-empty').hidden === true);
    window.close();
  }

  console.log(`\ntotal=${failures ? 'failed' : 'ok'} failed=${failures}`);
  process.exit(failures ? 1 : 0);
})();
