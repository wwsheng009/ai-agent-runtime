// e2e mock runtime server.
//
// Serves the API surface the workspace page needs on the port the vite dev
// server proxies /api to (default 8101), and answers POST /api/agent/chat
// with deterministic, scripted SSE streams so the e2e tests can assert the
// live rendering behaviour (reasoning before chunks, tool card lifecycle,
// scroll-follow, phase status, stopped marker) without a real model backend.
//
// Script selection is driven by keywords in the chat request payload
// (concatenated string fields):
//   "tool"       -> meta + tool_start/tool_call/tool_end + chunks + done
//   "scroll"     -> meta + many spaced-out long chunks + done
//   "error"      -> meta + reasoning + partial chunk + error event (no done)
//   "interrupt"  -> meta + chunks forever until the client aborts
//   otherwise    -> meta + reasoning first + chunks + done

import http from "node:http";

const PORT = Number(process.env.MOCK_PORT ?? 8101);

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function sseEvent(event, payload) {
  return `event: ${event}\ndata: ${JSON.stringify(payload)}\n\n`;
}

function makeChunk(index, text, type = "text") {
  return {
    index,
    type,
    content: text,
    text: { content: text, total_chars: text.length },
  };
}

const META = {
  session_id: "e2e-session-1",
  agent_id: "e2e-agent",
  model: "e2e-mock-model",
  kind: "chat",
  status: "started",
};

const DONE = {
  session_id: "e2e-session-1",
  agent_id: "e2e-agent",
  status: "completed",
  content: "The capital of France is Paris.",
};

const TOOL_ARGS = { query: "capital of France" };

const toolScript = [
  { event: "meta", payload: META },
  {
    event: "tool_start",
    delay: 150,
    payload: {
      type: "tool_start",
      index: 0,
      status: "started",
      tool: { id: "tool-1", name: "web_search", args: TOOL_ARGS },
      tool_call: { id: "tool-1", name: "web_search", args: TOOL_ARGS },
      delta: { id: "tool-1" },
      metadata: { name: "web_search" },
    },
  },
  {
    event: "tool_call",
    delay: 400,
    payload: {
      type: "tool_call",
      index: 1,
      status: "running",
      tool: { id: "tool-1", name: "web_search", args: TOOL_ARGS },
      tool_call: { id: "tool-1", name: "web_search", args: TOOL_ARGS },
      delta: { id: "tool-1" },
      metadata: { name: "web_search" },
    },
  },
  {
    event: "tool_end",
    delay: 400,
    payload: {
      type: "tool_end",
      index: 2,
      status: "completed",
      tool: {
        id: "tool-1",
        name: "web_search",
        args: TOOL_ARGS,
        result: "Paris",
        output: "Paris",
      },
      tool_call: {
        id: "tool-1",
        name: "web_search",
        args: TOOL_ARGS,
        result: "Paris",
      },
      delta: { id: "tool-1" },
      metadata: { name: "web_search", result: "Paris" },
    },
  },
  {
    event: "chunk",
    delay: 300,
    payload: makeChunk(3, "Searching the web gave us the answer: "),
  },
  { event: "chunk", delay: 200, payload: makeChunk(4, "Paris is the capital of France.") },
  {
    event: "done",
    delay: 150,
    payload: {
      ...DONE,
      content: "Searching the web gave us the answer: Paris is the capital of France.",
    },
  },
];

const LONG_LINE =
  "The quick brown fox jumps over the lazy dog near the river bank while " +
  "the sun sets over the hills and the wind carries the scent of pines. ";

const scrollParts = Array.from(
  { length: 36 },
  (_, i) => `${LONG_LINE}${LONG_LINE}—part ${i + 1}—`,
);

const scrollScript = [];
scrollScript.push({ event: "meta", payload: META });
for (let i = 0; i < 36; i += 1) {
  scrollScript.push({
    event: "chunk",
    delay: 60,
    payload: makeChunk(i, scrollParts[i]),
  });
}
// finalizeTurn replaces the streamed text with done.content when non-empty,
// so the done payload must carry the full streamed text (otherwise the
// rendered timeline collapses to the one-line summary).
scrollScript.push({
  event: "done",
  delay: 60,
  payload: { ...DONE, content: scrollParts.join("") },
});

const errorScript = [
  { event: "meta", payload: META },
  {
    event: "reasoning",
    payload: makeChunk(0, "Searching for the answer before replying...", "reasoning"),
  },
  { event: "chunk", delay: 300, payload: makeChunk(1, "The capital of France is ") },
  {
    event: "error",
    delay: 350,
    payload: {
      error: "stream interrupted by test",
      code: "INTERRUPTED",
      status: "interrupted",
    },
  },
];

const reasoningScript = [
  { event: "meta", payload: META },
  {
    event: "reasoning",
    payload: makeChunk(0, "Checking whether the user request needs a tool", "reasoning"),
  },
  {
    event: "reasoning",
    delay: 150,
    payload: makeChunk(1, "No tool needed, drafting the answer", "reasoning"),
  },
  {
    event: "reasoning",
    delay: 150,
    payload: makeChunk(2, "Writing the final answer now", "reasoning"),
  },
  { event: "chunk", delay: 400, payload: makeChunk(3, "The capital of France is Paris.") },
  { event: "done", delay: 150, payload: DONE },
];

function collectStrings(value, out) {
  if (typeof value === "string") {
    out.push(value);
  } else if (Array.isArray(value)) {
    for (const item of value) collectStrings(item, out);
  } else if (value !== null && typeof value === "object") {
    for (const key of Object.keys(value)) collectStrings(value[key], out);
  }
}

function pickScript(rawBody) {
  const strings = [];
  collectStrings(rawBody, strings);
  const haystack = strings.join("\n").toLowerCase();

  if (haystack.includes("tool")) return { name: "tool", script: toolScript };
  if (haystack.includes("scroll")) return { name: "scroll", script: scrollScript };
  if (haystack.includes("error")) return { name: "error", script: errorScript };
  if (haystack.includes("interrupt")) return { name: "interrupt", script: null };
  return { name: "reasoning", script: reasoningScript };
}

async function runChatScript(req, res, script, onAbort) {
  res.writeHead(200, {
    "Content-Type": "text/event-stream",
    "Cache-Control": "no-cache",
    Connection: "keep-alive",
    "X-Accel-Buffering": "no",
  });

  req.on("close", () => onAbort?.());

  for (const step of script) {
    if (step.delay > 0) await sleep(step.delay);
    // NOTE: req.destroyed flips to true as soon as the request body has been
    // consumed (Node autoDestroy); it does NOT mean the client disconnected.
    // Only bail out when the response side is actually gone.
    if (res.destroyed || req.socket?.destroyed || res.writableEnded) return;
    res.write(sseEvent(step.event, step.payload));
  }
  res.end();
}

async function runInterruptScript(req, res) {
  res.writeHead(200, {
    "Content-Type": "text/event-stream",
    "Cache-Control": "no-cache",
    Connection: "keep-alive",
    "X-Accel-Buffering": "no",
  });

  req.on("close", () => {
    process.stdout.write("[mock] interrupt script: client aborted stream\n");
  });

  res.write(sseEvent("meta", META));
  for (let i = 0; i < 100; i += 1) {
    await sleep(90);
    if (res.destroyed || req.socket?.destroyed || res.writableEnded) return;
    res.write(sseEvent("chunk", makeChunk(i, `Interruptible chunk ${i + 1}. `)));
  }
  res.end();
}

function writeJson(res, status, body) {
  const payload = JSON.stringify(body);
  res.writeHead(status, {
    "Content-Type": "application/json",
    "Content-Length": Buffer.byteLength(payload),
  });
  res.end(payload);
}

function readBody(req) {
  return new Promise((resolve) => {
    let raw = "";
    req.on("data", (chunk) => {
      raw += chunk;
    });
    req.on("end", () => {
      try {
        resolve(raw ? JSON.parse(raw) : {});
      } catch {
        resolve({ raw });
      }
    });
  });
}

const server = http.createServer(async (req, res) => {
  try {
    await handleRequest(req, res);
  } catch (error) {
    process.stdout.write(`[mock] request error: ${error?.stack ?? error}\n`);
    try {
      writeJson(res, 500, { error: "mock handler error", detail: String(error) });
    } catch {
      res.destroy();
    }
  }
});

async function handleRequest(req, res) {
  const url = new URL(req.url ?? "/", `http://${req.headers.host ?? "localhost"}`);
  const path = url.pathname;

  if (path === "/healthz") {
    writeJson(res, 200, { ok: true });
    return;
  }

  if (path === "/api/agent/chat" && req.method === "POST") {
    const body = await readBody(req);
    const selected = pickScript(body);
    process.stdout.write(`[mock] chat POST path=${path} script=${selected.name}\n`);
    if (selected.name === "interrupt") {
      await runInterruptScript(req, res);
      return;
    }
    await runChatScript(req, res, selected.script, () => {});
    return;
  }

  // --- static API stubs the workspace page loads on mount ---
  if (path === "/api/runtime/sessions" && req.method === "GET") {
    writeJson(res, 200, { sessions: [] });
    return;
  }
  if (path === "/api/runtime/sessions" && req.method === "POST") {
    writeJson(res, 200, {
      session: {
        session_id: "e2e-session-1",
        id: "e2e-session-1",
        title: "e2e session",
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      },
      session_id: "e2e-session-1",
    });
    return;
  }
  if (path === "/api/runtime/sessions/users") {
    writeJson(res, 200, { users: [] });
    return;
  }
  if (/^\/api\/runtime\/sessions\/[^/]+$/.test(path)) {
    writeJson(res, 200, {
      session: {
        session_id: "e2e-session-1",
        id: "e2e-session-1",
        title: "e2e session",
      },
    });
    return;
  }
  // --- artifact panel / checkpoint / backtrack / plan stubs ---
  if (/^\/api\/runtime\/sessions\/[^/]+\/checkpoints$/.test(path)) {
    writeJson(res, 200, { checkpoints: [], count: 0 });
    return;
  }
  if (/^\/api\/runtime\/sessions\/[^/]+\/checkpoints\/[^/]+\/files$/.test(path)) {
    writeJson(res, 200, { files: [], count: 0 });
    return;
  }
  if (/^\/api\/runtime\/sessions\/[^/]+\/checkpoints\/[^/]+\/preview$/.test(path)) {
    writeJson(res, 200, { result: { checkpoint_id: "e2e-checkpoint", mode: "both" } });
    return;
  }
  if (/^\/api\/runtime\/sessions\/[^/]+\/checkpoints\/[^/]+\/restore$/.test(path)) {
    writeJson(res, 200, { ok: true });
    return;
  }
  if (/^\/api\/runtime\/sessions\/[^/]+\/backtrack\/audit$/.test(path)) {
    writeJson(res, 200, { session_id: "e2e-session-1", entries: [], count: 0 });
    return;
  }
  if (/^\/api\/runtime\/sessions\/[^/]+\/backtrack(\/preview)?$/.test(path)) {
    writeJson(res, 200, { ok: true });
    return;
  }
  if (/^\/api\/runtime\/sessions\/[^/]+\/plan$/.test(path)) {
    writeJson(res, 200, {
      session_id: "e2e-session-1",
      active: false,
      status: "inactive",
      permission_mode: "default",
    });
    return;
  }
  if (/^\/api\/runtime\/sessions\/[^/]+\/history$/.test(path)) {
    writeJson(res, 200, { session_id: "e2e-session-1", history: [], count: 0 });
    return;
  }
  if (path === "/api/runtime/teams" && req.method === "GET") {
    writeJson(res, 200, { teams: [] });
    return;
  }
  if (path === "/api/runtime/teams/summary") {
    writeJson(res, 200, { teams: [] });
    return;
  }
  if (path === "/api/runtime/models") {
    writeJson(res, 200, { providers: [], default_provider: "", default_model: "" });
    return;
  }
  if (path === "/api/runtime/service") {
    writeJson(res, 200, { status: "running", healthy: true });
    return;
  }

  // Unknown API calls: answer an empty JSON object so page hooks that read
  // optional nested fields degrade gracefully.
  writeJson(res, 200, {});
}

server.listen(PORT, "127.0.0.1", () => {
  process.stdout.write(`[mock] e2e runtime server listening on 127.0.0.1:${PORT}\n`);
});

process.on("unhandledRejection", (reason) => {
  process.stdout.write(`[mock] unhandledRejection: ${reason?.stack ?? reason}\n`);
});
