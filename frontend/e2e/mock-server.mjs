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

import { createProfilesMock } from "./mock-profiles.mjs";

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
  // P1-1：Turn token 用量（完整：输入/输出/合计）——渲染层显示用量行。
  result: {
    usage: { prompt_tokens: 1234, completion_tokens: 567, total_tokens: 1801 },
  },
};

// --- 会话事件存储（模拟后端 EventStore 的 chat.sse.* 记录；P3-1/P3-2）---
// SSE 帧写出时同步记录；/runtime/sessions/:id/events 按 after/limit 查询，
// 并在 payload 注入持久化 seq（对齐后端 ListEvents 契约）。
const chatSseEventsBySession = new Map(); // sessionId -> [{type, payload, seq, timestamp}]
const mockSessions = new Map(); // sessionId -> {session_id, id, title, created_at, updated_at, history}
const brokenEventsSessions = new Set(); // e2e 故障开关：事件增量接口 500
const mockJobsBySession = new Map(); // sessionId -> [{...backend background.Job（PascalCase）}]
// P2-1A：运行时文件读取（`POST /api/runtime/fs/read-file`）的 mock 文件表。
// path -> { dataBase64, byteCount }；由 `/api/_test/files` 注入，未登记即 404。
const mockFiles = new Map();

// Batch 13 slice 9：profiles 域（列表 / 生命周期 / 导入导出 / 会话内切换）。
// 契约与纪律见 mock-profiles.mjs 顶部注释；状态由 `/api/_test/profiles` 注入、
// `/api/_test/reset` 清空。helper 按请求注入，避免模块求值顺序依赖。
const profilesMock = createProfilesMock();

// P0-P4：右侧栏「文件浏览器 / Git 变更面」夹具（/api/runtime/fs/*、/api/runtime/git/*）。
// 夹具刻意做大（根层 83 项 / 60 个变更）：滚动条缺失与「预览区被挤出可视范围」这类缺陷
// 只在内容超出容器时显形，而 jsdom 不计算布局 —— 这里是唯一的真实浏览器回归面。
const E2E_FS_SCOPE = "session:e2e-workspace";
const E2E_FS_ROOT_PATH = "E:/workspace/e2e";
const E2E_PREVIEW_MARKER = "E2E_PREVIEW_TEXT_OK";
// mtime 一律 Unix 秒（后端 `info.ModTime().Unix()`）：前端曾把秒当毫秒 → 恒显示 1970-01-21。
const E2E_MTIME_SECONDS = 1758000000;
const E2E_TREE_ENTRY_COUNT = 80;
const E2E_CHANGED_FILE_COUNT = 60;

function mockFsTreeEntries() {
  const entries = [
    { name: "src", path: "src", type: "dir", size: 0, mtime: E2E_MTIME_SECONDS },
    {
      name: "README.md",
      path: "README.md",
      type: "file",
      size: 512,
      mtime: E2E_MTIME_SECONDS,
      ext: "md",
      is_text: true,
    },
  ];
  for (let index = 1; index <= E2E_TREE_ENTRY_COUNT; index += 1) {
    const name = `entry-${String(index).padStart(3, "0")}.txt`;
    entries.push({
      name,
      path: name,
      type: "file",
      size: 1024 + index,
      mtime: E2E_MTIME_SECONDS,
      ext: "txt",
      is_text: true,
    });
  }
  entries.push({
    name: "notes.txt",
    path: "notes.txt",
    type: "file",
    size: 96,
    mtime: E2E_MTIME_SECONDS,
    ext: "txt",
    is_text: true,
  });
  return entries;
}

// P1：全库搜索夹具（`/api/runtime/fs/search`）。刻意含**子目录**文件（证明是跨目录检索，
// 而不是只回根层），并按名称子串命中（`match` 给 rune 偏移供高亮；仅 ASCII，偏移即索引）。
const E2E_SEARCH_ITEMS = [
  {
    name: "notes.txt",
    path: "notes.txt",
    type: "file",
    size: 96,
    mtime: E2E_MTIME_SECONDS,
    ext: "txt",
    score: 150,
  },
  {
    name: "composer-menu.ts",
    path: "src/lib/composer-menu.ts",
    type: "file",
    size: 4096,
    mtime: E2E_MTIME_SECONDS,
    ext: "ts",
    score: 120,
  },
  {
    name: "composer-menu.test.ts",
    path: "src/lib/composer-menu.test.ts",
    type: "file",
    size: 3072,
    mtime: E2E_MTIME_SECONDS,
    ext: "ts",
    score: 110,
  },
  { name: "src", path: "src", type: "dir", size: 0, mtime: E2E_MTIME_SECONDS, score: 60 },
];

function mockFsSearchMatches(kinds, query) {
  const needle = query.trim().toLowerCase();
  const matched = [];
  for (const item of E2E_SEARCH_ITEMS) {
    if (kinds === "file" && item.type !== "file") {
      continue;
    }
    if (kinds === "dir" && item.type !== "dir") {
      continue;
    }
    const name = item.name.toLowerCase();
    const path = item.path.toLowerCase();
    if (needle && !name.includes(needle) && !path.includes(needle)) {
      continue;
    }
    const start = needle ? name.indexOf(needle) : -1;
    matched.push({
      ...item,
      ...(start >= 0 ? { match: { field: "name", start, end: start + needle.length } } : {}),
    });
  }
  return matched;
}

// P2-7 子片 3：运行时模型目录（`GET /api/runtime/models`）。默认空目录，由
// `/api/_test/models` 注入；前端只据这份目录组装 `/model` 候选，未注入即无候选。
let mockRuntimeModelsCatalog = null;
// P2-1A：技能市场（`/api/runtime/skills/*`）状态。契约对齐
// backend/internal/api/skills/handler.go：写操作要求管理令牌；热重载未配置时
// stats 端点返回 503，前端如实呈现「未启用」而不是伪造 watching=false。
const MOCK_SKILLS_ADMIN_TOKEN = "e2e-skills-token";
let mockSkillsEmbeddingEnabled = false;
let mockSkillsHotReload = {
  configured: false,
  watching: false,
  skillCount: 0,
  callbackCount: 0,
  debounceTime: "",
};
let mockChatSeq = 0;
let mockCreatedSessionSeq = 0; // POST 建会话（含 Fork）时生成确定性新 id

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

/**
 * 由会话事件（+ 后台任务）推导 runtime 快照，供
 * `GET /api/runtime/sessions/:id/runtime`（P2-1A 重载恢复）使用。
 *
 * 只在 mock 内维护「最后一个未决审批 / 提问」：与后端快照同语义（snake_case），
 * 供前端 P1-7 待交互注册表在页面重载后重建。
 */
function deriveSessionRuntimeState(sessionId) {
  const list = chatSseEventsBySession.get(sessionId) ?? [];
  let approval = null;
  let question = null;
  let latestSeq = 0;
  let updatedAt = "";

  for (const entry of list) {
    latestSeq = Math.max(latestSeq, entry.seq ?? 0);
    if (entry.timestamp) updatedAt = entry.timestamp;
    const payload = entry.payload ?? {};
    const type = String(entry.type ?? "").toLowerCase();
    if (type === "approval_requested" || type === "approval.requested") {
      const requestId = payload.request_id ?? payload.requestId ?? payload.id;
      if (typeof requestId === "string" && requestId) {
        approval = {
          request_id: requestId,
          tool_name: payload.tool_name ?? payload.toolName ?? "",
          reason: payload.reason ?? "",
          risk_level: payload.risk_level ?? payload.riskLevel ?? "",
          ...(typeof payload.expires_at === "string" && payload.expires_at
            ? { expires_at: payload.expires_at }
            : {}),
        };
      }
      continue;
    }
    if (type === "approval_resolved" || type === "approval.resolved") {
      const requestId = payload.request_id ?? payload.requestId;
      if (!requestId || !approval || approval.request_id === requestId) {
        approval = null;
      }
      continue;
    }
    if (type === "question_asked" || type === "question.asked") {
      const questionId = payload.question_id ?? payload.questionId ?? payload.id;
      if (typeof questionId === "string" && questionId) {
        question = {
          question_id: questionId,
          prompt: payload.prompt ?? "",
          required: payload.required === true,
          suggestions: Array.isArray(payload.suggestions) ? payload.suggestions : [],
          ...(typeof payload.expires_at === "string" && payload.expires_at
            ? { expires_at: payload.expires_at }
            : {}),
        };
      }
      continue;
    }
    if (type === "question_answered" || type === "question.answered") {
      const questionId = payload.question_id ?? payload.questionId;
      if (!questionId || !question || question.question_id === questionId) {
        question = null;
      }
    }
  }

  const activeJobIds = (mockJobsBySession.get(sessionId) ?? [])
    .filter((job) =>
      ["running", "pending", "queued"].includes(String(job.Status ?? "").toLowerCase()),
    )
    .map((job) => job.ID)
    .filter((id) => typeof id === "string" && id);

  return {
    session_id: sessionId,
    status: approval ? "waiting_approval" : question ? "waiting_question" : "idle",
    head_offset: latestSeq,
    active_job_ids: activeJobIds,
    pending_approval: approval,
    pending_question: question,
    updated_at: updatedAt || new Date().toISOString(),
  };
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
    // 400ms 的 Running 窗口在负载下会被 React 批处理合并，G2 断言
    // Started→Running→Finished 会间歇性看不到中间态；给足可观测窗口。
    delay: 900,
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

// P2-1A：file 预览链路的最小会话（read_file 工具行 → 行内文件链接 → 文件预览弹层）。
// 触发词：prompt 含 "read-file"。路径由 /api/_test/files 注入，未注入则弹层如实报 404。
const READ_FILE_PATH = "/workspace/e2e/notes.txt";

// P2-1A 修正：工具行路径也可能是「相对会话工作目录」的写法（agent 的常见输出）。
// 前端必须先按 `/fs/roots?session_id=` 给出的会话根解析成绝对路径再读，这里按解析结果注入文件：
// `notes/relative.txt` → `${E2E_FS_ROOT_PATH}/notes/relative.txt`。
// 触发词：prompt 含 "read-file-rel"（匹配顺序必须早于 "read-file"）。
const READ_FILE_RELATIVE_PATH = "notes/relative.txt";

// P2-1A 扩展：Markdown 文本文件的预览页签（原始 / Markdown 预览）。
// 触发词：prompt 含 "read-file-md"（匹配顺序必须早于泛化的 "read-file"）。
const READ_FILE_MARKDOWN_PATH = "/workspace/e2e/readme.md";

function buildReadFileScript(targetPath) {
  const args = { file_path: targetPath };
  return [
    { event: "meta", payload: META },
    {
      event: "tool_start",
      delay: 120,
      payload: {
        type: "tool_start",
        index: 0,
        status: "started",
        tool: { id: "read-1", name: "read_file", args },
        tool_call: { id: "read-1", name: "read_file", args },
        delta: { id: "read-1" },
        metadata: { name: "read_file" },
      },
    },
    {
      event: "tool_end",
      delay: 200,
      payload: {
        type: "tool_end",
        index: 1,
        status: "completed",
        tool: {
          id: "read-1",
          name: "read_file",
          args,
          result: "read 2 lines",
          output: "read 2 lines",
        },
        tool_call: { id: "read-1", name: "read_file", args, result: "read 2 lines" },
        delta: { id: "read-1" },
        metadata: { name: "read_file", result: "read 2 lines" },
      },
    },
    { event: "chunk", delay: 120, payload: makeChunk(2, "I read the file you pointed at.") },
    {
      event: "done",
      delay: 120,
      payload: { ...DONE, content: "I read the file you pointed at." },
    },
  ];
}

const readFileScript = buildReadFileScript(READ_FILE_PATH);
const readFileRelativeScript = buildReadFileScript(READ_FILE_RELATIVE_PATH);
const readFileMarkdownScript = buildReadFileScript(READ_FILE_MARKDOWN_PATH);

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

// G1 用：批次 B2 后推理展开区是 Markdown 面板，回合 done 会随历史刷新卸载该行，
// 因此把推理块间隔放大，保证「展开 → 读到全文」的断言落在稳定的流式窗口内。
const reasoningHoldScript = [
  { event: "meta", payload: META },
  {
    event: "reasoning",
    payload: makeChunk(0, "Checking whether the user request needs a tool", "reasoning"),
  },
  {
    event: "reasoning",
    delay: 900,
    payload: makeChunk(1, "No tool needed, drafting the answer", "reasoning"),
  },
  {
    event: "reasoning",
    delay: 900,
    payload: makeChunk(2, "Writing the final answer now", "reasoning"),
  },
  { event: "chunk", delay: 900, payload: makeChunk(3, "The capital of France is Paris.") },
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
  if (haystack.includes("read-file-rel"))
    return { name: "read-file-relative", script: readFileRelativeScript };
  if (haystack.includes("read-file-md"))
    return { name: "read-file-md", script: readFileMarkdownScript };
  if (haystack.includes("read-file")) return { name: "read-file", script: readFileScript };
  if (haystack.includes("burst")) return { name: "burst", script: burstScript };
  if (haystack.includes("perfmark")) return { name: "perfmark", script: perfMarkScript };
  if (haystack.includes("scroll")) return { name: "scroll", script: scrollScript };
  if (haystack.includes("error")) return { name: "error", script: errorScript };
  if (haystack.includes("interrupt")) return { name: "interrupt", script: null };
  if (haystack.includes("hold")) return { name: "reasoning-hold", script: reasoningHoldScript };
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

// perfmark（临时诊断脚本，keyword=`perfmark`）：贴近真实「长回答 + 高频 token 流」的
// 压力场景——约 21KB markdown（散文 + 围栏代码块 + 列表 + 表格）切成 40 字符增量、
// 10ms 一帧，共约 525 帧 / 5.3s。用于 CDP Profiler 定位流式期间的主线程开销。
const PERF_SENTENCES = [
  "The runtime keeps a bounded event window per session. ",
  "Each frame carries a monotonically increasing sequence. ",
  "Delivery is at-least-once, so the client merges by sequence. ",
  "The renderer freezes completed markdown blocks by absolute offset. ",
  "Only the tail block is reparsed while a response is streaming. ",
];
const PERF_CODE_BLOCK = [
  "```ts\n",
  "export function reveal(target: string, shown: number, step: number) {\n",
  "  const next = Math.min(target.length, shown + step);\n",
  "  return target.slice(0, next);\n",
  "}\n",
  "```\n\n",
];
const PERF_LIST = "- first item\n- second item\n- third item\n\n";
const PERF_TABLE = "| seq | kind |\n| --- | --- |\n| 1 | delta |\n| 2 | done |\n\n";
// 文档规模可控（PERF_DOC_ROUNDS），用于验证「每帧开销是否随正文长度增长」。
const PERF_DOC_ROUNDS = Number(process.env.PERF_DOC_ROUNDS ?? "40");
const perfMarkDoc = (() => {
  const out = [];
  for (let round = 0; round < PERF_DOC_ROUNDS; round += 1) {
    out.push(...PERF_SENTENCES, "\n\n", ...PERF_CODE_BLOCK, PERF_LIST, PERF_TABLE);
  }
  out.push("PERFMARK-END\n");
  return out.join("");
})();

const perfMarkScript = [{ event: "meta", payload: META }];
{
  const PERF_DELTA_CHARS = 40;
  let index = 0;
  for (let offset = 0; offset < perfMarkDoc.length; offset += PERF_DELTA_CHARS) {
    perfMarkScript.push({
      event: "chunk",
      delay: 10,
      payload: makeChunk(index, perfMarkDoc.slice(offset, offset + PERF_DELTA_CHARS)),
    });
    index += 1;
  }
  perfMarkScript.push({
    event: "done",
    delay: 10,
    payload: { ...DONE, content: perfMarkDoc },
  });
}

// --- 用量 / 配额 mock 数据（P2-1A 用量面板）---
// 键名与后端 handler 直接构造的 map 一致（snake_case）；
// 全局视图给出 scope 列表，指定作用域后给出 quota（tenant-a 故意不配上限）。
function mockUsagePolicySummary() {
  return {
    tracking_enabled: true,
    ledger_enabled: true,
    quota_enabled: true,
    default_max_requests: 100,
    default_max_tokens: 50000,
    tenant_quota_count: 1,
    project_quota_count: 0,
    user_quota_count: 1,
  };
}

function mockUsageScopes() {
  return [
    { tenant_id: "", project_id: "", user_id: "alice", scope_key: "alice" },
    { tenant_id: "tenant-a", project_id: "", user_id: "", scope_key: "tenant-a" },
  ];
}

function mockGlobalUsage() {
  return {
    scope_count: 2,
    user_count: 1,
    request_count: 9,
    execute_count: 6,
    agent_chat_count: 3,
    success_count: 8,
    failure_count: 1,
    prompt_tokens: 7000,
    completion_tokens: 3000,
    total_tokens: 10000,
    last_skill: "ledger-skill",
    last_entrypoint: "execute",
    last_request_at: "2026-09-13T10:00:00Z",
  };
}

function mockScopedUsage() {
  return {
    ...mockGlobalUsage(),
    scope_count: 0,
    user_count: 0,
    request_count: 3,
    total_tokens: 1200,
  };
}

function mockUsageQuota(scopeKey) {
  if (scopeKey !== "alice") {
    // 未配置上限：如实返回 null，由前端展示「不可用」，不伪造余量。
    return null;
  }
  return {
    scope_key: "alice",
    enabled: true,
    max_requests: 100,
    max_tokens: 50000,
    remaining_requests: 91,
    remaining_tokens: 41500,
    resolved_from: "user",
  };
}

function mockUsageLedgerRecords() {
  const nilUUID = "00000000-0000-0000-0000-000000000000";
  return [
    {
      id: "e2e-usage-1",
      request_id: "e2e-req-1",
      model_id: nilUUID,
      provider_id: nilUUID,
      input_tokens: 900,
      output_tokens: 300,
      total_tokens: 1200,
      message_count: 3,
      max_tokens: 50000,
      success: true,
      status_code: 200,
      metadata: { entrypoint: "execute", skill: "ledger-skill", scope_key: "alice" },
      created_at: "2026-09-13T10:00:00Z",
    },
    {
      id: "e2e-usage-2",
      request_id: "e2e-req-2",
      model_id: nilUUID,
      provider_id: nilUUID,
      input_tokens: 100,
      output_tokens: 0,
      total_tokens: 100,
      message_count: 1,
      max_tokens: 0,
      success: false,
      status_code: 500,
      metadata: { entrypoint: "agent_chat" },
      created_at: "2026-09-13T09:00:00Z",
    },
  ];
}

// --- 技能市场 mock 数据（P2-1A）---
// 目录里刻意留一条「缺 name」的坏条目：后端可能这样上报，前端按约定丢弃该条，
// 但 `count` 保留后端上报值（e2e 据此断言「不按数组长度改写」）。
function mockSkillEntries() {
  return [
    {
      name: "code-review",
      description: "Review a diff and report correctness risks.",
      version: "1.2.0",
      category: "quality",
      capabilities: ["static-analysis"],
      tags: ["review", "quality"],
      triggers: [{ type: "keyword", values: ["review", "diff"], weight: 0.9 }],
      tools: ["read_file", "grep"],
      systemPrompt: "You review code changes and report risks.",
      userPrompt: "Review the diff and summarise risks.",
      workflow: {
        steps: [
          {
            id: "scan",
            name: "Scan diff",
            tool: "read_file",
            args: { path: "diff.patch" },
            dependsOn: [],
            condition: "",
          },
          {
            id: "report",
            name: "Report findings",
            tool: "grep",
            args: {},
            dependsOn: ["scan"],
            condition: "scan.ok",
          },
        ],
      },
      context: { files: ["docs/review.md"], environment: [], symbols: [] },
      permissions: ["read"],
      source: {
        path: "D:/skills/code-review/SKILL.md",
        dir: "D:/skills",
        layer: "project",
        prompt_path: "D:/skills/code-review/prompt.md",
      },
    },
    {
      name: "docs-writer",
      description: "Draft and update project documentation.",
      version: "0.4.1",
      category: "docs",
      capabilities: [],
      tags: ["docs"],
      triggers: [{ type: "keyword", values: ["docs"], weight: null }],
      tools: ["read_file"],
      systemPrompt: "",
      userPrompt: "Draft the requested documentation.",
      workflow: { steps: [] },
      context: { files: [], environment: [], symbols: [] },
      permissions: [],
      source: {
        path: "D:/skills/docs-writer/SKILL.md",
        dir: "D:/skills",
        layer: "project",
        prompt_path: "",
      },
    },
    { description: "entry without a name" },
  ];
}

/** 目录载荷：`{skills, count}`；count 故意上报 3（含坏条目），与数组长度不同。 */
function mockSkillCatalogPayload() {
  const entries = mockSkillEntries();
  return { skills: entries, count: entries.length };
}

function mockHotReloadStatsPayload() {
  return {
    enabled: mockSkillsHotReload.configured,
    watching: mockSkillsHotReload.watching,
    skillDir: "D:/skills",
    skillDirs: ["D:/skills", "E:/shared/skills"],
    skillCount: mockSkillsHotReload.skillCount,
    callbackCount: mockSkillsHotReload.callbackCount,
    debounceTime: mockSkillsHotReload.debounceTime,
  };
}

/** 写操作鉴权：与后端一致，接受 X-Skills-Admin-Token 或 Bearer admin token。 */
function mockSkillsAuthorized(req) {
  const header = String(req.headers["x-skills-admin-token"] ?? "").trim();
  if (header && header === MOCK_SKILLS_ADMIN_TOKEN) {
    return true;
  }
  const authorization = String(req.headers.authorization ?? "").trim();
  const bearer = authorization.toLowerCase().startsWith("bearer ")
    ? authorization.slice(7).trim()
    : "";
  return bearer === MOCK_SKILLS_ADMIN_TOKEN;
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

/** /logs 页固定日志条目（字段对齐 RuntimeLogEntry）。 */
const MOCK_RUNTIME_LOGS = [
  {
    cursor: 42,
    raw_text: "runtime-server listening on 0.0.0.0:8101",
    timestamp: "2026-09-14T10:00:00Z",
    level: "info",
    module: "runtime-server",
    message: "runtime-server listening on 0.0.0.0:8101",
  },
  {
    cursor: 41,
    raw_text: "provider call failed: upstream timeout",
    timestamp: "2026-09-14T09:59:58Z",
    level: "error",
    module: "provider",
    message: "provider call failed: upstream timeout",
  },
];

async function handleRequest(req, res) {
  const url = new URL(req.url ?? "/", `http://${req.headers.host ?? "localhost"}`);
  const path = url.pathname;

  if (path === "/healthz") {
    writeJson(res, 200, { ok: true });
    return;
  }

  // profiles 域先接管（含 `/api/runtime/sessions/{id}/runtime/commands` 的
  // set_profile 分支）：未命中即返回 false，后续路由不受影响。
  if (await profilesMock.handle(req, res, { path, url, readBody, writeJson })) {
    return;
  }

  // 测试隔离：POST /api/_test/reset
  // mock server 由 playwright webServer 跨整个 run 共享（workers=1），会话历史、
  // 事件存储与故障开关都会残留到后续 spec。每个用例开始前显式清空，避免
  // e2e-session-1 的历史逐轮累积（例如工作区时间线出现多份同一条回答）。
  if (path === "/api/_test/reset" && req.method === "POST") {
    mockSessions.clear();
    chatSseEventsBySession.clear();
    brokenEventsSessions.clear();
    mockJobsBySession.clear();
    mockFiles.clear();
    profilesMock.reset();
    mockRuntimeModelsCatalog = null;
    mockSkillsEmbeddingEnabled = false;
    mockSkillsHotReload = {
      configured: false,
      watching: false,
      skillCount: 0,
      callbackCount: 0,
      debounceTime: "",
    };
    mockChatSeq = 0;
    mockCreatedSessionSeq = 0;
    writeJson(res, 200, { ok: true });
    return;
  }

  // 测试注入（Q4）：POST /api/_test/runtime-events
  // body: { session_id, type, payload }（单条，兼容旧调用）
  //     或 { session_id, events: [{ type, payload }] }（批量，seedRuntimeEvents）→
  // 与 chat.sse.* 共用同一 seq 序列；返回最后一条的 seq 与全部 seqs。
  if (path === "/api/_test/runtime-events" && req.method === "POST") {
    const body = await readBody(req);
    const sessionId =
      typeof body?.session_id === "string" && body.session_id
        ? body.session_id
        : "e2e-session-1";
    const events = Array.isArray(body?.events)
      ? body.events
      : [{ type: body?.type, payload: body?.payload }];
    const seqs = events.map((event) =>
      recordRuntimeTestEvent(
        sessionId,
        typeof event?.type === "string" ? event.type : "approval_requested",
        typeof event?.payload === "object" && event.payload ? event.payload : {},
      ),
    );
    writeJson(res, 200, { seq: seqs[seqs.length - 1] ?? 0, seqs });
    return;
  }

  // 测试注入：POST /api/_test/jobs
  // body: { session_id, jobs: [{ ID, Status, Command, CreatedAt, StartedAt, ... }] }
  // 按后端 `background.Job` 的序列化形态（Go 字段名，无 json tag）存放，
  // 保证前端 normalize 层在 e2e 里也走真实键名路径。
  if (path === "/api/_test/jobs" && req.method === "POST") {
    const body = await readBody(req);
    const sessionId =
      typeof body?.session_id === "string" && body.session_id
        ? body.session_id
        : "e2e-session-1";
    const jobs = Array.isArray(body?.jobs) ? body.jobs : [];
    mockJobsBySession.set(
      sessionId,
      jobs.map((job, index) => {
        const now = new Date().toISOString();
        return {
          ID: typeof job?.ID === "string" ? job.ID : `e2e-job-${index + 1}`,
          SessionID: sessionId,
          Kind: typeof job?.Kind === "string" ? job.Kind : "shell",
          Command: typeof job?.Command === "string" ? job.Command : "echo e2e",
          Cwd: typeof job?.Cwd === "string" ? job.Cwd : "E:/projects/e2e",
          Priority: 0,
          RestartPolicy: "never",
          Status: typeof job?.Status === "string" ? job.Status : "running",
          Message: typeof job?.Message === "string" ? job.Message : "",
          CreatedAt: typeof job?.CreatedAt === "string" ? job.CreatedAt : now,
          StartedAt: typeof job?.StartedAt === "string" ? job.StartedAt : now,
          FinishedAt: typeof job?.FinishedAt === "string" ? job.FinishedAt : "",
          ExitCode: typeof job?.ExitCode === "number" ? job.ExitCode : null,
          LogPath: "",
          _output: typeof job?.Output === "string" ? job.Output : "",
        };
      }),
    );
    writeJson(res, 200, { ok: true, count: jobs.length });
    return;
  }

  // 测试注入（P2-1A）：POST /api/_test/files
  // body: { files: [{ path, content?, data_base64?, byte_count? }] }
  // content 为 UTF-8 文本的便捷写法；data_base64 用于二进制/坏编码等场景。
  if (path === "/api/_test/files" && req.method === "POST") {
    const body = await readBody(req);
    const files = Array.isArray(body?.files) ? body.files : [];
    for (const file of files) {
      if (typeof file?.path !== "string" || !file.path) continue;
      const dataBase64 =
        typeof file.data_base64 === "string"
          ? file.data_base64
          : Buffer.from(typeof file.content === "string" ? file.content : "", "utf8").toString(
              "base64",
            );
      const byteCount =
        typeof file.byte_count === "number" && file.byte_count >= 0
          ? file.byte_count
          : Buffer.from(dataBase64, "base64").byteLength;
      mockFiles.set(file.path, { dataBase64, byteCount });
    }
    writeJson(res, 200, { ok: true, count: files.length });
    return;
  }

  // 测试注入（P2-7 子片 3）：POST /api/_test/models
  // body: { providers: [{ name, models, default_model? }], default_provider?, default_model? }
  // 只登记目录原文（不排序、不补默认模型）；`count` 与服务端一致地按 provider 数计算。
  if (path === "/api/_test/models" && req.method === "POST") {
    const body = await readBody(req);
    const providers = Array.isArray(body?.providers) ? body.providers : [];
    mockRuntimeModelsCatalog = {
      providers,
      count: providers.length,
      default_provider:
        typeof body?.default_provider === "string" ? body.default_provider : "",
      default_model:
        typeof body?.default_model === "string" ? body.default_model : "",
    };
    writeJson(res, 200, { ok: true, count: providers.length });
    return;
  }

  // 测试注入（B4/B5）：POST /api/_test/history
  // body: { session_id, history: [{ role, content, tool_call_id?, tool_calls?, metadata? }] }
  // 覆盖 `GET /api/runtime/sessions/:id/history` 的数据源，用于「历史里的工具回执
  // 还原成具体工具行（读文件/改文件/执行命令）」的用例；未注入即空历史。
  if (path === "/api/_test/history" && req.method === "POST") {
    const body = await readBody(req);
    const sessionId =
      typeof body?.session_id === "string" && body.session_id
        ? body.session_id
        : "e2e-session-1";
    const history = Array.isArray(body?.history) ? body.history : [];
    const session = ensureMockSession(sessionId);
    if (session) {
      session.history = history;
    }
    writeJson(res, 200, { ok: true, session_id: sessionId, count: history.length });
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
  // --- 后台任务（/background/jobs 端点；P2-1A 面板消费）---
  // 返回形态与后端一致：列表 `{jobs,count}`、详情/取消 `{job}`、输出 `{output}`；
  // job 字段用 Go 字段名（PascalCase），驱动前端 normalize 层。
  function findMockJob(jobId) {
    for (const jobs of mockJobsBySession.values()) {
      const match = jobs.find((entry) => entry.ID === jobId);
      if (match) return match;
    }
    return null;
  }
  function stripMockJob(job) {
    const { _output, ...rest } = job;
    return rest;
  }
  if (path === "/api/runtime/background/jobs" && req.method === "GET") {
    const sessionId = url.searchParams.get("session_id") ?? "";
    const statusFilter = (url.searchParams.get("status") ?? "")
      .split(",")
      .map((value) => value.trim().toLowerCase())
      .filter(Boolean);
    const all = [...mockJobsBySession.values()].flat();
    const scoped = sessionId
      ? all.filter((job) => job.SessionID === sessionId)
      : all;
    const jobs = statusFilter.length
      ? scoped.filter((job) =>
          statusFilter.includes(String(job.Status).toLowerCase()),
        )
      : scoped;
    writeJson(res, 200, { jobs: jobs.map(stripMockJob), count: jobs.length });
    return;
  }
  if (path.startsWith("/api/runtime/background/jobs/")) {
    const rest = path.slice("/api/runtime/background/jobs/".length);
    const [rawId, action] = rest.split("/");
    const jobId = decodeURIComponent(rawId ?? "");
    const job = findMockJob(jobId);
    if (!job) {
      writeJson(res, 404, { error: "job not found", job_id: jobId });
      return;
    }
    if (action === "cancel" && req.method === "POST") {
      job.Status = "cancelled";
      job.FinishedAt = new Date().toISOString();
      writeJson(res, 200, { job: stripMockJob(job) });
      return;
    }
    if (action === "output" && req.method === "GET") {
      const offset = Number(url.searchParams.get("offset") ?? 0) || 0;
      const limit = Number(url.searchParams.get("limit") ?? 8192) || 8192;
      const text = job._output ?? "";
      const chunk = text.slice(offset, offset + limit);
      writeJson(res, 200, {
        output: {
          JobID: job.ID,
          Status: job.Status,
          Output: chunk,
          NextOffset: offset + chunk.length,
          ExitCode: job.ExitCode ?? null,
        },
      });
      return;
    }
    if (action === "events" && req.method === "GET") {
      writeJson(res, 200, { events: [], count: 0 });
      return;
    }
    if (!action && req.method === "GET") {
      writeJson(res, 200, { job: stripMockJob(job) });
      return;
    }
    writeJson(res, 405, { error: "method not allowed" });
    return;
  }

  // --- 运行时文件读取（P2-1A：`POST /api/runtime/fs/read-file`）---
  // 契约对齐后端 file_transfer_handlers.go：`{path}` → `{file:{path, data_base64, byte_count}}`；
  // 未登记的路径与后端一致返回 500（读盘失败），前端如实呈现，不做本地兜底。
  if (path === "/api/runtime/fs/read-file") {
    if (req.method !== "POST") {
      writeJson(res, 405, { error: "method not allowed" });
      return;
    }
    const body = await readBody(req);
    const requestedPath = typeof body?.path === "string" ? body.path : "";
    if (!requestedPath) {
      writeJson(res, 400, { error: "path is required" });
      return;
    }
    const file = mockFiles.get(requestedPath);
    if (!file) {
      writeJson(res, 500, { error: `no such file: ${requestedPath}` });
      return;
    }
    writeJson(res, 200, {
      file: {
        path: requestedPath,
        data_base64: file.dataBase64,
        byte_count: file.byteCount,
      },
    });
    return;
  }

  // --- 文件浏览器（P0-P2：/api/runtime/fs/roots|list|preview）---
  // 契约对齐 backend/internal/filebrowse：roots 给可用作用域根，list 只铺根层，
  // preview 只认 notes.txt（其余路径与后端一致 404）。路径一律「相对作用域根」。
  if (path === "/api/runtime/fs/roots" && req.method === "GET") {
    writeJson(res, 200, {
      roots: [
        {
          scope: E2E_FS_SCOPE,
          kind: "session",
          name: "e2e-workspace",
          path: E2E_FS_ROOT_PATH,
          exists: true,
          is_git_repo: true,
          git_root: E2E_FS_ROOT_PATH,
        },
      ],
      count: 1,
    });
    return;
  }

  if (path === "/api/runtime/fs/list" && req.method === "GET") {
    const requestedDir = (url.searchParams.get("path") ?? "").trim();
    const isRoot = requestedDir === "" || requestedDir === ".";
    writeJson(res, 200, {
      dir: {
        path: isRoot ? "" : requestedDir,
        abs_path: isRoot ? E2E_FS_ROOT_PATH : `${E2E_FS_ROOT_PATH}/${requestedDir}`,
        parent: isRoot
          ? ""
          : requestedDir.includes("/")
            ? requestedDir.slice(0, requestedDir.lastIndexOf("/"))
            : "",
        is_root: isRoot,
      },
      // 只铺根层：用例不进入子目录，子层如实给空数组（不伪造条目）。
      entries: isRoot ? mockFsTreeEntries() : [],
      next_cursor: null,
      has_more: false,
      truncated: false,
      sort: "type_then_name",
    });
    return;
  }

  // P1：全库模糊搜索（`/api/runtime/fs/search`）。契约对齐 backend/internal/filebrowse/search.go：
  // 扁平 items（带 score/match）+ 游标分页 + truncated 归因；未命中给空数组而不是 404。
  if (path === "/api/runtime/fs/search" && req.method === "GET") {
    const query = (url.searchParams.get("q") ?? "").trim();
    const kinds = (url.searchParams.get("kinds") ?? "both").trim();
    const cursor = (url.searchParams.get("cursor") ?? "").trim();
    const limitRaw = Number(url.searchParams.get("limit") ?? "");
    const limit = Number.isFinite(limitRaw) && limitRaw > 0 ? Math.min(limitRaw, 200) : 50;
    const matched = mockFsSearchMatches(kinds, query);
    // 第二页用固定游标名：夹具总量小，这样能在 E2E 里确定性地覆盖 has_more → loadMore。
    const offset = cursor === "e2e-search-page-2" ? limit : 0;
    const pageItems = matched.slice(offset, offset + limit);
    const remaining = matched.length - (offset + pageItems.length);
    writeJson(res, 200, {
      scope: E2E_FS_SCOPE,
      query,
      base: "",
      items: pageItems,
      next_cursor: remaining > 0 ? "e2e-search-page-2" : null,
      has_more: remaining > 0,
      scanned: matched.length,
      truncated: false,
      truncated_reason: [],
      elapsed_ms: 4,
      limit,
    });
    return;
  }

  if (path === "/api/runtime/fs/preview" && req.method === "GET") {
    const target = (url.searchParams.get("path") ?? "").trim();
    if (target !== "notes.txt") {
      writeJson(res, 404, { error: `path does not exist: ${target}` });
      return;
    }
    const text = [
      E2E_PREVIEW_MARKER,
      "第 2 行：预览区可见，说明高度链没有被内容高度顶出可视范围。",
      "第 3 行：行号与 mtime 都来自服务端结论。",
    ].join("\n");
    writeJson(res, 200, {
      kind: "text",
      path: target,
      abs_path: `${E2E_FS_ROOT_PATH}/notes.txt`,
      size: 96,
      mtime: E2E_MTIME_SECONDS,
      mime: "text/plain",
      text,
      truncated: false,
      line_count: 3,
      encoding: "utf-8",
    });
    return;
  }

  // --- Git 变更面（P3-P4：/api/runtime/git/status|diff|commits）---
  // status 给 61 个变更（列表必然溢出）；diff/commits 给最小合法载荷
  // （git-surface 挂载即取 diff 与 commits，缺任一路由会渲染成失败态）。
  if (path === "/api/runtime/git/status" && req.method === "GET") {
    const unstaged = [];
    for (let index = 1; index <= E2E_CHANGED_FILE_COUNT; index += 1) {
      unstaged.push({
        path: `src/module-${String(index).padStart(3, "0")}.ts`,
        status: "M",
        insertions: index,
        deletions: index,
        binary: false,
      });
    }
    writeJson(res, 200, {
      repo: {
        root: E2E_FS_ROOT_PATH,
        branch: "e2e-main",
        detached: false,
        head: "0123456789abcdef0123456789abcdef01234567",
        ahead: 0,
        behind: 0,
        is_bare: false,
      },
      clean: false,
      staged: [
        { path: "src/staged.ts", status: "A", insertions: 3, deletions: 0, binary: false },
      ],
      unstaged,
      untracked: [],
      conflicts: [],
      warnings: [],
      generated_at: E2E_MTIME_SECONDS,
    });
    return;
  }

  if (path === "/api/runtime/git/diff" && req.method === "GET") {
    const file = (url.searchParams.get("file") ?? "").trim();
    writeJson(res, 200, {
      file: { path: file, status: "M", is_binary: false, is_submodule: false },
      target: "working",
      context: 3,
      whitespace: "show",
      insertions: 1,
      deletions: 1,
      hunks: [
        {
          header: "@@ -1,4 +1,4 @@",
          old_start: 1,
          old_lines: 4,
          new_start: 1,
          new_lines: 4,
          lines: [
            { type: "context", old_no: 1, new_no: 1, text: "export function e2e() {" },
            { type: "del", old_no: 2, new_no: null, text: "  return 1;" },
            { type: "add", old_no: null, new_no: 2, text: "  return 2;" },
            { type: "context", old_no: 3, new_no: 3, text: "}" },
          ],
        },
      ],
      raw: "",
      parse_error: "",
      truncated: false,
      generated_at: E2E_MTIME_SECONDS,
    });
    return;
  }

  if (path === "/api/runtime/git/commits" && req.method === "GET") {
    writeJson(res, 200, {
      commits: [
        {
          sha: "0123456789abcdef0123456789abcdef01234567",
          short_sha: "0123456",
          author: "e2e",
          authored_at: "2025-09-16T05:20:00Z",
          subject: "e2e fixture",
          refs: [],
        },
      ],
      next_cursor: null,
      has_more: false,
    });
    return;
  }

  // --- 用量与配额（/api/runtime/usage/*；P2-1A 用量面板消费）---
  // 形态对齐后端 authorizeUsageAdmin 之后的 handler：全局 stats 带 scopes
  // （scope/quota 为 null），指定 scope 时返回 scope + quota 且不再带 scopes。
  if (path === "/api/runtime/usage/stats" && req.method === "GET") {
    const tenantId = (url.searchParams.get("tenant_id") ?? "").trim();
    const projectId = (url.searchParams.get("project_id") ?? "").trim();
    const userId = (url.searchParams.get("user_id") ?? "").trim();
    const scopeKey = userId || projectId || tenantId;
    const payload = {
      tracking_enabled: true,
      policy: mockUsagePolicySummary(),
      usage: scopeKey ? mockScopedUsage() : mockGlobalUsage(),
    };
    if (scopeKey) {
      const quota = mockUsageQuota(scopeKey);
      writeJson(res, 200, {
        ...payload,
        scope: {
          tenant_id: tenantId,
          project_id: projectId,
          user_id: userId,
          scope_key: scopeKey,
        },
        // tenant-a 未配置上限：如实返回 null，由前端显示「不可用」而不是伪造余量。
        quota,
      });
      return;
    }
    writeJson(res, 200, { ...payload, scope: null, quota: null, scopes: mockUsageScopes() });
    return;
  }
  if (path === "/api/runtime/usage/ledger" && req.method === "GET") {
    const entrypoint = (url.searchParams.get("entrypoint") ?? "").trim();
    const skill = (url.searchParams.get("skill") ?? "").trim();
    const successParam = (url.searchParams.get("success") ?? "").trim();
    const limit = Number(url.searchParams.get("limit") ?? "50") || 50;
    let records = mockUsageLedgerRecords();
    if (entrypoint) {
      records = records.filter((record) => record.metadata.entrypoint === entrypoint);
    }
    if (skill) {
      records = records.filter((record) => record.metadata.skill === skill);
    }
    if (successParam === "true" || successParam === "false") {
      const expected = successParam === "true";
      records = records.filter((record) => record.success === expected);
    }
    records = records.slice(0, limit);
    writeJson(res, 200, {
      records,
      count: records.length,
      filters: { limit },
    });
    return;
  }
  if (path === "/api/runtime/usage/policy" && req.method === "GET") {
    writeJson(res, 200, {
      policy: {
        ...mockUsagePolicySummary(),
        tenants: { "tenant-a": { max_requests: 500, max_tokens: 200000 } },
        projects: {},
        users: { alice: { max_requests: 100, max_tokens: 50000 } },
      },
    });
    return;
  }
  // --- 技能市场（/api/runtime/skills/*；P2-1A）---
  // 契约对齐 backend/internal/api/skills/handler.go：
  //   GET  /skills                  → {skills, count}
  //   GET  /skills/{name}           → Skill（未登记 404，前端区分「不存在」与其他失败）
  //   GET  /skills/search           → {query, results, matches, count, limit, resolved_mode…}
  //   GET  /skills/stats            → {stats, total_skills, skill_dirs, source_summary, …}
  //   GET  /skills/hot-reload/stats → {stats}；未配置热重载时 503（与后端同形）
  //   POST /skills/hot-reload/{start,stop,reload} → 需管理令牌，缺失/不匹配即 403
  // 路由顺序要紧：search / stats / hot-reload 必须先于 `/skills/{name}` 前缀匹配。
  if (path === "/api/runtime/skills/search" && req.method === "GET") {
    const query = (url.searchParams.get("q") ?? "").trim();
    const category = (url.searchParams.get("category") ?? "").trim();
    const requestedMode = (url.searchParams.get("mode") ?? "auto").trim() || "auto";
    const rawLimit = Number(url.searchParams.get("limit") ?? "");
    const limit = Number.isFinite(rawLimit) && rawLimit > 0 ? Math.min(Math.floor(rawLimit), 200) : 20;
    if (!query) {
      writeJson(res, 400, { error: "query parameter `q` is required" });
      return;
    }
    const needle = query.toLowerCase();
    const matched = mockSkillEntries()
      .filter((skill) => typeof skill.name === "string" && skill.name)
      .filter((skill) => {
        if (category && skill.category !== category) {
          return false;
        }
        const haystack = [skill.name, skill.description, skill.category, ...(skill.tags ?? [])]
          .join(" ")
          .toLowerCase();
        return haystack.includes(needle);
      });
    const slice = matched.slice(0, limit);
    // 语义模式在 embedding 关闭时按后端行为降级为 lexical，并原样上报 resolved_mode；
    // 前端据此显示真实模式，而不是把降级结果假装成语义命中。
    const resolvedMode =
      requestedMode === "semantic" && !mockSkillsEmbeddingEnabled
        ? "lexical"
        : requestedMode === "auto"
          ? "lexical"
          : requestedMode;
    writeJson(res, 200, {
      query,
      results: slice,
      matches: slice.map((skill) => ({
        skill,
        score: 1,
        matched_by: resolvedMode === "semantic" ? "embedding" : "keyword",
        details: `keyword: ${needle}`,
      })),
      count: slice.length,
      limit,
      requested_mode: requestedMode,
      resolved_mode: resolvedMode,
      used_embedding: resolvedMode === "semantic",
    });
    return;
  }

  if (path === "/api/runtime/skills/stats" && req.method === "GET") {
    writeJson(res, 200, {
      stats: [
        {
          name: "code-review",
          category: "quality",
          call_count: 12,
          success_rate: 0.92,
          avg_duration_ms: 1200,
          source_dir: "D:/skills",
          source_path: "D:/skills/code-review/SKILL.md",
          source_layer: "project",
        },
        {
          name: "docs-writer",
          category: "docs",
          call_count: 3,
          success_rate: 1,
          avg_duration_ms: 450,
          source_dir: "D:/skills",
          source_path: "D:/skills/docs-writer/SKILL.md",
          source_layer: "project",
        },
      ],
      total_skills: 3,
      skill_dirs: ["D:/skills", "E:/shared/skills"],
      source_summary: { project: 2, user: 1 },
      mutation_policy: {
        read_only: false,
        disable_import: false,
        disable_persist: false,
        disable_reload_ops: false,
        disable_hot_reload: false,
      },
      embedding: { enabled: mockSkillsEmbeddingEnabled },
    });
    return;
  }

  if (path === "/api/runtime/skills/hot-reload/stats" && req.method === "GET") {
    if (!mockSkillsHotReload.configured) {
      writeJson(res, 503, { error: "skill hot reload is not configured" });
      return;
    }
    writeJson(res, 200, { stats: mockHotReloadStatsPayload() });
    return;
  }

  if (path.startsWith("/api/runtime/skills/hot-reload/")) {
    const action = path.slice("/api/runtime/skills/hot-reload/".length);
    if (req.method !== "POST") {
      writeJson(res, 405, { error: "method not allowed" });
      return;
    }
    if (!["start", "stop", "reload"].includes(action)) {
      writeJson(res, 404, { error: "unknown hot reload action" });
      return;
    }
    if (!mockSkillsAuthorized(req)) {
      writeJson(res, 403, { error: "forbidden" });
      return;
    }
    if (action === "start") {
      const body = await readBody(req);
      const dirs = Array.isArray(body?.dirs)
        ? body.dirs.filter((dir) => typeof dir === "string" && dir.trim())
        : [];
      if (dirs.length === 0) {
        writeJson(res, 400, { error: "`dirs` must contain at least one directory" });
        return;
      }
      mockSkillsHotReload.configured = true;
      mockSkillsHotReload.watching = true;
      mockSkillsHotReload.skillCount = mockSkillEntries().filter((skill) => skill.name).length;
      mockSkillsHotReload.debounceTime =
        typeof body?.debounce_ms === "number" && Number.isFinite(body.debounce_ms)
          ? `${Math.max(0, Math.floor(body.debounce_ms))}ms`
          : "1s";
    } else if (action === "stop") {
      mockSkillsHotReload.watching = false;
      mockSkillsHotReload.skillCount = 0;
    } else {
      mockSkillsHotReload.callbackCount += 1;
    }
    writeJson(res, 200, { stats: mockHotReloadStatsPayload() });
    return;
  }

  if (path.startsWith("/api/runtime/skills/") && req.method === "GET") {
    const requested = decodeURIComponent(path.slice("/api/runtime/skills/".length));
    const skill = mockSkillEntries().find((entry) => entry.name === requested);
    if (!skill) {
      writeJson(res, 404, { error: `skill not found: ${requested}` });
      return;
    }
    writeJson(res, 200, skill);
    return;
  }

  if (path === "/api/runtime/skills" && req.method === "GET") {
    writeJson(res, 200, mockSkillCatalogPayload());
    return;
  }

  if (path === "/api/runtime/sessions" && req.method === "GET") {
    writeJson(res, 200, { sessions: [...mockSessions.values()] });
    return;
  }
  if (path === "/api/runtime/sessions" && req.method === "POST") {
    const body = await readBody(req);
    const requestedId =
      typeof body?.session_id === "string" && body.session_id
        ? body.session_id
        : typeof body?.id === "string" && body.id
          ? body.id
          : typeof body?.title === "string" && body.title
            ? `e2e-fork-${++mockCreatedSessionSeq}`
            : "e2e-session-1";
    const session = ensureMockSession(requestedId);
    if (session && typeof body?.title === "string" && body.title) {
      session.title = body.title;
    }
    // P2-1A：seed 时可携带检索维度（state/user_id/tags/metadata），供
    // `POST /api/runtime/sessions/search` 的服务端过滤用例使用。
    if (session) {
      if (typeof body?.state === "string" && body.state) {
        session.state = body.state;
      }
      if (typeof body?.user_id === "string" && body.user_id) {
        session.user_id = body.user_id;
      }
      if (Array.isArray(body?.tags)) {
        session.tags = body.tags.filter((tag) => typeof tag === "string" && tag.trim());
      }
      if (body?.metadata && typeof body.metadata === "object") {
        session.metadata = { ...(session.metadata ?? {}), ...body.metadata };
      }
      // 检索结果行读取的是 metadata（与 chat.Session 同形），因此 seed 的标题/标签
      // 需要同时落到 metadata，避免「过滤命中了但行里看不到标签」的假象。
      if (session.title) {
        session.metadata = { ...(session.metadata ?? {}), title: session.title };
      }
      if (Array.isArray(session.tags) && session.tags.length > 0) {
        session.metadata = { ...(session.metadata ?? {}), tags: session.tags };
      }
    }
    const now = new Date().toISOString();
    writeJson(res, 200, {
      session: {
        ...session,
        updated_at: now,
      },
      session_id: requestedId,
      id: requestedId,
    });
    return;
  }
  // P2-1A：会话元数据检索（真实后端 backend/internal/api/skills/handler.go SearchSessions）。
  // 过滤语义与后端一致：tags 为 AND，state 为全等，user_id 为空表示不限用户；
  // 响应 filters 用 camelCase（与 chat.SessionSearchOptions 的 json tag 对齐）。
  if (path === "/api/runtime/sessions/search" && req.method === "POST") {
    const body = await readBody(req);
    const userId =
      typeof body?.user_id === "string" && body.user_id.trim()
        ? body.user_id.trim()
        : (url.searchParams.get("user_id") ?? "").trim();
    const state =
      typeof body?.state === "string" && body.state.trim()
        ? body.state.trim()
        : (url.searchParams.get("state") ?? "").trim();
    const tags = Array.isArray(body?.tags)
      ? body.tags.filter((tag) => typeof tag === "string" && tag.trim())
      : [];
    const limitRaw = Number(body?.limit);
    const offsetRaw = Number(body?.offset);
    const limit = Number.isFinite(limitRaw) && limitRaw > 0 ? Math.floor(limitRaw) : 50;
    const offset = Number.isFinite(offsetRaw) && offsetRaw > 0 ? Math.floor(offsetRaw) : 0;

    const matches = [...mockSessions.values()].filter((session) => {
      if (userId && session.user_id !== userId) return false;
      if (state && session.state !== state) return false;
      const sessionTags = Array.isArray(session.tags) ? session.tags : [];
      return tags.every((tag) => sessionTags.includes(tag));
    });
    writeJson(res, 200, {
      sessions: matches.slice(offset, offset + limit),
      count: matches.length,
      filters: {
        userId: userId || undefined,
        state: state || undefined,
        tags: tags.length > 0 ? tags : undefined,
        limit,
        offset,
      },
    });
    return;
  }
  // P2-1A：会话统计（真实后端 GET /api/runtime/sessions/stats，见
  // internal/api/skills/handler.go GetSessionStats）。mock 口径与后端一致：
  //   * user_id 为空 → 全量聚合；否则只统计该用户的会话；
  //   * totalMessages 由 mock 事件表推导（真实后端按持久化消息计），
  //     仅用于验证 camelCase 字段接线，不改变前端归一化逻辑。
  if (path === "/api/runtime/sessions/stats" && req.method === "GET") {
    const statsUserId = (url.searchParams.get("user_id") ?? "").trim();
    const statsSessions = [...mockSessions.values()].filter(
      (session) => !statsUserId || session.user_id === statsUserId,
    );
    const sessionStats = {
      total: statsSessions.length,
      active: 0,
      idle: 0,
      closed: 0,
      archived: 0,
      totalMessages: 0,
      tags: {},
    };
    for (const session of statsSessions) {
      const state = typeof session.state === "string" ? session.state : "";
      if (state === "active") sessionStats.active += 1;
      if (state === "idle") sessionStats.idle += 1;
      if (state === "closed") sessionStats.closed += 1;
      if (state === "archived") sessionStats.archived += 1;
      for (const tag of Array.isArray(session.tags) ? session.tags : []) {
        if (typeof tag === "string" && tag.trim()) {
          sessionStats.tags[tag] = (sessionStats.tags[tag] ?? 0) + 1;
        }
      }
      sessionStats.totalMessages += (
        chatSseEventsBySession.get(session.session_id) ?? []
      ).length;
    }
    writeJson(res, 200, {
      user_id: statsUserId || statsSessions[0]?.user_id || "default",
      stats: sessionStats,
    });
    return;
  }
  if (path === "/api/runtime/sessions/users") {
    // P2-1A：用户清单由 seed 的会话派生（reset 后为空），让侧栏用户切换与
    // 会话检索弹层的用户筛选在 e2e 里有真实数据源，而不是硬编码空列表。
    const grouped = new Map();
    for (const session of mockSessions.values()) {
      const userId = typeof session.user_id === "string" ? session.user_id.trim() : "";
      if (!userId) continue;
      const entry = grouped.get(userId) ?? {
        user_id: userId,
        session_count: 0,
        active_count: 0,
        idle_count: 0,
        closed_count: 0,
        archived_count: 0,
        latest_updated_at: undefined,
      };
      entry.session_count += 1;
      const state = typeof session.state === "string" ? session.state : "";
      if (state === "active") entry.active_count += 1;
      if (state === "idle") entry.idle_count += 1;
      if (state === "closed") entry.closed_count += 1;
      if (state === "archived") entry.archived_count += 1;
      const updatedAt = typeof session.updated_at === "string" ? session.updated_at : "";
      if (updatedAt && (!entry.latest_updated_at || updatedAt > entry.latest_updated_at)) {
        entry.latest_updated_at = updatedAt;
      }
      grouped.set(userId, entry);
    }
    const users = [...grouped.values()].sort((left, right) =>
      left.user_id.localeCompare(right.user_id),
    );
    writeJson(res, 200, {
      users,
      count: users.length,
      total_count: users.length,
      default_user_id: users[0]?.user_id,
    });
    return;
  }
  // 会话分支（方案 §6.1）：POST /api/runtime/sessions/{id}/branch。
  // 与后端同语义：把源会话在锚点处的**历史前缀**复制进新会话，源会话零改动；
  // 锚点缺省 = 整会话（`include_anchor` 对缺省锚点无效）；`include_anchor:false`
  // 时前缀停在锚点之前。新会话走既有的确定性 `e2e-branch-N` 序列，lineage 写进
  // `metadata.context` 的 `fork_*` 键（不复用 `agent_parent_session_id`，避免被
  // agent-control 面板展示成子代理）；`workspace_path` 由「服务端」继承。
  if (/^\/api\/runtime\/sessions\/[^/]+\/branch$/.test(path) && req.method === "POST") {
    const sourceId = decodeURIComponent(path.split("/")[4]);
    const source = mockSessions.get(sourceId);
    if (!source) {
      writeJson(res, 404, { error: "session not found", session_id: sourceId });
      return;
    }
    const body = await readBody(req);
    const anchorId =
      typeof body?.anchor_message_id === "string"
        ? body.anchor_message_id.trim()
        : "";
    const includeAnchor = body?.include_anchor !== false;
    const history = Array.isArray(source.history) ? source.history : [];
    let anchorIndex = -1;
    if (anchorId) {
      anchorIndex = history.findIndex(
        (message) => message?.metadata?.message_id === anchorId,
      );
      if (anchorIndex < 0) {
        writeJson(res, 400, { error: "branch anchor message not found" });
        return;
      }
    }
    const sourceTitle =
      typeof source.title === "string" && source.title ? source.title : sourceId;
    const createdId = `e2e-branch-${++mockCreatedSessionSeq}`;
    const title =
      typeof body?.title === "string" && body.title
        ? body.title
        : `${sourceTitle} (branch)`;
    const now = new Date().toISOString();
    const sourceContext =
      source.metadata && typeof source.metadata.context === "object"
        ? source.metadata.context
        : {};
    const branchSession = {
      session_id: createdId,
      id: createdId,
      title,
      created_at: now,
      updated_at: now,
      user_id:
        typeof body?.user_id === "string" && body.user_id
          ? body.user_id
          : source.user_id,
      state: "active",
      // 深拷贝前缀：源会话历史零改动（断言「源历史条数不变」的用例依赖这一点）。
      history: (anchorId
        ? history.slice(0, includeAnchor ? anchorIndex + 1 : anchorIndex)
        : history
      ).map((message) => structuredClone(message)),
      metadata: {
        title,
        context: {
          ...(sourceContext &&
          typeof sourceContext.workspace_path === "string" &&
          sourceContext.workspace_path
            ? { workspace_path: sourceContext.workspace_path }
            : {}),
          fork_parent_session_id: sourceId,
          fork_root_session_id:
            typeof sourceContext.fork_root_session_id === "string" &&
            sourceContext.fork_root_session_id
              ? sourceContext.fork_root_session_id
              : sourceId,
          ...(anchorId ? { fork_source_message_id: anchorId } : {}),
          fork_origin_title: sourceTitle,
        },
      },
    };
    mockSessions.set(createdId, branchSession);
    writeJson(res, 201, {
      session: branchSession,
      anchor: {
        source_message_id: anchorId,
        turn_index: 0,
        included: anchorId ? includeAnchor : true,
      },
    });
    return;
  }

  // P1-9 e2e：归档/恢复与非破坏删除需要可变状态，否则侧栏刷新后行不消失。
  if (
    /^\/api\/runtime\/sessions\/[^/]+\/(?:archive|activate)$/.test(path) &&
    req.method === "POST"
  ) {
    const sessionId = decodeURIComponent(path.split("/")[4]);
    const session = mockSessions.get(sessionId);
    if (session) {
      session.state = path.endsWith("/archive") ? "archived" : "active";
      session.updated_at = new Date().toISOString();
    }
    writeJson(res, 200, { session_id: sessionId, id: sessionId, state: session?.state });
    return;
  }
  // 行内重命名（PATCH /api/runtime/sessions/{id}，body: { title }）：真实后端更新标题
  // 后返回会话记录；侧栏 refreshSessions 重取列表时行标题随之更新。
  // 少了这条，e2e 里「重命名成功但行标题不变」会被误判成前端没刷新。
  if (/^\/api\/runtime\/sessions\/[^/]+$/.test(path) && req.method === "PATCH") {
    const sessionId = decodeURIComponent(path.split("/")[4]);
    const patchBody = await readBody(req);
    const session = mockSessions.get(sessionId);
    if (!session) {
      writeJson(res, 404, { error: `session not found: ${sessionId}` });
      return;
    }
    if (typeof patchBody?.title === "string" && patchBody.title.trim()) {
      session.title = patchBody.title;
      session.metadata = { ...(session.metadata ?? {}), title: patchBody.title };
    }
    session.updated_at = new Date().toISOString();
    writeJson(res, 200, { session });
    return;
  }
  if (/^\/api\/runtime\/sessions\/[^/]+$/.test(path) && req.method === "DELETE") {
    const sessionId = decodeURIComponent(path.split("/")[4]);
    const deleted = mockSessions.delete(sessionId);
    writeJson(res, 200, { deleted, id: sessionId });
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
  // P2-1A：运行时状态快照（重载 / 重连后重建待交互卡片）。
  if (/^\/api\/runtime\/sessions\/[^/]+\/runtime$/.test(path) && req.method === "GET") {
    const sessionId = decodeURIComponent(path.split("/")[4]);
    if (!mockSessions.has(sessionId)) {
      writeJson(res, 404, { error: "session not found" });
      return;
    }
    writeJson(res, 200, {
      state: deriveSessionRuntimeState(sessionId),
      execution_route: "e2e-mock",
    });
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
    writeJson(
      res,
      200,
      mockRuntimeModelsCatalog ?? {
        providers: [],
        count: 0,
        default_provider: "",
        default_model: "",
      },
    );
    return;
  }
  if (path === "/api/runtime/service") {
    writeJson(res, 200, { status: "running", healthy: true });
    return;
  }

  // /logs 页此前完全没有 mock 覆盖：页面拿到兜底 `{}`（entries 缺失）后曾在
  // entries.some / entries.find 上抛 TypeError，把整条路由打成错误面。
  // 前端已按「缺字段降级为空列表」加固，这里补上真实契约让页面能正常渲染。
  if (path === "/api/runtime/logs" && req.method === "GET") {
    writeJson(res, 200, {
      count: MOCK_RUNTIME_LOGS.length,
      entries: MOCK_RUNTIME_LOGS,
      exists: true,
      file_path: "backend/logs/runtime-server.log",
      next_cursor: MOCK_RUNTIME_LOGS[0]?.cursor ?? 0,
    });
    return;
  }

  if (path === "/api/runtime/logs/stream" && req.method === "GET") {
    res.writeHead(200, {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-cache",
      Connection: "keep-alive",
      "X-Accel-Buffering": "no",
    });
    res.write(
      sseEvent("ready", {
        cursor: MOCK_RUNTIME_LOGS[0]?.cursor ?? 0,
        exists: true,
        file_path: "backend/logs/runtime-server.log",
      }),
    );
    // 保持长连接（与真实后端一致）：用例结束由浏览器断开时同步释放 socket。
    req.on("close", () => res.destroy());
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
