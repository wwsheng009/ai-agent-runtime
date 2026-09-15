// zz-mount-audit-server：一次性诊断用的极小假后端（只服务 DOM remount 审计）。
//
// 职责：
//   * POST /api/agent/chat → text/event-stream，按固定节奏推 chunk，内容含 GFM 表格，
//     且表格「最后一个单元格」的文本在整条流里持续增长（用于验证逐帧 remount）。
//     请求体里出现 "zzplain" 时改推「纯文本对照」流（同样的前导块，尾块换成段落）。
//   * 其它 /api 路由 → 最小合法 JSON（catch-all 也返回合法 JSON 并记录日志）。
//
// 端口：默认 AUDIT_PORT=0（由操作系统分配空闲端口），启动后打印 `AUDIT_PORT=<port>`。
// 绝不代理到真实后端 8101，也绝不发起真实 LLM 调用。

import http from "node:http";

const PORT = Number(process.env.AUDIT_PORT ?? 0);
const HOST = process.env.AUDIT_HOST ?? "127.0.0.1";
const CHUNK_MS = Number(process.env.AUDIT_CHUNK_MS ?? 20);
const GROWTH_CHUNKS = Number(process.env.AUDIT_GROWTH_CHUNKS ?? 500);
const SESSION_ID = "zz-mount-audit-session";

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function sseEvent(event, payload) {
  return `event: ${event}\ndata: ${JSON.stringify(payload)}\n\n`;
}

function chunkPayload(index, text) {
  return {
    index,
    type: "text",
    content: text,
    text: { content: text, total_chars: text.length },
  };
}

function writeJson(res, status, body) {
  const raw = JSON.stringify(body);
  res.writeHead(status, {
    "Content-Type": "application/json; charset=utf-8",
    "Content-Length": Buffer.byteLength(raw),
    "Cache-Control": "no-store",
  });
  res.end(raw);
}

function readBody(req) {
  return new Promise((resolve) => {
    const parts = [];
    req.on("data", (part) => parts.push(part));
    req.on("end", () => {
      const raw = Buffer.concat(parts).toString("utf8");
      try {
        resolve(raw ? JSON.parse(raw) : {});
      } catch {
        resolve({ _raw: raw });
      }
    });
    req.on("error", () => resolve({}));
  });
}

// 会话历史：turn 收尾的 `done` 帧才写库（与 mock-server / 后端同口径），
// 保证 `done` 之后前端做 history 投影时仍能拿到完整正文。
const historyBySession = new Map();

function sessionHistory(sessionId) {
  if (!historyBySession.has(sessionId)) historyBySession.set(sessionId, []);
  return historyBySession.get(sessionId);
}

// --- 流内容构造 -------------------------------------------------------------

const PRELUDE = [
  "# Streaming mount audit",
  "",
  "Paragraph one is stable prose that must not re-render while the tail grows.",
  "",
  "```ts",
  "const answer = compute(41);",
  "```",
  "",
  "- list item one",
  "- list item two",
  "",
].join("\n");

const TABLE_HEAD = ["| Metric | Value |", "| --- | --- |", "| rows | 2 |"].join("\n") + "\n";

// 同一张 GFM 表格，但分隔行带冒号（`:---` / `---:`）。
// 目的：`parseTableAlignmentCell` 对「不带冒号的 `---`」返回 null（前端判定对齐行非法），
// 会让流式表格走不到 `streaming-blocks.tsx` 的结构化渲染分支；带冒号才会命中该分支。
const TABLE_HEAD_ALIGNED = ["| Metric | Value |", "| :--- | ---: |", "| rows | 2 |"].join("\n") + "\n";

const GROWTH_START = "| growth | token0000";

function buildFrames(mode) {
  // 前导块拆成几帧发出（模拟真实 LLM 的分块到达）。
  const preludeFrames = [
    "# Streaming mount audit\n\n",
    "Paragraph one is stable prose that must not re-render while the tail grows.\n\n",
    "```ts\nconst answer = compute(41);\n```\n\n",
    "- list item one\n",
    "- list item two\n\n",
  ];
  const frames = [];
  for (const text of preludeFrames) {
    frames.push(text);
  }
  if (mode === "table" || mode === "table_stream") {
    frames.push(mode === "table_stream" ? TABLE_HEAD_ALIGNED : TABLE_HEAD);
    frames.push("| growth | token0000");
  } else {
    frames.push("The trailing paragraph keeps growing: token0000");
  }
  return frames;
}

function growthToken(i) {
  return ` token${String(1000 + i).slice(-4)}`;
}

async function runChatStream(req, res, mode, sessionId) {
  const frames = buildFrames(mode);
  res.writeHead(200, {
    "Content-Type": "text/event-stream",
    "Cache-Control": "no-cache",
    Connection: "keep-alive",
    "X-Accel-Buffering": "no",
  });

  let alive = true;
  let aborted = false;
  res.on("close", () => {
    alive = false;
    aborted = !res.writableEnded;
  });

  const write = (event, payload) => {
    if (!alive) return false;
    res.write(sseEvent(event, payload));
    return true;
  };

  write("meta", {
    session_id: SESSION_ID,
    agent_id: "zz-audit-agent",
    model: "zz-audit-no-llm",
    kind: "chat",
    status: "started",
  });

  let index = 0;
  let full = "";
  for (const text of frames) {
    if (!alive) break;
    await sleep(40);
    full += text;
    write("chunk", chunkPayload(index, text));
    index += 1;
    stats.chunks += 1;
  }

  // 关键段：只往「表格最后一个单元格」追加文本（无换行、无 `|`），
  // 因此整个尾块始终是同一个合法表格，但单元格内容逐帧变化。
  for (let i = 0; i < GROWTH_CHUNKS; i += 1) {
    if (!alive) break;
    await sleep(CHUNK_MS);
    const text = growthToken(i);
    full += text;
    write("chunk", chunkPayload(index, text));
    index += 1;
    stats.chunks += 1;
  }

  if (alive) {
    write("done", {
      session_id: sessionId,
      agent_id: "zz-audit-agent",
      status: "completed",
      content: full,
      result: {
        usage: { prompt_tokens: 11, completion_tokens: 22, total_tokens: 33 },
      },
    });
    res.end();
    sessionHistory(sessionId).push({ role: "assistant", content: full });
  }
  stats.streamFinishedAt = Date.now();
  stats.lastAborted = aborted;
  stats.frames = index;
  process.stdout.write(
    `[zz-audit-server] stream mode=${mode} frames=${index} aborted=${aborted}\n`,
  );
}

// --- 路由 -------------------------------------------------------------------

const stats = {
  chunks: 0,
  frames: 0,
  chatRequests: 0,
  requests: [],
  lastAborted: false,
  streamFinishedAt: 0,
};

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url ?? "/", `http://${HOST}`);
  const path = url.pathname;
  stats.requests.push(`${req.method} ${path}`);

  if (path === "/healthz") {
    writeJson(res, 200, { ok: true });
    return;
  }

  if (path === "/__stats") {
    writeJson(res, 200, stats);
    return;
  }

  if (path === "/__reset") {
    stats.chunks = 0;
    stats.frames = 0;
    stats.chatRequests = 0;
    stats.requests = [];
    writeJson(res, 200, { ok: true });
    return;
  }

  if (path === "/api/agent/chat" && req.method === "POST") {
    const body = await readBody(req);
    stats.chatRequests += 1;
    const haystack = JSON.stringify(body ?? {}).toLowerCase();
    const mode = haystack.includes("zzstream")
      ? "table_stream"
      : haystack.includes("zzplain")
        ? "plain"
        : "table";
    stats.mode = mode;
    const sessionId =
      (typeof body?.session_id === "string" && body.session_id) || SESSION_ID;
    const history = sessionHistory(sessionId);
    const messages = Array.isArray(body?.messages) ? body.messages : [];
    const lastUser = [...messages]
      .reverse()
      .find((message) => message && message.role === "user");
    if (lastUser && typeof lastUser.content === "string") {
      history.push({ role: "user", content: lastUser.content });
    }
    await runChatStream(req, res, mode, sessionId);
    return;
  }

  // 运行时事件流：审计期间不需要任何历史事件，直接开流并立即收尾。
  if (/^\/api\/runtime\/sessions\/[^/]+\/runtime\/stream$/.test(path)) {
    res.writeHead(200, {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-cache",
      Connection: "keep-alive",
    });
    res.end();
    return;
  }

  if (path === "/api/runtime/sessions" && req.method === "GET") {
    writeJson(res, 200, {
      sessions: [
        {
          session_id: SESSION_ID,
          id: SESSION_ID,
          title: "zz mount audit",
          state: "active",
          created_at: "2026-09-15T00:00:00Z",
          updated_at: "2026-09-15T00:00:00Z",
        },
      ],
      count: 1,
    });
    return;
  }

  if (path === "/api/runtime/sessions" && req.method === "POST") {
    const body = await readBody(req);
    const id =
      (typeof body?.session_id === "string" && body.session_id) ||
      (typeof body?.id === "string" && body.id) ||
      SESSION_ID;
    writeJson(res, 200, { ok: true, session_id: id, id });
    return;
  }

  if (/^\/api\/runtime\/sessions\/[^/]+\/history$/.test(path)) {
    const sessionId = decodeURIComponent(path.split("/")[4]);
    const history = sessionHistory(sessionId);
    writeJson(res, 200, {
      session_id: sessionId,
      count: history.length,
      history,
    });
    return;
  }

  if (/^\/api\/runtime\/sessions\/[^/]+\/runtime$/.test(path)) {
    writeJson(res, 200, {
      state: { status: "idle", pending_interactions: [], agents: [] },
      execution_route: "zz-mount-audit",
    });
    return;
  }

  if (/^\/api\/runtime\/sessions\/[^/]+\/runtime\/events$/.test(path)) {
    writeJson(res, 200, { events: [], count: 0, latest_seq: 0 });
    return;
  }

  // 以下形状照抄 mock-server.mjs（前端会直接读这些字段，缺字段会打穿错误边界）。
  if (path === "/api/runtime/models") {
    writeJson(res, 200, {
      providers: [],
      count: 0,
      default_provider: "",
      default_model: "",
    });
    return;
  }

  if (path === "/api/runtime/teams" || path === "/api/runtime/teams/summary") {
    writeJson(res, 200, { teams: [] });
    return;
  }

  if (path === "/api/runtime/sessions/users") {
    writeJson(res, 200, {
      users: [],
      count: 0,
      total_count: 0,
      default_user_id: undefined,
    });
    return;
  }

  if (path === "/api/runtime/sessions/stats" && req.method === "GET") {
    writeJson(res, 200, {
      user_id: "default",
      stats: {
        total: 1,
        active: 1,
        idle: 0,
        closed: 0,
        archived: 0,
        totalMessages: 0,
        tags: {},
      },
    });
    return;
  }

  if (path === "/api/runtime/workspace-directories" && req.method === "GET") {
    writeJson(res, 200, { directories: [], count: 0 });
    return;
  }

  if (/^\/api\/runtime\/sessions\/[^/]+$/.test(path)) {
    const sessionId = decodeURIComponent(path.split("/")[4]);
    writeJson(res, 200, {
      session: { session_id: sessionId, id: sessionId, title: "zz mount audit" },
    });
    return;
  }

  // 兜底与 mock-server 同口径：未知 /api/* 返回空对象，让可选字段自然降级。
  if (path.startsWith("/api/")) {
    writeJson(res, 200, {});
    return;
  }

  writeJson(res, 404, { error: "not found", path });
});

server.listen(PORT, HOST, () => {
  const address = server.address();
  const port = typeof address === "object" && address ? address.port : PORT;
  process.stdout.write(`AUDIT_PORT=${port}\n`);
  process.stdout.write(`[zz-audit-server] listening on ${HOST}:${port}\n`);
});
