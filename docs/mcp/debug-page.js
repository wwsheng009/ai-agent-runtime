// Attach to the localhost:5173 page target, reload, capture console/exception/network errors, then dump DOM state.
const fs = require('fs');
const crypto = require('crypto');
const net = require('net');

const portFile = 'C:\\Users\\vince\\AppData\\Local\\Microsoft\\Edge\\User Data\\DevToolsActivePort';
const [port, wsPath] = fs.readFileSync(portFile, 'utf8').trim().split(/\r?\n/);
const PORT = parseInt(port);

const key = crypto.randomBytes(16).toString('base64');
const sock = net.createConnection({ host: '127.0.0.1', port: PORT }, () =>
  sock.write(`GET ${wsPath} HTTP/1.1\r\nHost: 127.0.0.1:${PORT}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: ${key}\r\nSec-WebSocket-Version: 13\r\n\r\n`));

let buf = Buffer.alloc(0), handshakeDone = false, pageSessionId = null, nextId = 1;
const pending = new Map();
const logs = { console: [], exceptions: [], network: [], evals: {} };
let phase = 'attach';

function fail(msg) { console.error('FATAL: ' + msg); try { sock.destroy(); } catch {} process.exit(1); }
function send(method, params = {}, sessionId = pageSessionId) {
  const id = nextId++;
  const msg = { id, method, params };
  if (sessionId) msg.sessionId = sessionId;
  pending.set(id, { method });
  return id;
}

// ---- websocket framing ----
function parseFrame() {
  if (buf.length < 2) return null;
  const b0 = buf[0], b1 = buf[1];
  const opcode = b0 & 0x0f;
  let len = b1 & 0x7f, off = 2;
  if (len === 126) { if (buf.length < 4) return null; len = buf.readUInt16BE(2); off = 4; }
  else if (len === 127) { if (buf.length < 10) return null; len = Number(buf.readBigUInt64BE(2)); off = 10; }
  const maskLen = (b1 & 0x80) ? 4 : 0;
  if (buf.length < off + maskLen + len) return null;
  const payload = buf.slice(off + maskLen, off + maskLen + len);
  buf = buf.slice(off + maskLen + len);
  return { opcode, payload };
}
function sendWs(text) {
  const payload = Buffer.from(text, 'utf8');
  const mask = crypto.randomBytes(4);
  let header;
  if (payload.length < 126) header = Buffer.from([0x81, 0x80 | payload.length]);
  else if (payload.length < 65536) { header = Buffer.alloc(4); header[0] = 0x81; header[1] = 0x80 | 126; header.writeUInt16BE(payload.length, 2); }
  else { header = Buffer.alloc(10); header[0] = 0x81; header[1] = 0x80 | 127; header.writeBigUInt64BE(BigInt(payload.length), 2); }
  const masked = Buffer.alloc(payload.length);
  for (let i = 0; i < payload.length; i++) masked[i] = payload[i] ^ mask[i % 4];
  sock.write(Buffer.concat([header, mask, masked]));
}

const timer = setTimeout(() => finish('TIMEOUT in phase ' + phase), 25000);

function finish(why) {
  clearTimeout(timer);
  console.log('=== finish reason: ' + why + ' ===');
  console.log('\n--- CONSOLE (' + logs.console.length + ') ---');
  for (const c of logs.console) console.log(`[${c.level}] ${c.text.slice(0, 500)}`);
  console.log('\n--- EXCEPTIONS (' + logs.exceptions.length + ') ---');
  for (const e of logs.exceptions) console.log((e.description || e.text || JSON.stringify(e)).slice(0, 800));
  console.log('\n--- NETWORK ISSUES (' + logs.network.length + ') ---');
  for (const n of logs.network) console.log(n);
  console.log('\n--- EVAL RESULTS ---');
  for (const [k, v] of Object.entries(logs.evals)) console.log(`${k}: ${String(v).slice(0, 1500)}`);
  try { sock.destroy(); } catch {}
  process.exit(0);
}

function evaluate(expr, label) {
  const id = nextId;
  send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
  logs['eval_' + id] = label;
}

sock.on('data', (d) => {
  buf = Buffer.concat([buf, d]);
  if (!handshakeDone) {
    const idx = buf.indexOf('\r\n\r\n');
    if (idx === -1) return;
    const status = buf.slice(0, idx).toString().split('\r\n')[0];
    if (!/101/.test(status)) fail('handshake: ' + status);
    buf = buf.slice(idx + 4);
    handshakeDone = true;
    phase = 'find target';
    send('Target.getTargets', {});
    return;
  }
  let frame;
  while ((frame = parseFrame())) {
    if (frame.opcode === 0x8) fail('closed by peer');
    if (frame.opcode !== 0x1) continue;
    const msg = JSON.parse(frame.payload.toString('utf8'));

    if (msg.id && pending.has(msg.id)) {
      const method = pending.get(msg.id).method;
      pending.delete(msg.id);
      if (method === 'Target.getTargets') {
        const page = msg.result.targetInfos.find(t => t.type === 'page' && t.url.includes('localhost:5173'));
        if (!page) finish('NO PAGE TARGET for localhost:5173');
        phase = 'attach to page';
        send('Target.attachToTarget', { targetId: page.targetId, flatten: true });
      } else if (method === 'Target.attachToTarget') {
        pageSessionId = msg.result.sessionId;
        phase = 'enable domains';
        send('Runtime.enable');
        send('Log.enable');
        send('Network.enable');
        send('Page.enable');
        phase = 'reload';
        send('Page.reload', { ignoreCache: true });
      } else if (method === 'Runtime.evaluate') {
        const label = logs['eval_' + msg.id] || 'eval';
        logs.evals[label] = msg.result && msg.result.result ? (msg.result.result.value !== undefined ? JSON.stringify(msg.result.result.value) : JSON.stringify(msg.result)) : JSON.stringify(msg);
        if (label === '__final') finish('done');
      }
      continue;
    }

    if (!msg.method || !pageSessionId || msg.sessionId !== pageSessionId) continue;
    const { method, params } = msg;
    if (method === 'Runtime.consoleAPICalled') {
      const text = params.args.map(a => a.value !== undefined ? String(a.value) : (a.description || a.type)).join(' ');
      logs.console.push({ level: params.type, text });
    } else if (method === 'Runtime.exceptionThrown') {
      const d = params.exceptionDetails;
      logs.exceptions.push(d.exception || { text: d.text });
    } else if (method === 'Log.entryAdded') {
      const e = params.entry;
      if (e.level === 'error' || e.level === 'warning') logs.console.push({ level: 'log.' + e.level, text: e.text + ' @' + (e.url || '') });
    } else if (method === 'Network.responseReceived') {
      const r = params.response;
      if (r.status >= 400) logs.network.push(`${r.status} ${r.url}`);
    } else if (method === 'Network.loadingFailed') {
      logs.network.push(`FAILED(${params.errorText}) ${params.blockedReason || ''} type=${params.type}`);
    }
    // After 6s of reload, probe DOM state.
  }
});
sock.on('error', (e) => fail('socket: ' + e.message));

setTimeout(() => {
  phase = 'probe DOM';
  evaluate('JSON.stringify({readyState: document.readyState, title: document.title, bodyLen: document.body ? document.body.innerHTML.length : -1, rootHtml: document.getElementById("root") ? document.getElementById("root").innerHTML.slice(0, 300) : "NO #root", bodyStart: document.body ? document.body.innerHTML.slice(0, 200) : ""})', 'dom');
  evaluate('window.__lastError || null', '__lastError');
  evaluate('navigator.userAgent', 'ua');
  setTimeout(() => evaluate('"__final"', '__final'), 800);
}, 6000);
