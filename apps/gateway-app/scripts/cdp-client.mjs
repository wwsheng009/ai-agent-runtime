// Shared WebView2 CDP client for the dev-only probe scripts.
export async function connectPage(port) {
  const targets = await discoverTargets(port);
  const page = targets.find((target) => target.type === "page");
  if (!page) throw new Error(`no page target on port ${port}`);

  const socket = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => {
    socket.addEventListener("open", resolve, { once: true });
    socket.addEventListener("error", reject, { once: true });
  });

  let nextId = 1;
  const pending = new Map();
  socket.addEventListener("message", (event) => {
    const message = JSON.parse(event.data);
    const waiter = pending.get(message.id);
    if (!waiter) return;
    pending.delete(message.id);
    if (message.error) waiter.reject(new Error(JSON.stringify(message.error)));
    else waiter.resolve(message.result);
  });

  function call(method, params = {}) {
    const id = nextId++;
    socket.send(JSON.stringify({ id, method, params }));
    return new Promise((resolve, reject) => pending.set(id, { resolve, reject }));
  }

  await call("Runtime.enable");
  return { page, socket, call };
}

async function discoverTargets(port) {
  const errors = [];
  for (const host of ["127.0.0.1", "[::1]"]) {
    try {
      const response = await fetch(`http://${host}:${port}/json`);
      if (!response.ok) {
        errors.push(`${host}: HTTP ${response.status}`);
        continue;
      }
      const targets = await response.json();
      if (Array.isArray(targets) && targets.length > 0) return targets;
      errors.push(`${host}: no targets`);
    } catch (error) {
      errors.push(`${host}: ${error.message}`);
    }
  }
  throw new Error(`CDP discovery failed (${errors.join("; ")})`);
}
