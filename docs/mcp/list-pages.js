// List browser pages via CDP Target.getTargets. WebSocket handled with raw net socket (Node 24 global WebSocket may stall on this loopback).
const fs = require('fs');
const crypto = require('crypto');
const net = require('net');

const portFile = 'C:\\Users\\vince\\AppData\\Local\\Microsoft\\Edge\\User Data\\DevToolsActivePort';
const [port, wsPath] = fs.readFileSync(portFile, 'utf8').trim().split(/\r?\n/);

const key = crypto.randomBytes(16).toString('base64');
const req =
  `GET ${wsPath} HTTP/1.1\r\nHost: 127.0.0.1:${port}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n` +
  `Sec-WebSocket-Key: ${key}\r\nSec-WebSocket-Version: 13\r\n\r\n`;

const sock = net.createConnection({ host: '127.0.0.1', port: parseInt(port) }, () => sock.write(req));

let buf = Buffer.alloc(0);
let handshakeDone = false;

function fail(msg) { console.error(msg); try { sock.destroy(); } catch {} process.exit(1); }

const timer = setTimeout(() => fail('TIMEOUT: buf so far=' + buf.toString('utf8').slice(0, 400)), 8000);

function parseFrame() {
  // Minimal unmasked server-frame parser: expects FIN=1, no fragmentation.
  if (buf.length < 2) return null;
  const b0 = buf[0], b1 = buf[1];
  const opcode = b0 & 0x0f;
  const masked = (b1 & 0x80) !== 0;
  let len = b1 & 0x7f;
  let off = 2;
  if (len === 126) { if (buf.length < 4) return null; len = buf.readUInt16BE(2); off = 4; }
  else if (len === 127) { if (buf.length < 10) return null; len = Number(buf.readBigUInt64BE(2)); off = 10; }
  const maskLen = masked ? 4 : 0;
  if (buf.length < off + maskLen + len) return null;
  let payload = buf.slice(off + maskLen, off + maskLen + len);
  buf = buf.slice(off + maskLen + len);
  return { opcode, payload };
}

sock.on('data', (d) => {
  buf = Buffer.concat([buf, d]);
  if (!handshakeDone) {
    const idx = buf.indexOf('\r\n\r\n');
    if (idx === -1) return;
    const headers = buf.slice(0, idx).toString();
    if (!/101/.test(headers.split('\r\n')[0])) fail('HANDSHAKE FAILED: ' + headers.split('\r\n')[0]);
    buf = buf.slice(idx + 4);
    handshakeDone = true;
    send(JSON.stringify({ id: 1, method: 'Target.getTargets', params: {} }));
  }
  let frame;
  while ((frame = parseFrame())) {
    if (frame.opcode === 0x8) { fail('CLOSED by peer'); }
    if (frame.opcode === 0x1) {
      const msg = JSON.parse(frame.payload.toString('utf8'));
      if (msg.id === 1) {
        clearTimeout(timer);
        for (const t of msg.result.targetInfos) {
          console.log(`type=${t.type}\tattached=${t.attached}\ttitle=${t.title}\turl=${t.url}`);
        }
        sock.end();
        process.exit(0);
      }
    }
  }
});
sock.on('error', (e) => fail('SOCKET ERROR: ' + e.message));

function send(text) {
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
