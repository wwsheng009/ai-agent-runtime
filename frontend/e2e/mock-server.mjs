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

// --- 会话事件存储（模拟后端 EventStore 的 chat.sse.* 记录；P3-1/P3-2）---
// SSE 帧写出时同步记录；/runtime/sessions/:id/events 按 after/limit 查询，
// 并在 payload 注入持久化 seq（对齐后端 ListEvents 契约）。
const chatSseEventsBySession = new Map(); // sessionId -> [{type, payload, seq, timestamp}]
const mockSessions = new Map(); // sessionId -> {session_id, id, title, created_at, updated_at, history}
const brokenEventsSessions = new Set(); // e2e 故障开关：事件增量接口 500
let mockChatSeq = 0;

function recordChatSseEvent(sessionId, eventName, payload) {
  if (!sessionId) return undefined;
  const seq = ++mockChatSeq;
  const { _event, ...rest } = payload ?? {};
  let list = chatSseEventsBySession.get(sessionId);
  if (!list) {
    list = [];
    chatSseEventsBySession.set(sessionId, list);
  }
  list.push({
    type: `chat.sse.${eventName}`,
    payload: rest,
    seq,
    timestamp: new Date().toISOString(),
  });
  return seq;
}

/** 测试专用：向事件存储注入一条 runtime 生命周期事件（Q4 映射链路）。 */
function recordRuntimeTestEvent(sessionId, eventType, payload) {
  if (!sessionId) return undefined;
  const seq = ++mockChatSeq;
  let list = chatSseEventsBySession.get(sessionId);
  if (!list) {
    list = [];
    chatSseEventsBySession.set(sessionId, list);
  }
  list.push({
    type: eventType,
    payload: { ...(payload ?? {}), _source: "test" },
    seq,
    timestamp: new Date().toISOString(),
  });
  return seq;
}

function ensureMockSession(sessionId) {
  if (!sessionId) return null;
  let session = mockSessions.get(sessionId);
  if (!session) {
    const now = new Date().toISOString();
    session = {
      session_id: sessionId,
      id: sessionId,
      title: sessionId,
      created_at: now,
      updated_at: now,
      history: [],
    };
    mockSessions.set(sessionId, session);
  }
  return session;
}

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
  if (haystack.includes("burst")) return { name: "burst", script: burstScript };
  if (haystack.includes("scroll")) return { name: "scroll", script: scrollScript };
  if (haystack.includes("error")) return { name: "error", script: errorScript };
  if (haystack.includes("interrupt")) return { name: "interrupt", script: null };
  return { name: "reasoning", script: reasoningScript };
}

async function runChatScript(req, res, script, onAbort, sessionId) {
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
    // 与真实后端 Phase 0 契约一致：每帧携带 EventStore 持久化 seq
    // （事件先落存储再写帧；存储不可用时降级为连接内计数）。
    const seq = recordChatSseEvent(sessionId, step.event, step.payload) ?? 0;
    const payload = { ...step.payload, _event: { sequence: seq } };
    if (step.event === "done") {
      const session = ensureMockSession(sessionId);
      if (session) {
        session.history.push({
          role: "assistant",
          content: step.payload.content ?? step.payload.text ?? "",
        });
      }
    }
    res.write(sseEvent(step.event, payload));
  }
  res.end();
}

async function runInterruptScript(req, res, sessionId) {
  res.writeHead(200, {
    "Content-Type": "text/event-stream",
    "Cache-Control": "no-cache",
    Connection: "keep-alive",
    "X-Accel-Buffering": "no",
  });

  req.on("close", () => {
    process.stdout.write("[mock] interrupt script: client aborted stream\n");
  });

  const metaSeq = recordChatSseEvent(sessionId, "meta", META) ?? 1;
  res.write(sseEvent("meta", { ...META, _event: { sequence: metaSeq } }));
  for (let i = 0; i < 100; i += 1) {
    await sleep(90);
    if (res.destroyed || req.socket?.destroyed || res.writableEnded) return;
    const chunkSeq = recordChatSseEvent(sessionId, "chunk", makeChunk(i, `Interruptible chunk ${i + 1}. `)) ?? i + 2;
    res.write(
      sseEvent("chunk", {
        ...makeChunk(i, `Interruptible chunk ${i + 1}. `),
        _event: { sequence: chunkSeq },
      }),
    );
  }
  res.end();
}

// burst：1200 条 observation 事件（每条一个独立 structured Item）+ 少量工具调用，
// 用于轨迹视图 1000+ 行虚拟滚动断言（P2-3/P2-8）。
const burstScript = [];
burstScript.push({ event: "meta", payload: META });
for (let i = 0; i < 1200; i += 1) {
  burstScript.push({
    event: "observation",
    payload: { type: "observation", content: `observation-${i}`, source: "burst" },
  });
}
for (let i = 0; i < 10; i += 1) {
  const id = `burst-tool-${i}`;
  burstScript.push({
    event: "tool_start",
    payload: { type: "tool_call", tool_call: { id, name: "burst_tool" } },
  });
  burstScript.push({
    event: "tool_end",
    payload: {
      type: "tool_call",
      tool_call: { id, name: "burst_tool" },
      tool: { output_summary: `result-${i}` },
    },
  });
}
burstScript.push({ event: "done", delay: 1, payload: { ...DONE, content: "burst complete" } });

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

  // 测试注入（Q4）：POST /api/_test/runtime-events
  // body: { session_id, type, payload } → 与 chat.sse.* 共用同一 seq 序列。
  if (path === "/api/_test/runtime-events" && req.method === "POST") {
    const body = await readBody(req);
    const sessionId =
      typeof body?.session_id === "string" && body.session_id
        ? body.session_id
        : "e2e-session-1";
    const seq = recordRuntimeTestEvent(
      sessionId,
      typeof body?.type === "string" ? body.type : "approval_requested",
      typeof body?.payload === "object" && body.payload ? body.payload : {},
    );
    writeJson(res, 200, { seq });
    return;
  }

  if (path === "/api/agent/chat" && req.method === "POST") {
    const body = await readBody(req);
    const selected = pickScript(body);
    const sessionId =
      typeof body?.session_id === "string" && body.session_id
        ? body.session_id
        : "e2e-session-1";
    const session = ensureMockSession(sessionId);
    const userPrompt = body?.messages?.[0]?.content;
    if (session && typeof userPrompt === "string") {
      session.history.push({ role: "user", content: userPrompt });
    }
    process.stdout.write(`[mock] chat POST path=${path} script=${selected.name}\n`);
    if (selected.name === "interrupt") {
      await runInterruptScript(req, res, sessionId);
      return;
    }
    await runChatScript(req, res, selected.script, () => {}, sessionId);
    return;
  }

  // --- 会话 / 事件 / 历史端点（P3-1 恢复支撑；模拟后端 EventStore 查询）---
  // e2e 故障开关：对指定 session 的事件增量接口返回 500（模拟后端连接失败）。
  if (path === "/api/_mock/break-events" && req.method === "POST") {
    const breakBody = await readBody(req);
    const breakFor = String(breakBody?.session_id ?? "e2e-session-1");
    brokenEventsSessions.add(breakFor);
    process.stdout.write(`[mock] break-events enabled for ${breakFor}\n`);
    writeJson(res, 200, { ok: true, session_id: breakFor });
    return;
  }
  if (path === "/api/_mock/break-events" && req.method === "DELETE") {
    const clearBody = await readBody(req);
    if (clearBody?.session_id) {
      brokenEventsSessions.delete(String(clearBody.session_id));
    } else {
      brokenEventsSessions.clear();
    }
    writeJson(res, 200, { ok: true });
    return;
  }
  if (path === "/api/runtime/sessions" && req.method === "GET") {
    writeJson(res, 200, { sessions: [...mockSessions.values()] });
    return;
  }
  if (path === "/api/runtime/sessions" && req.method === "POST") {
    ensureMockSession("e2e-session-1");
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
  if (/^\/api\/runtime\/sessions\/[^/]+\/(?:runtime\/)?events$/.test(path) && req.method === "GET") {
    const sessionId = decodeURIComponent(path.split("/")[4]);
    if (brokenEventsSessions.has(sessionId)) {
      writeJson(res, 500, {
        error: "simulated events failure (e2e break-events)",
      });
      return;
    }
    const after = Number(url.searchParams.get("after") ?? "0") || 0;
    const rawLimit = url.searchParams.get("limit");
    const limit = rawLimit ? Number(rawLimit) || 0 : 0;
    const list = chatSseEventsBySession.get(sessionId) ?? [];
    const events = [];
    let latestSeq = 0;
    for (const entry of list) {
      if (entry.seq <= after) continue;
      latestSeq = Math.max(latestSeq, entry.seq);
      events.push({
        type: entry.type,
        payload: { ...entry.payload, seq: entry.seq },
        timestamp: entry.timestamp,
      });
      if (limit > 0 && events.length >= limit) break;
    }
    writeJson(res, 200, { events, count: events.length, latest_seq: latestSeq });
    return;
  }
  if (/^\/api\/runtime\/sessions\/[^/]+\/history$/.test(path) && req.method === "GET") {
    const sessionId = decodeURIComponent(path.split("/")[4]);
    const session = mockSessions.get(sessionId);
    const history = session?.history ?? [];
    writeJson(res, 200, { session_id: sessionId, count: history.length, history });
    return;
  }
  if (/^\/api\/runtime\/sessions\/[^/]+$/.test(path)) {
    const sessionId = decodeURIComponent(path.split("/")[3]);
    const session = mockSessions.get(sessionId);
    writeJson(res, 200, {
      session:
        session ?? {
          session_id: sessionId,
          id: sessionId,
          title: sessionId,
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
