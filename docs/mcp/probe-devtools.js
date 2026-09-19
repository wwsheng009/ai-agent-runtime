// Probe the local DevTools endpoint on 9222 using raw sockets.
const net = require('net');

function probe(request, label) {
  return new Promise((resolve) => {
    const sock = net.createConnection({ host: '127.0.0.1', port: 9222 }, () => {
      sock.write(request);
    });
    let buf = '';
    const done = (result) => { try { sock.destroy(); } catch {} resolve(result); };
    sock.on('data', (d) => {
      buf += d.toString();
      if (buf.includes('\r\n\r\n') || buf.length > 20000) {
        done(`[${label}] status=${buf.split('\r\n')[0]}\nbody=${buf.split('\r\n\r\n')[1] || '(empty)'}\n`);
      }
    });
    sock.on('error', (e) => done(`[${label}] ERROR: ${e.message}\n`));
    setTimeout(() => done(`[${label}] TIMEOUT\nbuf=${buf.slice(0, 500)}`), 4000);
  });
}

(async () => {
  console.log(await probe('GET /json/list HTTP/1.1\r\nHost: 127.0.0.1:9222\r\nConnection: close\r\n\r\n', 'json/list'));
  console.log(await probe('GET /json/version HTTP/1.1\r\nHost: 127.0.0.1:9222\r\nConnection: close\r\n\r\n', 'json/version'));
  console.log(await probe('GET /devtools/browser/11b33a40-8afc-44cf-a092-710e35caff25 HTTP/1.1\r\nHost: 127.0.0.1:9222\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n', 'ws-handshake'));
})();
