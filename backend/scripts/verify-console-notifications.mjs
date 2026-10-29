// Verify the notification-policy page as a user sees it: result-oriented labels,
// quiet-hour validation, unavailable channels, dirty/save gating, navigation
// protection, and session-override search/grouping/filtering.
//
// Usage (repo root):
//   node backend/scripts/verify-console-notifications.mjs
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
  return { ok: status >= 200 && status < 300, status, json: async () => body, text: async () => JSON.stringify(body) };
}
const KINDS = [
  { kind: 'approval', label: '待审批', priority: 'high' },
  { kind: 'question', label: '待回答', priority: 'high' },
  { kind: 'error', label: '出错', priority: 'high' },
  { kind: 'completed', label: '已完成', priority: 'normal' },
  { kind: 'waiting', label: '等待中', priority: 'normal' },
];
const CHANNELS = [
  { name: 'webpush', label: 'Web Push', available: true },
  { name: 'ntfy', label: 'ntfy', available: true },
  { name: 'apppush', label: 'AppPush', available: false },
];
function makeNode(id, name, sessions) {
  return {
    node_id: id,
    name,
    kind: 'aicli',
    stale: false,
    capabilities: ['runner', 'status', 'sessions'],
    routes: ['/web', '/web/api'],
    last_heartbeat: new Date().toISOString(),
    sessions,
  };
}
function makeSession(id, title, attention) {
  return {
    session_id: id,
    title,
    state: attention === 'approval' || attention === 'question' ? 'review' : 'idle',
    busy: false,
    attention,
    updated_at: new Date().toISOString(),
    preview: title + ' 的预览',
  };
}

function initialPolicy() {
  return {
    version: 1,
    global: {
      muted: false,
      min_priority: 'normal',
      allow_kinds: ['approval', 'question', 'error', 'completed', 'waiting'],
      quiet_hours: ['22:00-08:00'],
    },
    sessions: { 's-beta-custom': { muted: true } },
    channels: { ntfy: { muted: true } },
  };
}

async function boot() {
  const nodes = [
    makeNode('node-alpha', 'alpha-node', [
      makeSession('s-alpha-approval', '审批会话', 'approval'),
      makeSession('s-alpha-done', '完成会话', 'completed'),
    ]),
    makeNode('node-beta', 'beta-node', [
      makeSession('s-beta-custom', '自定义会话', 'question'),
      makeSession('s-beta-waiting', '等待会话', 'waiting'),
    ]),
  ];
  const dom = new JSDOM(html, {
    url: 'https://console.test/#notifications',
    runScripts: 'outside-only',
    pretendToBeVisual: true,
  });
  const { window } = dom;
  const doc = window.document;
  let policy = initialPolicy();
  let lastPut = null;
  let confirmResult = false;
  window.confirm = () => confirmResult;
  window.fetch = async (input, init) => {
    const url = typeof input === 'string' ? input : (input && input.url) || String(input);
    const method = (init && init.method) || 'GET';
    if (url.includes('/api/nodes')) { return response({ nodes }); }
    if (url.includes('/api/devices')) { return response({ devices: [] }); }
    if (url.includes('/api/connectors')) { return response({ items: [] }); }
    if (url.includes('/api/push/config')) { return response({ enabled: false }); }
    if (url.includes('/api/presence')) { return response({ ok: true }); }
    if (url.includes('/api/notify/policy') && method === 'PUT') {
      lastPut = JSON.parse(init.body);
      policy = lastPut;
      return response({
        policy,
        meta: {
          source: 'file',
          path: '/tmp/notify-policy.json',
          kinds: KINDS,
          channels: CHANNELS,
          defaults: { muted: false, min_priority: 'normal', allow_kinds: KINDS.map((k) => k.kind) },
        },
      });
    }
    if (url.includes('/api/notify/policy')) {
      return response({
        policy,
        meta: {
          source: 'file',
          path: '/tmp/notify-policy.json',
          kinds: KINDS,
          channels: CHANNELS,
          defaults: { muted: false, min_priority: 'normal', allow_kinds: KINDS.map((k) => k.kind) },
        },
      });
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
  if (!window.navigator.clipboard) {
    Object.defineProperty(window.navigator, 'clipboard', {
      configurable: true,
      value: { writeText: async () => {} },
    });
  }
  window.eval(appjs);
  await sleep(40);
  doc.dispatchEvent(new window.Event('DOMContentLoaded', { bubbles: true }));
  await sleep(140);
  return {
    dom, window, doc,
    lastPut: () => lastPut,
    setConfirm: (value) => { confirmResult = value; },
    policy: () => policy,
  };
}

function optionTexts(select) {
  return [...select.options].map((opt) => opt.textContent.trim());
}
function text(doc, id) {
  const node = doc.getElementById(id);
  return node ? node.textContent.trim() : '';
}
function activeView(doc, name) {
  const node = doc.querySelector(`.view[data-view="${name}"]`);
  return node && !node.hidden;
}

(async () => {
  const { window, doc, lastPut, setConfirm, policy } = await boot();
  const view = doc.querySelector('.view[data-view="notifications"]');
  check('通知设置页已加载', !view.hidden);
  check('初始状态没有未保存提示', text(doc, 'notify-dirty') === '所有修改已保存', text(doc, 'notify-dirty'));
  check('初始保存按钮禁用', doc.getElementById('notify-save').disabled === true);

  const priority = doc.getElementById('notify-min-priority');
  const priorityText = optionTexts(priority);
  check('通知范围不暴露内部枚举', priorityText.every((label) => !/\b(low|normal|high)\b/i.test(label)), priorityText.join(' / '));
  check('通知范围使用结果导向文案', priorityText.includes('全部通知') &&
    priorityText.some((label) => label.includes('重要通知')) &&
    priorityText.some((label) => label.includes('仅待处理')), priorityText.join(' / '));
  check('通知范围有可读解释', text(doc, 'notify-priority-hint').length > 8, text(doc, 'notify-priority-hint'));

  const kindText = doc.getElementById('notify-kinds').textContent;
  check('消息类型不显示内部优先级', !/（high）|（normal）|（low）/i.test(kindText), kindText.slice(0, 80));
  check('消息类型有逐项解释', kindText.includes('需要你允许或拒绝') && kindText.includes('等待你的答复'));

  const unavailable = [...doc.querySelectorAll('#notify-channels .notify-channel')]
    .find((row) => row.textContent.includes('AppPush'));
  check('未配置通道不呈现为已勾选', !!unavailable &&
    unavailable.querySelector('input').checked === false &&
    unavailable.querySelector('input').disabled === true);
  check('未配置通道带标签与说明', !!unavailable &&
    unavailable.textContent.includes('未配置') &&
    unavailable.textContent.includes('网关启动参数'), unavailable ? unavailable.textContent : '');

  check('通知通道是卡片式布局', css.includes('.notify-channel.is-unavailable') &&
    css.includes('grid-template-columns: repeat(auto-fit, minmax(240px, 1fr))'));
  check('保存条固定在视口底部', css.includes('.notify-savebar') &&
    css.includes('position: fixed') && css.includes('bottom: 16px'));

  const quietRows = () => [...doc.querySelectorAll('#notify-quiet-rows .notify-quiet-row')];
  check('已有免打扰时段拆成开始/结束输入', quietRows().length === 1 &&
    quietRows()[0].querySelector('[data-quiet-start]').value === '22:00' &&
    quietRows()[0].querySelector('[data-quiet-end]').value === '08:00' &&
    quietRows()[0].querySelector('[data-quiet-start]').type === 'time');

  const groupHeads = () => [...doc.querySelectorAll('.notify-session-group-head')].map((node) => node.textContent.trim());
  check('会话覆盖按节点分组', groupHeads().length === 2 && groupHeads().includes('alpha-node') && groupHeads().includes('beta-node'), groupHeads().join(','));
  check('已调整会话显示标记', [...doc.querySelectorAll('.notify-session-row')]
    .some((row) => row.textContent.includes('自定义会话') && row.classList.contains('is-overridden') && row.textContent.includes('已调整')));

  priority.value = 'high';
  priority.dispatchEvent(new window.Event('change', { bubbles: true }));
  check('修改后出现未保存提示', text(doc, 'notify-dirty') === '有未保存的修改', text(doc, 'notify-dirty'));
  check('修改后保存按钮可用', doc.getElementById('notify-save').disabled === false);
  check('范围解释随选择更新', text(doc, 'notify-priority-hint').includes('仅待审批'), text(doc, 'notify-priority-hint'));

  doc.getElementById('notify-save').click();
  await sleep(100);
  check('保存请求包含新范围', lastPut() && lastPut().global.min_priority === 'high', JSON.stringify(lastPut() && lastPut().global));
  check('保存成功后提示消失', text(doc, 'notify-dirty') === '所有修改已保存', text(doc, 'notify-dirty'));
  check('保存成功后按钮重新禁用', doc.getElementById('notify-save').disabled === true);

  const add = doc.getElementById('notify-quiet-add');
  add.click();
  check('新增空时段立即提示错误', text(doc, 'notify-quiet-error').includes('同时填写') &&
    doc.getElementById('notify-save').disabled === true);
  let rows = quietRows();
  const newStart = rows[1].querySelector('[data-quiet-start]');
  const newEnd = rows[1].querySelector('[data-quiet-end]');
  newStart.value = '02:00';
  newEnd.value = '03:00';
  newStart.dispatchEvent(new window.Event('input', { bubbles: true }));
  newEnd.dispatchEvent(new window.Event('input', { bubbles: true }));
  check('补齐时段后错误消失', doc.getElementById('notify-quiet-error').hidden === true);
  check('新增时段后保存可用', doc.getElementById('notify-save').disabled === false);

  const firstStart = quietRows()[0].querySelector('[data-quiet-start]');
  const firstEnd = quietRows()[0].querySelector('[data-quiet-end]');
  firstStart.value = '09:00';
  firstEnd.value = '09:00';
  firstStart.dispatchEvent(new window.Event('input', { bubbles: true }));
  check('开始等于结束会就地报错', text(doc, 'notify-quiet-error').includes('不能相同') &&
    doc.getElementById('notify-save').disabled === true);
  firstStart.value = '22:00';
  firstEnd.value = '08:00';
  firstStart.dispatchEvent(new window.Event('input', { bubbles: true }));
  check('修正后错误可恢复', doc.getElementById('notify-quiet-error').hidden === true &&
    doc.getElementById('notify-save').disabled === false);

  doc.getElementById('notify-save').click();
  await sleep(100);
  check('保存请求包含开始/结束时段', lastPut() &&
    Array.isArray(lastPut().global.quiet_hours) &&
    lastPut().global.quiet_hours.includes('22:00-08:00') &&
    lastPut().global.quiet_hours.includes('02:00-03:00'), JSON.stringify(lastPut() && lastPut().global.quiet_hours));

  const search = doc.getElementById('notify-session-search');
  search.value = 'beta';
  search.dispatchEvent(new window.Event('input', { bubbles: true }));
  check('会话搜索按会话/节点过滤', doc.querySelectorAll('.notify-session-group').length === 1 &&
    doc.querySelectorAll('.notify-session-row').length === 2 &&
    text(doc, 'notify-session-count').includes('2 / 4'), text(doc, 'notify-session-count'));
  search.value = '';
  search.dispatchEvent(new window.Event('input', { bubbles: true }));

  const onlyCustom = doc.getElementById('notify-session-only-custom');
  onlyCustom.checked = true;
  onlyCustom.dispatchEvent(new window.Event('change', { bubbles: true }));
  check('仅显示已调整会话', doc.querySelectorAll('.notify-session-row').length === 1 &&
    doc.querySelector('.notify-session-row').textContent.includes('自定义会话'));
  onlyCustom.checked = false;
  onlyCustom.dispatchEvent(new window.Event('change', { bubbles: true }));

  const enabled = doc.getElementById('notify-global-enabled');
  enabled.checked = false;
  enabled.dispatchEvent(new window.Event('change', { bubbles: true }));
  check('离开前有未保存状态', text(doc, 'notify-dirty') === '有未保存的修改');
  setConfirm(false);
  window.location.hash = '#dashboard';
  await sleep(80);
  check('取消离开时保留通知页', activeView(doc, 'notifications') && window.location.hash === '#notifications',
    window.location.hash);
  check('取消离开时保留草稿', enabled.checked === false && text(doc, 'notify-dirty') === '有未保存的修改');

  setConfirm(true);
  window.location.hash = '#dashboard';
  await sleep(80);
  check('确认离开后进入目标页', activeView(doc, 'dashboard') && !activeView(doc, 'notifications'));
  window.location.hash = '#notifications';
  await sleep(80);
  check('重新进入通知页读取已保存值', activeView(doc, 'notifications') &&
    doc.getElementById('notify-global-enabled').checked === true &&
    text(doc, 'notify-dirty') === '所有修改已保存');

  check('内部枚举未出现在可见文案', !view.textContent.includes('low（') &&
    !view.textContent.includes('normal（') && !view.textContent.includes('high（'));
  window.close();

  console.log(`\ntotal=${failures ? 'failed' : 'ok'} failed=${failures}`);
  process.exit(failures ? 1 : 0);
})();
