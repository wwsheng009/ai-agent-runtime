// UX-08 regression: touch target floor, one debounced live summary, named
// controls, skip link, and no duplicate IDs / unlabeled form fields.
//
// Usage (repo root):
//   node backend/scripts/verify-console-a11y.mjs
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

const approvalSession = {
  session_id: 'sess-approval',
  title: 'needs approval',
  state: 'idle',
  busy: false,
  attention: 'approval',
  updated_at: new Date().toISOString(),
};
const nodeBase = {
  node_id: 'node-a11y-1',
  name: 'a11y-node',
  kind: 'aicli',
  stale: false,
  capabilities: ['runner', 'status', 'sessions'],
  routes: ['/web', '/web/api'],
  last_heartbeat: new Date().toISOString(),
};

(async () => {
  const sourceRefs = [];
  const dom = new JSDOM(html, {
    url: 'https://console.test/#dashboard',
    runScripts: 'outside-only',
    pretendToBeVisual: true,
  });
  const { window } = dom;
  const doc = window.document;

  let nodes = [{ ...nodeBase, sessions: [] }];
  window.fetch = async (input) => {
    const url = typeof input === 'string' ? input : (input && input.url) || String(input);
    if (url.includes('/api/nodes')) { return response({ nodes }); }
    if (url.includes('/api/devices')) { return response({ devices: [] }); }
    if (url.includes('/api/mounts')) { return response({ mounts: [] }); }
    if (url.includes('/api/connectors')) { return response({ items: [] }); }
    if (url.includes('/api/push/config')) { return response({ enabled: false }); }
    if (url.includes('/api/notify/policy')) {
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
  const source = () => sourceRefs[sourceRefs.length - 1];

  function textOfId(id) {
    const el = doc.getElementById(id);
    return el ? el.textContent.trim() : '';
  }
  function accessibleName(el) {
    const aria = (el.getAttribute('aria-label') || '').trim();
    if (aria) { return aria; }
    const by = el.getAttribute('aria-labelledby');
    if (by) {
      return by.split(/\s+/).map(textOfId).filter(Boolean).join(' ');
    }
    const text = (el.textContent || '').trim();
    if (text) { return text; }
    return (el.getAttribute('title') || el.getAttribute('alt') || '').trim();
  }
  function isHidden(el) {
    return !!el.closest('[hidden]') || el.getAttribute('aria-hidden') === 'true';
  }

  // ── document structure ───────────────────────────────────────────
  const visibleH1 = [...doc.querySelectorAll('h1')].filter((el) => !isHidden(el));
  check('当前视图只有一个可见 H1', visibleH1.length === 1,
    visibleH1.map((el) => el.textContent).join(' | '));
  const skip = doc.querySelector('a.skip-link[href="#main"]');
  check('提供跳到主内容链接', !!skip && skip.textContent.includes('跳到主内容'));
  check('主内容可编程聚焦', doc.getElementById('main').getAttribute('tabindex') === '-1');

  const ids = [...doc.querySelectorAll('[id]')].map((el) => el.id);
  const duplicateIds = ids.filter((id, i) => ids.indexOf(id) !== i);
  check('页面没有重复 ID', duplicateIds.length === 0, [...new Set(duplicateIds)].join(', '));

  // ── live regions ─────────────────────────────────────────────────
  const status = doc.getElementById('a11y-status');
  check('存在单一汇总状态播报节点', !!status && status.getAttribute('role') === 'status' &&
    status.getAttribute('aria-live') === 'polite' && status.getAttribute('aria-atomic') === 'true');
  check('汇总状态节点视觉隐藏', status.classList.contains('sr-only') && css.includes('.sr-only'));
  check('统计卡自身不再重复 live', !doc.getElementById('stat-attention-card').hasAttribute('aria-live'));
  check('事件条不再逐条 live 播报', !doc.getElementById('event-strip').hasAttribute('aria-live'));

  const focusBefore = doc.getElementById('device-search');
  focusBefore.focus();
  nodes = [{ ...nodeBase, sessions: [approvalSession] }];
  source().emit('nodes', { nodes });
  await sleep(200);
  check('计数播报有防抖延迟', status.textContent === '', status.textContent);
  await sleep(1500);
  check('计数变化汇总播报完整语义', status.textContent === '待处理 1 项', status.textContent);
  check('动态计数不抢焦点', doc.activeElement === focusBefore,
    doc.activeElement && (doc.activeElement.id || doc.activeElement.tagName));

  // ── touch targets / motion ───────────────────────────────────────
  check('粗指针小按钮采用 44px 底线',
    /@media\s*\(pointer:\s*coarse\)[\s\S]*?\.btn\.sm\s*\{[^}]*min-height:\s*44px/s.test(css));
  check('粗指针分段控件采用 44px',
    /@media\s*\(pointer:\s*coarse\)[\s\S]*?\.seg-btn\s*\{[^}]*min-height:\s*44px/s.test(css));
  check('粗指针筛选 chip 采用 44px',
    /@media\s*\(pointer:\s*coarse\)[\s\S]*?\.attn-chip\s*\{[^}]*min-height:\s*44px/s.test(css));
  check('粗指针事件 chip 采用 44px',
    /@media\s*\(pointer:\s*coarse\)[\s\S]*?\.event-chip\s*\{[^}]*min-height:\s*44px/s.test(css));
  check('粗指针会话 ID 触点采用 44px',
    /@media\s*\(pointer:\s*coarse\)[\s\S]*?\.session-card \.s-id\s*\{[^}]*min-height:\s*44px/s.test(css));
  check('粗指针输入与下拉采用 44px',
    /@media\s*\(pointer:\s*coarse\)[\s\S]*?input,\s*\.select\s*\{[^}]*min-height:\s*44px/s.test(css));
  check('保留减少动态效果降级', css.includes('prefers-reduced-motion: reduce'));
  check('保留 focus-visible 焦点环', /:focus-visible\s*\{[^}]*outline:/s.test(css));

  // ── names for controls ───────────────────────────────────────────
  const controls = [...doc.querySelectorAll('button, a[href], select, textarea, [role="menuitem"]')]
    .filter((el) => !el.disabled && !isHidden(el));
  const unnamed = controls.filter((el) => !accessibleName(el));
  check('全部可交互控件有可访问名称', unnamed.length === 0,
    unnamed.slice(0, 6).map((el) => el.tagName + (el.id ? '#' + el.id : '') + '.' + el.className).join(' | '));

  const fields = [...doc.querySelectorAll('input:not([type="hidden"]), select, textarea')];
  const unlabeled = fields.filter((el) => {
    if (accessibleName(el)) { return false; }
    if (el.labels && el.labels.length) { return false; }
    return true;
  });
  check('全部表单字段有标签', unlabeled.length === 0,
    unlabeled.slice(0, 6).map((el) => el.tagName + (el.id ? '#' + el.id : '') + '.' + el.className).join(' | '));

  const iconButtons = [...doc.querySelectorAll('button')].filter((el) => {
    if (isHidden(el)) { return false; }
    const text = (el.textContent || '').trim();
    return text === '' || text === '×' || text === '⋯';
  });
  check('图标按钮都有可访问名称', iconButtons.every((el) => !!accessibleName(el)),
    iconButtons.filter((el) => !accessibleName(el)).map((el) => el.id || el.className).join(' | '));

  window.close();
  console.log(`\ntotal=${failures ? 'failed' : 'ok'} failed=${failures}`);
  process.exit(failures ? 1 : 0);
})();