// Verify dashboard "pending" semantics with a real console DOM.
//
// Usage (repo root):
//   node backend/scripts/verify-console-attention.mjs
// Exit 0 = all checks pass.
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

function response(obj) {
  return { ok: true, status: 200, json: async () => obj };
}

function makeSession(id, title, attention) {
  return {
    session_id: id,
    title,
    state: attention === 'question' ? 'waiting' : attention === 'approval' ? 'review' : 'running',
    busy: attention === 'approval' || attention === 'question',
    attention,
    updated_at: new Date().toISOString(),
    preview: title + ' preview',
  };
}

const ALL_ATTENTION = [
  makeSession('s-approval', '审批会话', 'approval'),
  makeSession('s-question', '回答会话', 'question'),
  makeSession('s-error', '出错会话', 'error'),
  makeSession('s-completed', '完成会话', 'completed'),
  makeSession('s-waiting', '等待会话', 'waiting'),
];

function makeNode(sessions) {
  return {
    node_id: 'node-attention-test',
    name: 'attention-node',
    kind: 'aicli',
    stale: false,
    capabilities: ['runner', 'status', 'sessions', 'screen', 'events', 'input'],
    routes: ['/web', '/web/api'],
    last_heartbeat: new Date().toISOString(),
    sessions,
  };
}

async function boot(sessions, readKeys = []) {
  const node = makeNode(sessions);
  const dom = new JSDOM(html, {
    url: 'https://console.test/',
    runScripts: 'outside-only',
    pretendToBeVisual: true,
  });
  const { window } = dom;
  const doc = window.document;
  window.fetch = async (input) => {
    const url = typeof input === 'string' ? input : (input && input.url) || String(input);
    if (url.includes('/api/nodes')) { return response({ nodes: [node] }); }
    if (url.includes('/api/devices')) { return response({ devices: [] }); }
    if (url.includes('/api/connectors')) { return response({ items: [] }); }
    if (url.includes('/api/push/config')) { return response({ enabled: false }); }
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
  if (!window.open) { window.open = () => null; }
  if (readKeys.length) {
    const raw = {};
    for (const key of readKeys) { raw[key] = Date.now(); }
    window.localStorage.setItem('aicli-gw-read-attention', JSON.stringify(raw));
  }
  window.eval(appjs);
  await sleep(40);
  doc.dispatchEvent(new window.Event('DOMContentLoaded', { bubbles: true }));
  await sleep(80);
  return { dom, window, doc };
}

function text(doc, id) {
  const node = doc.getElementById(id);
  return node ? node.textContent.trim() : '';
}

(async () => {
  {
    const { doc, window } = await boot(ALL_ATTENTION);
    check('待处理只统计 approval/question/error', text(doc, 'stat-attention') === '3', text(doc, 'stat-attention'));
    check('导航徽标同源为 3', text(doc, 'nav-badge-sessions') === '3', text(doc, 'nav-badge-sessions'));
    check('导航徽标可访问语义', doc.querySelector('.nav-item[data-view="sessions"]').getAttribute('aria-label').includes('3 项待处理'));
    check('收件箱只含 3 条 actionable', doc.querySelectorAll('#inbox-list .card').length === 3,
      String(doc.querySelectorAll('#inbox-list .card').length));
    check('收件箱不含已完成/等待中', !doc.querySelector('#inbox-list').textContent.includes('完成会话') &&
      !doc.querySelector('#inbox-list').textContent.includes('等待会话'));
    check('待处理构成只显示 3 类', !!doc.getElementById('attn-approval') &&
      !!doc.getElementById('attn-question') && !!doc.getElementById('attn-error') &&
      !doc.getElementById('attn-completed'));
    check('构成分项各为 1', text(doc, 'attn-approval') === '1' &&
      text(doc, 'attn-question') === '1' && text(doc, 'attn-error') === '1');
    check('有 actionable 时统计卡可点击且有强调态', (() => {
      const card = doc.getElementById('stat-attention-card');
      return card.getAttribute('href') === '#sessions?attention=1' &&
        card.classList.contains('has-attention') && !card.classList.contains('is-empty');
    })());
    doc.querySelector('.seg-btn[data-sfilter="attention"]').click();
    await sleep(25);
    check('需处理筛选只保留 actionable', doc.querySelectorAll('.session-card').length === 3,
      String(doc.querySelectorAll('.session-card').length));
    window.close();
  }

  {
    const { doc, window } = await boot(ALL_ATTENTION, ['node-attention-test|s-approval|approval']);
    check('已读 actionable 从待处理总数移除', text(doc, 'stat-attention') === '2', text(doc, 'stat-attention'));
    check('已读 actionable 从构成移除', text(doc, 'attn-approval') === '0' &&
      text(doc, 'attn-question') === '1' && text(doc, 'attn-error') === '1');
    check('已读后构成之和仍等于待处理', Number(text(doc, 'attn-approval')) +
      Number(text(doc, 'attn-question')) + Number(text(doc, 'attn-error')) === 2);
    check('已读后收件箱只剩 2 条', doc.querySelectorAll('#inbox-list .card').length === 2,
      String(doc.querySelectorAll('#inbox-list .card').length));
    doc.querySelector('.seg-btn[data-sfilter="attention"]').click();
    await sleep(25);
    check('已读后需处理筛选也只剩 2 条', doc.querySelectorAll('.session-card').length === 2,
      String(doc.querySelectorAll('.session-card').length));
    window.close();
  }

  {
    const { doc, window } = await boot([ALL_ATTENTION[3], ALL_ATTENTION[4]]);
    const card = doc.getElementById('stat-attention-card');
    check('只有 completed/waiting 时待处理为 0', text(doc, 'stat-attention') === '0', text(doc, 'stat-attention'));
    check('仅非待办时徽标隐藏', doc.getElementById('nav-badge-sessions').hidden === true);
    check('仅非待办时统计卡无链接且为空态', !card.hasAttribute('href') &&
      card.classList.contains('is-empty') && !card.classList.contains('has-attention') &&
      card.getAttribute('aria-label') === '暂无待处理');
    check('仅非待办时构成面板隐藏', doc.getElementById('attention-breakdown').hidden === true);
    check('仅非待办时收件箱隐藏', doc.getElementById('inbox-panel').hidden === true);
    check('仅非待办时 inbox 计数为空', text(doc, 'inbox-count') === '');
    doc.querySelector('.seg-btn[data-sfilter="attention"]').click();
    await sleep(25);
    check('需处理筛选在空场景下为 0', doc.querySelectorAll('.session-card').length === 0,
      String(doc.querySelectorAll('.session-card').length));
    window.close();
  }

  for (const [label, sessions] of [
    ['只有 completed', [ALL_ATTENTION[3]]],
    ['只有 waiting', [ALL_ATTENTION[4]]],
  ]) {
    const { doc, window } = await boot(sessions);
    check(label + ' 时待处理为 0', text(doc, 'stat-attention') === '0', text(doc, 'stat-attention'));
    check(label + ' 时徽标隐藏', doc.getElementById('nav-badge-sessions').hidden === true);
    window.close();
  }

  console.log(`\ntotal=${failures ? 'failed' : 'ok'} failed=${failures}`);
  process.exit(failures ? 1 : 0);
})();
