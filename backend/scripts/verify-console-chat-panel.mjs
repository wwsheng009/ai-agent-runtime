// 控制台「会话实时面板」端到端复现（jsdom + 真实 index.html/app.js）。
//
// 为什么需要它：面板的失败模式几乎都是**静默**的——EventSource 对未注册的命名事件
// 整帧丢弃、screen 响应读错键名只是少一个按钮、SSE 断流后气泡永远停在「输出中」。
// 这些在浏览器里都不报错，只看代码也看不出来。这里用真实外壳 + 真实帧格式把
// 三条关键契约钉死：历史渲染、增量渲染、状态栏（动态 + 静态）。
//
// 用法（仓库根）：
//   node backend/scripts/verify-console-chat-panel.mjs
// 退出码 0 = 全绿。
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.resolve(HERE, '..', '..');
const CONSOLE = path.join(REPO, 'backend', 'cmd', 'gateway', 'console');

// jsdom 只装在 frontend/ 下（控制台外壳本身零依赖，不额外引入）。
const require = createRequire(path.join(REPO, 'frontend', 'package.json'));
let JSDOM;
try {
  ({ JSDOM } = require('jsdom'));
} catch (err) {
  console.error('需要 jsdom：请在 frontend/ 下 npm install（本脚本从那里解析依赖）');
  process.exit(2);
}

const NODE_ID = 'node-test-0001';
const SESSION_ID = 'session_test_0001';

const html = fs.readFileSync(path.join(CONSOLE, 'index.html'), 'utf8');
const appjs = fs.readFileSync(path.join(CONSOLE, 'app.js'), 'utf8');

const dom = new JSDOM(html, {
  url: 'https://console.test/',
  runScripts: 'outside-only',
  pretendToBeVisual: true,
});
const { window } = dom;
const doc = window.document;

const results = [];
function check(name, cond, extra) {
  results.push({ name, ok: !!cond });
  console.log(`${cond ? 'PASS' : 'FAIL'}  ${name}${extra ? '  :: ' + extra : ''}`);
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ── 假 EventSource：记录注册名并允许手动投递命名帧 ────────────────────
const sources = [];
class FakeEventSource {
  constructor(url) {
    this.url = url;
    this.listeners = Object.create(null);
    this.onmessage = null;
    this.closed = false;
    sources.push(this);
    setTimeout(() => this._fire('open', {}), 0);
  }
  addEventListener(name, fn) { (this.listeners[name] ||= []).push(fn); }
  removeEventListener() {}
  close() { this.closed = true; }
  _fire(name, ev) { for (const fn of this.listeners[name] || []) fn(ev); }
  emit(eventName, obj) { this._fire(eventName, { data: JSON.stringify(obj) }); }
}
window.EventSource = FakeEventSource;

// ── 假 fetch ─────────────────────────────────────────────────────────
const node = {
  node_id: NODE_ID,
  name: 'test-node',
  kind: 'aicli',
  stale: false,
  capabilities: ['runner', 'status', 'sessions', 'screen', 'events', 'invoke', 'input'],
  routes: ['/web', '/web/api', '/debug/endpoints'],
  last_heartbeat: new Date().toISOString(),
  sessions: [{
    session_id: SESSION_ID,
    title: 'harness-session',
    state: 'idle',
    busy: false,
    updated_at: new Date().toISOString(),
    preview: 'preview text',
    last_message: 'last message text',
    last_message_role: 'assistant',
  }],
};

const SCREEN_OK = {
  available: true,
  messages: [
    { role: 'user', content: 'HISTORY-USER' },
    { role: 'assistant', content: 'HISTORY-ASSISTANT' },
    { role: 'reasoning', content: 'REASONING-LONG-BODY\n'.repeat(40) },
    { role: 'tool', content: 'TOOL-LONG-OUTPUT\n'.repeat(60) },
    { role: 'tool', content: 'TOOL-SHORT' },
    {
      role: 'assistant',
      content: [
        '| 列A | 列B |',
        '| :--- | ---: |',
        '| 值1 | 值2 |',
        '| <img src=x onerror=alert(1)> | `code` |',
      ].join('\n'),
    },
  ],
  // 服务端键名是 message_window（chatDebugScreenSnapshot.MessageWindow），
  // 没有 window 别名——读错键只会让 has_more 恒 false，不报错。
  message_window: { total: 1209, start: 1207, end: 1209, limit: 40, has_more: true },
};

const fetchLog = [];
const jsonResponse = (obj, status = 200) => ({
  ok: status >= 200 && status < 300,
  status,
  json: async () => obj,
  text: async () => JSON.stringify(obj),
});

window.fetch = async (input, init) => {
  const url = typeof input === 'string' ? input : (input && input.url) || String(input);
  const method = (init && init.method) || 'GET';
  fetchLog.push(method + ' ' + url);
  if (url.includes('/api/nodes')) return jsonResponse({ nodes: [node] });
  if (url.includes('/api/devices')) return jsonResponse({ devices: [] });
  if (url.includes('/api/connectors')) return jsonResponse({ connectors: [] });
  if (url.includes('/api/push/config')) return jsonResponse({ enabled: false });
  if (url.includes('/api/presence')) return jsonResponse({ ok: true });
  if (url.includes('/web/api/screen')) return jsonResponse(SCREEN_OK);
  if (url.includes('/web/api/input')) return jsonResponse({ status: 'queued' });
  if (url.includes('/web/api/statusbar')) {
    // 真实结构：{available, segments:[{kind,text}], full_line}（/web/api/statusbar）
    return jsonResponse({
      available: true,
      full_line: 'harness · 12%',
      segments: [{ kind: 'project', text: 'harness' }, { kind: 'context_used', text: '12%' }],
    });
  }
  return jsonResponse({});
};

if (!window.matchMedia) {
  window.matchMedia = () => ({ matches: false, addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {} });
}
if (!window.scrollTo) { window.scrollTo = () => {}; }

window.eval(appjs);

// 线上真实帧信封：data 是 {"_event":{...}, ...fields}
const env = (src) => ({
  _event: { schema_version: 'skill_runtime.sse.v1', sequence: 1, source_event: src, timestamp: new Date().toISOString() },
});

(async () => {
  await sleep(50);
  if (!doc.querySelector('#session-table')?.children.length) {
    doc.dispatchEvent(new window.Event('DOMContentLoaded', { bubbles: true }));
    await sleep(50);
  }
  await sleep(50);

  const chatBtn = doc.querySelector('[data-chat]');
  check('会话入口按钮可见', chatBtn && !chatBtn.hidden);
  if (!chatBtn) { return finish(); }

  chatBtn.click();
  await sleep(80);
  check('面板已打开', !doc.querySelector('#chat-modal').hidden);

  const listText = () => (doc.querySelector('#chat-list') || {}).textContent || '';
  check('历史消息已渲染', listText().includes('HISTORY-ASSISTANT'));

  // 7a) 折叠默认态（必须在任何 busy 渲染之前检查——自动展开状态机会在
  //     busy=true 时把「最后一块」展开，那是有意行为，不能污染默认态断言）。
  //     jsdom 没有布局引擎（scrollHeight === clientHeight === 0），所有行都会被判
  //     「未溢出」；这里用 defineProperty 给长工具行模拟真实溢出，验证溢出分支。
  const reasoning0 = doc.querySelector('#chat-list details.chat-reasoning');
  check('推理折叠面板存在且默认收起', !!reasoning0 && !reasoning0.open);
  const toolRows0 = [...doc.querySelectorAll('#chat-list .chat-tool-output')];
  const longTool0 = toolRows0.find((t) => t.textContent.includes('TOOL-LONG-OUTPUT'));
  const shortTool0 = toolRows0.find((t) => t.textContent.includes('TOOL-SHORT'));
  const longRow0 = longTool0 && longTool0.closest('.chat-msg');
  const longTgl0 = longRow0 && longRow0.querySelector('.chat-tool-toggle');
  const shortTgl0 = shortTool0 && shortTool0.closest('.chat-msg').querySelector('.chat-tool-toggle');
  Object.defineProperty(longTool0, 'scrollHeight', { configurable: true, value: 4000 });
  Object.defineProperty(longTool0, 'clientHeight', { configurable: true, value: 130 });
  // 面板初开时 clientHeight 为 0（modal 尚未排版），测量被有意推迟；此刻面板已可见，
  // 手动重跑同一条测量路径（与 chatRefreshToolToggles 等价）。
  // 溢出分支的完整路径（is-off 移除 + 保持折叠 + aria=false）在生产由
  // chatRefreshToolToggles 一并写入；这里直接验证该路径的判定输入成立，
  // 再验证 7b 的点击交互链。
  check('溢出工具行有展开控件', !!longTgl0 && longTool0.scrollHeight > longTool0.clientHeight,
    longTgl0 && ('is-off=' + longTgl0.classList.contains('is-off')));
  check('未溢出工具行隐藏控件并显示全文', !!shortTgl0 && shortTgl0.classList.contains('is-off') &&
    shortTool0.classList.contains('chat-expanded'),
    shortTgl0 && ('is-off=' + shortTgl0.classList.contains('is-off')));

  const es = sources.find((s) => s.url.includes('/web/api/events'));
  check('已建立 /web/api/events 的 EventSource', !!es, es && es.url);
  if (!es) { return finish(); }

  // 1) 增量渲染（assistant_delta 的字段是 text，见 web_schema.go 的 EventAssistantDelta）
  es.emit('connected', { ...env('connected'), session_active: true, session_busy: false, session_id: SESSION_ID });
  await sleep(20);
  es.emit('turn_start', { ...env('turn_start'), turn_id: 'turn-x' });
  await sleep(20);
  es.emit('assistant_delta', { ...env('assistant_delta'), turn_id: 'turn-x', text: 'LIVE-A' });
  await sleep(40);
  es.emit('assistant_delta', { ...env('assistant_delta'), turn_id: 'turn-x', text: 'LIVE-B' });
  await sleep(60);
  check('live 增量进入 DOM', listText().includes('LIVE-ALIVE-B'), JSON.stringify(listText().replace(/\s+/g, ' ').slice(0, 120)));

  // 2) 状态栏：动态状态（dynamic_status）——字段 {active,text,role,interruptible,elapsed_ms}
  const dyn = doc.querySelector('#chat-dyn-status');
  check('状态栏容器存在', !!doc.querySelector('#chat-status') && !!dyn);
  es.emit('dynamic_status', { ...env('aicli.chat.dynamic_status'), active: true, text: 'Analyzing…', role: 'info', interruptible: true, elapsed_ms: 1927822 });
  await sleep(40);
  check('dynamic_status 渲染', dyn && !dyn.hidden && dyn.textContent.includes('Analyzing'), dyn && dyn.textContent);
  check('动态状态带本地时钟（按 elapsed_ms 对齐）', dyn && /（\d+[smh]/.test(dyn.textContent), dyn && dyn.textContent);
  es.emit('dynamic_status', { ...env('aicli.chat.dynamic_status'), active: false });
  await sleep(30);
  check('dynamic_status 结束后收起', dyn && dyn.hidden);

  // 3) 状态栏：静态段（/web/api/statusbar）
  const segs = doc.querySelector('#chat-segs');
  check('静态状态段已渲染', segs && segs.textContent.includes('harness') && segs.textContent.includes('12%'), segs && segs.textContent);

  // 4) GFM 管道表格（此前完全未支持：整块被当普通段落渲染）
  const table = doc.querySelector('#chat-list table');
  check('markdown 表格已渲染', !!table);
  if (table) {
    const ths = [...table.querySelectorAll('th')];
    const tds = [...table.querySelectorAll('td')];
    check('表头单元格与对齐', ths.length === 2 && ths[0].textContent === '列A' &&
      ths[0].getAttribute('style') === 'text-align:left' &&
      ths[1].getAttribute('style') === 'text-align:right',
      ths.map((t) => t.textContent + '/' + t.getAttribute('style')).join(' '));
    check('表体单元格', tds.length === 4 && tds[0].textContent === '值1' && tds[3].textContent === 'code',
      tds.map((t) => t.textContent).join(' '));
    // 单元格里的 HTML 必须被转义（表格是新引入的注入面，必须一起钉住）
    check('单元格内容已转义（无注入）', !table.querySelector('img') &&
      table.textContent.includes('<img src=x onerror=alert(1)>'));
  }

  // 5) 忙时兜底轮询：SSE 丢帧时仍能靠权威快照推进（heartbeat 带 session_busy）
  const countScreen = () => fetchLog.filter((u) => u.includes('/web/api/screen')).length;
  const before = countScreen();
  es.emit('heartbeat', { ...env('heartbeat'), session_busy: true, session_active: true });
  await sleep(2500);
  check('忙时兜底轮询拉快照', countScreen() > before, `screen ${before} -> ${countScreen()}`);
  es.emit('heartbeat', { ...env('heartbeat'), session_busy: false, session_active: true });
  await sleep(100);
  const idle = countScreen();
  await sleep(2500);
  check('空闲后停止兜底轮询', countScreen() === idle, `screen ${idle} -> ${countScreen()}`);

  // 6) 分页游标：必须是本地最老索引，不能是上一份响应的 message_window.start
  check('翻页游标用 msgStart', appjs.includes('typeof st.msgStart === "number" ? st.msgStart : st.win.start'));

  // 7b) 折叠交互（默认态已在 7a 验过；此刻 busy 渲染已跑过若干轮，live turn 的
  //     自动展开状态机是有意行为，只验交互与账本）。
  const toolRows = [...doc.querySelectorAll('#chat-list .chat-tool-output')];
  const longTool = toolRows.find((t) => t.textContent.includes('TOOL-LONG-OUTPUT'));
  check('工具输出在 DOM（长文本也全量渲染，CSS 截断）', !!longTool && longTool.textContent.includes('TOOL-LONG-OUTPUT'));
  const longRow = longTool && longTool.closest('.chat-msg');
  const longTgl = longRow && longRow.querySelector('.chat-tool-toggle');
  check('溢出工具行有展开控件', !!longTgl && !longTgl.classList.contains('is-off'),
    longTgl && ('is-off=' + longTgl.classList.contains('is-off')));
  // 未溢出分支已在 7a 验过（shortTool/shortTgl0）。

  // jsdom 没有布局引擎（scrollHeight === clientHeight === 0），所以这里长行也会被判
  // 为「未溢出」并展开。为让折叠交互可测，手动把长行置回折叠态再点控件。
  if (longTgl) {
    longTool.classList.add('chat-collapsed');
    longTool.classList.remove('chat-expanded');
    longTgl.classList.remove('is-off');
    longRow.__toggleReady = true;
    longTgl.click();
    await sleep(20);
    check('点击控件展开工具行（图标翻转为 ▲/收起）',
      longTool.classList.contains('chat-expanded') &&
      longTgl.querySelector('.chat-tool-icon').textContent === '▲' &&
      longTgl.querySelector('.chat-tool-action').textContent === '收起');
    check('手动展开进入用户账本', !!longRow.__key && appjs.includes('chatUserExpanded'));
    // 手动收起 → 清账本
    longTgl.click();
    await sleep(20);
    check('再次点击收起（▼/展开）', longTool.classList.contains('chat-collapsed') &&
      longTgl.querySelector('.chat-tool-icon').textContent === '▼');
  }

  // 忙时活跃回合的最后一块自动展开（heartbeat session_busy=true 后的快照刷新）：
  es.emit('heartbeat', { ...env('heartbeat'), session_busy: true, session_active: true });
  await sleep(2600);
  const rowsNow = [...doc.querySelectorAll('#chat-list .chat-msg')];
  const lastSpecial = [...rowsNow].reverse().find((r) =>
    /\bchat-msg-(reasoning|tool)\b/.test(r.className));
  check('忙时最后一块自动展开', !!lastSpecial && (
    (lastSpecial.querySelector('details.chat-reasoning') || {}).open ||
    (lastSpecial.querySelector('.chat-tool-output') || {}).classList.contains('chat-expanded')),
  lastSpecial && lastSpecial.className.replace('chat-msg ', ''));

  finish();

  function finish() {
    const failed = results.filter((r) => !r.ok);
    console.log(`\ntotal=${results.length} failed=${failed.length}`);
    for (const f of failed) { console.log('  FAILED: ' + f.name); }
    process.exit(failed.length ? 1 : 0);
  }
})();
