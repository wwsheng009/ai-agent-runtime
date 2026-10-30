// Verify the gateway console connector view aggregates gateway-hosted and
// node-local connector lists without duplicating the shared config.yaml seen
// by multiple nodes.
//
// Usage (repo root):
//   node backend/scripts/verify-console-connectors.mjs
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
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  };
}

(async () => {
  const dom = new JSDOM(html, {
    url: 'https://console.test/#connectors',
    runScripts: 'outside-only',
    pretendToBeVisual: true,
  });
  const { window } = dom;
  const doc = window.document;
  const nodeRequests = [];
  let gatewayEmpty = false;

  window.fetch = async (input) => {
    const url = typeof input === 'string' ? input : (input && input.url) || String(input);
    if (url === '/api/nodes') {
      return response({
        nodes: [
          {
            node_id: 'node-a',
            name: 'A',
            kind: 'aicli',
            routes: ['/web', '/web/api', '/api/connectors'],
            sessions: [],
          },
          {
            node_id: 'node-b',
            name: 'B',
            kind: 'aicli',
            routes: ['/web', '/web/api', '/api/connectors'],
            sessions: [],
          },
          {
            node_id: 'node-stale',
            name: '离线快照',
            kind: 'aicli',
            stale: true,
            routes: ['/api/connectors'],
            sessions: [],
          },
        ],
      });
    }
    if (url === '/api/connectors') {
      if (gatewayEmpty) { return response({ items: [] }); }
      return response({
        items: [{
          id: 'gw-1',
          provider: 'telegram',
          name: '网关通道',
          enabled: true,
          active_owner: 'gateway',
          owned_by_current_host: true,
          secret_keys: ['bot_token'],
        }],
      });
    }
    if (url === '/api/connectors/schema') {
      return response({ providers: [] });
    }
    if (url === '/api/nodes/node-a/connectors') {
      nodeRequests.push('node-a');
      return response({
        items: [{
          id: 'local-1',
          provider: 'wechat',
          name: '微信-本地',
          enabled: true,
          mode: 'polling',
          active_owner: 'aicli:node-a',
          owned_by_current_host: true,
          secret_keys: ['bot_token'],
        }],
      });
    }
    if (url === '/api/nodes/node-b/connectors') {
      nodeRequests.push('node-b');
      return response({
        items: [{
          id: 'local-1',
          provider: 'wechat',
          name: '微信-本地',
          enabled: true,
          mode: 'polling',
          active_owner: 'aicli:node-a',
          owned_by_current_host: false,
          secret_keys: ['bot_token'],
        }],
      });
    }
    if (url.startsWith('/api/nodes/node-stale/connectors')) {
      nodeRequests.push('node-stale');
      return response({ items: [] });
    }
    if (url === '/api/devices') { return response({ devices: [] }); }
    if (url === '/api/push/config') { return response({ enabled: false }); }
    if (url === '/api/presence') { return response({ ok: true }); }
    if (url === '/api/notify/policy') {
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
    window.matchMedia = () => ({
      matches: false,
      addListener() {},
      removeListener() {},
      addEventListener() {},
      removeEventListener() {},
    });
  }
  if (!window.scrollTo) { window.scrollTo = () => {}; }
  if (!window.Element.prototype.scrollIntoView) {
    window.Element.prototype.scrollIntoView = () => {};
  }
  if (!window.open) { window.open = () => null; }

  window.eval(appjs);
  await sleep(40);
  doc.dispatchEvent(new window.Event('DOMContentLoaded', { bubbles: true }));
  await sleep(250);

  const list = doc.getElementById('connectors-list');
  const count = doc.getElementById('connectors-count');
  const text = list ? list.textContent : '';
  const links = Array.from(list.querySelectorAll('a')).map((a) => a.getAttribute('href'));

  check('页面渲染网关连接器', text.includes('网关通道') && text.includes('来源 网关'));
  check('页面渲染节点本地连接器', text.includes('微信-本地'));
  check('共享配置按 id 去重', (text.match(/微信-本地/g) || []).length === 1, text);
  check('保留运行节点的来源', text.includes('来源 A / B'), text);
  check('网关条目链接走网关 API', links.includes('/api/connectors/gw-1'), links.join(','));
  check('节点条目链接走节点只读入口', links.includes('/api/nodes/node-a/connectors/local-1'), links.join(','));
  check('计数为去重后的连接器数', count && count.textContent === '2 个', count && count.textContent);
  check('离线快照节点不发起读取', !nodeRequests.includes('node-stale'), nodeRequests.join(','));

  // 用户实际场景：网关自身 store 为空，但节点本地有配置。刷新后仍应显示节点条目。
  gatewayEmpty = true;
  doc.getElementById('refresh').click();
  await sleep(250);
  const emptyGatewayText = list.textContent;
  check('网关空列表不遮蔽节点连接器', emptyGatewayText.includes('微信-本地'), emptyGatewayText);
  check('网关空列表后计数只算节点条目', count && count.textContent === '1 个', count && count.textContent);

  window.close();
  console.log(`\ntotal=${failures ? 'failed' : 'ok'} failed=${failures}`);
  process.exit(failures ? 1 : 0);
})();