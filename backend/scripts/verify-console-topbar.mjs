// Verify the narrow-screen topbar contract: one overflow action, keyboard menu,
// theme/refresh reachable in two clicks, a single logout entry, and no duplicate
// topbar actions on phones.
//
// Usage (repo root):
//   node backend/scripts/verify-console-topbar.mjs
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

(async () => {
  const dom = new JSDOM(html, {
    url: 'https://console.test/#dashboard',
    runScripts: 'outside-only',
    pretendToBeVisual: true,
  });
  const { window } = dom;
  const doc = window.document;
  let nodesCalls = 0;
  window.fetch = async (input) => {
    const url = typeof input === 'string' ? input : (input && input.url) || String(input);
    if (url.includes('/api/nodes')) { nodesCalls++; return response({ nodes: [] }); }
    if (url.includes('/api/devices')) { return response({ devices: [] }); }
    if (url.includes('/api/connectors')) { return response({ items: [] }); }
    if (url.includes('/api/push/config')) { return response({ enabled: false }); }
    if (url.includes('/api/presence')) { return response({ ok: true }); }
    if (url.includes('/api/notify/policy')) {
      return response({
        policy: { version: 1, global: {}, sessions: {}, channels: {} },
        meta: { kinds: [], channels: [], defaults: {} },
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
  window.eval(appjs);
  await sleep(40);
  doc.dispatchEvent(new window.Event('DOMContentLoaded', { bubbles: true }));
  await sleep(100);

  const more = doc.getElementById('topbar-more');
  const menu = doc.getElementById('topbar-menu');
  const themeToggle = doc.getElementById('theme-toggle');
  const refresh = doc.getElementById('refresh');
  const logoutTop = doc.getElementById('logout-top');
  const logoutSide = doc.getElementById('logout');

  check('页头更多按钮存在', !!more && more.getAttribute('aria-haspopup') === 'menu');
  check('页头菜单有主题与刷新两项', menu.querySelectorAll('[role="menuitem"]').length === 2);
  check('页头菜单默认收起', menu.hidden === true && more.getAttribute('aria-expanded') === 'false');
  check('移动端只显示一个动作入口', css.includes('.topbar .topbar-more { display: inline-flex; }') &&
    css.includes('#theme-toggle, #refresh { display: none; }'));
  check('主题/刷新在移动端仍保留桌面入口', !!themeToggle && !!refresh);
  check('页头退出入口被移除', !!logoutTop && !!logoutSide &&
    css.includes('#logout-top { display: none !important; }'));
  check('页面禁止横向滚动', css.includes('overflow-x: hidden;'));
  check('标题可压缩并省略', css.includes('.view-title') &&
    css.includes('text-overflow: ellipsis;') && css.includes('white-space: nowrap;'));

  more.click();
  await sleep(10);
  check('打开菜单并展开 aria', menu.hidden === false && more.getAttribute('aria-expanded') === 'true');
  check('打开后焦点进入第一项', doc.activeElement === doc.getElementById('topbar-theme'));
  menu.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
  check('方向键移动菜单焦点', doc.activeElement === doc.getElementById('topbar-refresh'));
  menu.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Home', bubbles: true }));
  check('Home 回到首项', doc.activeElement === doc.getElementById('topbar-theme'));
  menu.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  check('Escape 收起菜单并归还焦点', menu.hidden === true && doc.activeElement === more &&
    more.getAttribute('aria-expanded') === 'false');

  more.click();
  doc.getElementById('topbar-theme').click();
  await sleep(10);
  check('菜单主题项可切换主题', doc.documentElement.getAttribute('data-theme') === 'light' &&
    doc.getElementById('theme-label').textContent === '亮色');
  check('主题项标签同步当前模式', doc.getElementById('topbar-theme').textContent.includes('亮色'));

  const before = nodesCalls;
  more.click();
  doc.getElementById('topbar-refresh').click();
  await sleep(50);
  check('菜单刷新项触发当前页刷新', nodesCalls > before, String(before) + ' -> ' + String(nodesCalls));
  check('刷新后菜单收起', menu.hidden === true && more.getAttribute('aria-expanded') === 'false');

  window.close();
  console.log(`\ntotal=${failures ? 'failed' : 'ok'} failed=${failures}`);
  process.exit(failures ? 1 : 0);
})();
