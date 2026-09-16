#!/usr/bin/env node
// measure-runtime-stream-dump.mjs
//
// PR-1 步骤 1 的 E2E 首屏基线测量：对运行中的 runtime-server 发起
//   GET {base}/api/runtime/sessions/{session}/runtime/stream?after=0
// 并以流式方式统计 SSE dump 的 ttfB / 帧数 / 字节 / id: 行数等指标。
//
// 设计约束（与方案 §7 验收矩阵对齐）：
//   - 只读：不写任何服务端状态，不 kill/重启服务，不修改任何生产文件；
//   - 流式累计：响应体可达 MB 级，绝不把整份 body 驻留内存后再统计；
//   - 幂等可复跑：同一参数重复运行只产生新的测量结果，无副作用；
//   - 失败可读：服务未启动 / 会话不存在 / HTTP 非 200 都给出明确错误。
//
// 口径（与 docs/plan/sse-live-event-channel-optimization-plan.md §2.2 / §7 一致）：
//   ttfB_ms              发起请求 → 第一个响应体字节（`: open` 注释帧）到达
//   frames               `event: ` 开头的事件帧数（不含 `:` 注释帧）
//   bytes                响应体总字节数（含注释帧与帧分隔空行）
//   idLines              `id: ` 开头行数（Batch 1 起持久化帧才带 id:，是关键可观测信号）
//   dataLines            `data: ` 开头行数
//   keepaliveComments    `: keepalive` 注释帧数
//   frameBytesMin/Max/Avg  单帧字节数（帧 = id/event/data 行 + 分隔空行）
//
// 终止条件：
//   - idle：首个 event 帧到达后，静默 --idle-ms（默认 5000ms，< 服务端 15s keepalive）
//     视为 dump 已排空 → status=complete；
//   - 首个 event 帧前的最长静默为 max(--idle-ms, 15000ms)，避免把慢首查误判为排空；
//   - timeout：总时长超过 --timeout-ms（默认 120000）→ status=timeout（仍输出已统计部分）。
//
// 用法：
//   node scripts/measure-runtime-stream-dump.mjs --help
//   node scripts/measure-runtime-stream-dump.mjs --session <id>
//   node scripts/measure-runtime-stream-dump.mjs            # 自动选事件行数最多的会话
//
// 会话选择（--session 省略时）：
//   1) 只读打开事件库（node:sqlite，readOnly）按 session_events 行数取最大；
//      库路径来源：--db > backend/configs/runtime.yaml 的 sessionRuntime.storePath
//      > backend/data/runtime/session_runtime.sqlite > ~/.aicli/sessions/runtime/session_runtime.sqlite
//   2) 事件库不可用时退化为 HTTP /api/runtime/sessions，按 canonicalMessageCount 取最大（会标记 selection.source）。

import { createRequire } from 'node:module';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import process from 'node:process';

const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = resolve(SCRIPT_DIR, '..');
const require = createRequire(import.meta.url);

const DEFAULTS = {
  base: 'http://127.0.0.1:8101',
  after: 0,
  timeoutMs: 120000,
  idleMs: 5000,
};

// Batch 1 前 wire 上每帧携带的全零值 provenance 块特征（方案 §2.4：约 215B/帧纯噪音）。
// Batch 1 的 summarizeRuntimeEventProvenanceIfBearing 会按承载类型条件化/零值省略，
// 因此该特征出现与否可作为「服务端二进制是否含 Batch 1」的端点内可观测信号。
const ZERO_VALUE_PROVENANCE_SIGNATURE = '"profile_resource_kinds":{}';

const HELP = `measure-runtime-stream-dump.mjs — SSE live 通道 after=0 首屏 dump 基线测量（只读）

用法:
  node scripts/measure-runtime-stream-dump.mjs [选项]

选项:
  --base <url>           runtime-server 基地址（默认 ${DEFAULTS.base}）
  --session <id>         目标会话 id；省略时自动选事件行数最多的会话
  --after <n>            EventStore seq 游标（默认 ${DEFAULTS.after}，即全量首屏 dump）
  --timeout-ms <n>       硬超时，超时仍输出已统计部分（默认 ${DEFAULTS.timeoutMs}）
  --idle-ms <n>          首个 event 帧后判定 dump 排空的静默阈值（默认 ${DEFAULTS.idleMs}）
  --db <path>            事件库 sqlite 路径（仅用于会话自动选择 / 事件行数统计，只读打开）
  --out <path>           额外把结果 JSON 写入文件（stdout 始终输出 JSON）
  --help                 显示本帮助

退出码:
  0 测量完成（含 status=timeout 的部分结果）
  2 无法连接 runtime-server
  3 服务返回非 200（含会话不存在 404）
  4 参数错误
  5 连接成功但未收到任何响应体字节（含硬超时且零字节）

输出（stdout，纯 JSON）:
  { base, session, after, ttfB_ms, firstByteIsOpenComment, total_ms, frames, bytes,
    idLines, dataLines, keepaliveComments, frameBytesMin, frameBytesMax, frameBytesAvg, ... }
  diagnostics 含 zeroValueProvenanceFrames：wire 上带全零值 provenance 块的帧数
  （Batch 1 前的可观测特征；Batch 1 后应为 0）。

示例:
  node scripts/measure-runtime-stream-dump.mjs --session session_20260916133052_DktyYFb3
`;

function parseArgs(argv) {
  const opts = { ...DEFAULTS, session: '', db: '', out: '', help: false };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    const takeValue = (name) => {
      const next = argv[i + 1];
      if (next === undefined || next.startsWith('--')) {
        throw new Error(`选项 ${name} 需要一个值`);
      }
      i += 1;
      return next;
    };
    switch (arg) {
      case '--base': opts.base = takeValue(arg).replace(/\/+$/, ''); break;
      case '--session': opts.session = takeValue(arg).trim(); break;
      case '--after': opts.after = Number.parseInt(takeValue(arg), 10); break;
      case '--timeout-ms': opts.timeoutMs = Number.parseInt(takeValue(arg), 10); break;
      case '--idle-ms': opts.idleMs = Number.parseInt(takeValue(arg), 10); break;
      case '--db': opts.db = takeValue(arg).trim(); break;
      case '--out': opts.out = takeValue(arg).trim(); break;
      case '--help': case '-h': opts.help = true; break;
      default:
        throw new Error(`未知选项: ${arg}（--help 查看用法）`);
    }
  }
  if (!Number.isFinite(opts.after) || opts.after < 0) throw new Error('--after 必须是非负整数');
  if (!Number.isFinite(opts.timeoutMs) || opts.timeoutMs <= 0) throw new Error('--timeout-ms 必须是正整数');
  if (!Number.isFinite(opts.idleMs) || opts.idleMs <= 0) throw new Error('--idle-ms 必须是正整数');
  if (!/^https?:\/\//i.test(opts.base)) throw new Error(`--base 必须是 http(s) URL: ${opts.base}`);
  return opts;
}

function fail(message, code) {
  process.stderr.write(`measure-runtime-stream-dump: ${message}\n`);
  process.exit(code);
}

// ---------------------------------------------------------------------------
// 事件库发现（只读）与会话选择
// ---------------------------------------------------------------------------

function resolveStorePathFromConfig() {
  // backend/configs/runtime.yaml: sessionRuntime.storePath: ../data/runtime/session_runtime.sqlite
  // 相对路径按 config 文件所在目录解析（与后端 ApplyDefaults 的语义一致）。
  const configPath = join(REPO_ROOT, 'backend', 'configs', 'runtime.yaml');
  if (!existsSync(configPath)) return '';
  try {
    const lines = readFileSync(configPath, 'utf8').split(/\r?\n/);
    let inSessionRuntime = false;
    for (const line of lines) {
      if (/^sessionRuntime:\s*$/.test(line)) { inSessionRuntime = true; continue; }
      if (inSessionRuntime && /^\S/.test(line) && line.trim() !== '') { inSessionRuntime = false; }
      if (!inSessionRuntime) continue;
      const match = /^\s+storePath:\s*(\S+)\s*$/.exec(line);
      if (match) {
        const raw = match[1].replace(/^["']|["']$/g, '');
        const expanded = raw.startsWith('~')
          ? join(process.env.USERPROFILE || process.env.HOME || '', raw.slice(1))
          : raw;
        return isAbsolute(expanded) ? expanded : resolve(dirname(configPath), expanded);
      }
    }
  } catch {
    // 配置不可读时静默退化到候选路径
  }
  return '';
}

function discoverStorePath(explicit) {
  if (explicit) return explicit;
  const candidates = [
    resolveStorePathFromConfig(),
    join(REPO_ROOT, 'backend', 'data', 'runtime', 'session_runtime.sqlite'),
    process.env.USERPROFILE || process.env.HOME
      ? join(process.env.USERPROFILE || process.env.HOME, '.aicli', 'sessions', 'runtime', 'session_runtime.sqlite')
      : '',
  ].filter(Boolean);
  for (const candidate of candidates) {
    if (existsSync(candidate)) return candidate;
  }
  return '';
}

// 只读打开事件库并统计每会话行数。任何失败都返回 null（调用方退化到 HTTP 选择）。
function pickSessionFromEventStore(storePath) {
  if (!storePath) return null;
  let db;
  try {
    const { DatabaseSync } = require('node:sqlite');
    db = new DatabaseSync(storePath, { readOnly: true });
    const top = db
      .prepare('SELECT session_id, COUNT(*) AS rows, MAX(seq) AS max_seq FROM session_events GROUP BY session_id ORDER BY rows DESC, session_id LIMIT 1')
      .get();
    if (!top || !top.session_id) return null;
    const total = db.prepare('SELECT COUNT(*) AS rows FROM session_events').get();
    return {
      session: String(top.session_id),
      eventRows: Number(top.rows),
      maxSeq: Number(top.max_seq),
      storePath,
      totalEventRows: total ? Number(total.rows) : null,
    };
  } catch {
    return null;
  } finally {
    try { db?.close(); } catch { /* ignore */ }
  }
}

function countSessionEvents(storePath, sessionId) {
  if (!storePath || !sessionId) return null;
  let db;
  try {
    const { DatabaseSync } = require('node:sqlite');
    db = new DatabaseSync(storePath, { readOnly: true });
    const row = db
      .prepare('SELECT COUNT(*) AS rows, MAX(seq) AS max_seq FROM session_events WHERE session_id = ?')
      .get(sessionId);
    if (!row) return null;
    return { eventRows: Number(row.rows), maxSeq: Number(row.max_seq) };
  } catch {
    return null;
  } finally {
    try { db?.close(); } catch { /* ignore */ }
  }
}

async function fetchJson(url, timeoutMs) {
  const res = await fetch(url, { signal: AbortSignal.timeout(timeoutMs), headers: { accept: 'application/json' } });
  const text = await res.text();
  let body = null;
  try { body = JSON.parse(text); } catch { /* 保留原始文本 */ }
  return { status: res.status, ok: res.ok, body, text };
}

async function pickSessionFromHttp(base) {
  const { ok, status, body, text } = await fetchJson(`${base}/api/runtime/sessions?limit=2000`, 30000);
  if (!ok) throw new Error(`GET /api/runtime/sessions 返回 HTTP ${status}: ${text.slice(0, 300)}`);
  const sessions = Array.isArray(body?.sessions) ? body.sessions : [];
  if (sessions.length === 0) throw new Error('GET /api/runtime/sessions 未返回任何会话');
  const ranked = [...sessions].sort(
    (a, b) => (Number(b?.canonicalMessageCount) || 0) - (Number(a?.canonicalMessageCount) || 0),
  );
  const top = ranked[0];
  return {
    session: String(top.id),
    canonicalMessageCount: Number(top.canonicalMessageCount) || 0,
    source: 'http_sessions_canonicalMessageCount_fallback',
    warning: '事件库不可用：按 canonicalMessageCount（消息数）近似选择，可能不是事件行数最多的会话',
  };
}

async function assertSessionExists(base, sessionId) {
  const res = await fetch(`${base}/api/runtime/sessions/${encodeURIComponent(sessionId)}/runtime`, {
    signal: AbortSignal.timeout(15000),
    headers: { accept: 'application/json' },
  });
  if (res.status === 404) throw new Error(`会话不存在: ${sessionId}（GET /api/runtime/sessions/${sessionId}/runtime → 404）`);
  if (!res.ok) {
    const text = await res.text().catch(() => '');
    throw new Error(`会话预检失败: HTTP ${res.status} ${text.slice(0, 200)}`);
  }
  await res.arrayBuffer(); // 释放连接
}

// ---------------------------------------------------------------------------
// 流式测量核心
// ---------------------------------------------------------------------------

class MeasureError extends Error {
  constructor(message, exitCode) {
    super(message);
    this.exitCode = exitCode;
  }
}

const round3 = (value) => (value === null || value === undefined ? null : Math.round(value * 1000) / 1000);

function makeStats() {
  return {
    bytes: 0,
    frames: 0,
    idLines: 0,
    dataLines: 0,
    keepaliveComments: 0,
    openComments: 0,
    otherComments: 0,
    zeroValueProvenanceFrames: 0,
    frameSizes: [],
    ttfB: null,
    firstByteIsOpenComment: false,
    firstEventFrameAt: null,
    firstIdValue: null,
    lastIdValue: null,
  };
}

// handleLine 按行累计统计。byteLen 是该行在响应体中的字节数（含行尾 \n）。
function handleLine(line, byteLen, now, stats) {
  if (line.startsWith(':')) {
    const comment = line.slice(1).trim();
    if (comment === 'keepalive') stats.keepaliveComments += 1;
    else if (comment === 'open') stats.openComments += 1;
    else stats.otherComments += 1;
    return; // 注释帧不参与帧字节统计
  }
  if (line === '') {
    if (stats._inFrame) {
      stats.frameSizes.push(stats._currentFrameBytes + byteLen);
      stats._inFrame = false;
      stats._currentFrameBytes = 0;
    }
    stats._pendingBytes = 0;
    return;
  }
  if (line.startsWith('id:')) {
    stats.idLines += 1;
    const raw = line.slice(3).trim();
    if (raw !== '') {
      const parsed = Number.parseInt(raw, 10);
      if (Number.isFinite(parsed)) {
        if (stats.firstIdValue === null) stats.firstIdValue = parsed;
        stats.lastIdValue = parsed;
      }
    }
    if (stats._inFrame) stats._currentFrameBytes += byteLen;
    else stats._pendingBytes += byteLen;
    return;
  }
  if (line.startsWith('event:')) {
    stats.frames += 1;
    if (stats.firstEventFrameAt === null) stats.firstEventFrameAt = now;
    stats._inFrame = true;
    stats._currentFrameBytes = stats._pendingBytes + byteLen;
    stats._pendingBytes = 0;
    return;
  }
  if (line.startsWith('data:')) {
    stats.dataLines += 1;
    if (line.includes(ZERO_VALUE_PROVENANCE_SIGNATURE)) stats.zeroValueProvenanceFrames += 1;
    if (stats._inFrame) stats._currentFrameBytes += byteLen;
    else stats._pendingBytes += byteLen;
    return;
  }
  if (stats._inFrame) stats._currentFrameBytes += byteLen;
  else stats._pendingBytes += byteLen;
}

async function measureDump({ base, session, after, timeoutMs, idleMs }) {
  const url = `${base}/api/runtime/sessions/${encodeURIComponent(session)}/runtime/stream?after=${after}`;
  const controller = new AbortController();
  const t0 = performance.now();
  let hardTimedOut = false;
  const hardTimer = setTimeout(() => {
    hardTimedOut = true;
    controller.abort();
  }, timeoutMs);

  let res;
  try {
    res = await fetch(url, { signal: controller.signal, headers: { accept: 'text/event-stream' } });
  } catch (err) {
    clearTimeout(hardTimer);
    if (hardTimedOut) throw new MeasureError(`请求超时（${timeoutMs}ms 内未建立响应）: ${url}`, 5);
    throw new MeasureError(
      `无法连接 runtime-server: ${base}（${err?.cause?.code || err?.message || err}）—— 请确认服务已启动`,
      2,
    );
  }

  if (!res.ok) {
    clearTimeout(hardTimer);
    const text = await res.text().catch(() => '');
    throw new MeasureError(`流端点返回 HTTP ${res.status}: ${text.slice(0, 300) || url}`, 3);
  }

  const stats = makeStats();
  stats._inFrame = false;
  stats._currentFrameBytes = 0;
  stats._pendingBytes = 0;

  const reader = res.body.getReader();
  const decoder = new TextDecoder('utf-8');
  let pending = '';
  let stopReason = 'unknown';

  let idleResolve = null;
  let idlePromise = new Promise((r) => { idleResolve = r; });
  let idleTimer = null;
  const armIdle = () => {
    clearTimeout(idleTimer);
    // 首个 event 帧前放宽静默窗口：慢首查（SQLite 排队）不应被误判为 dump 排空。
    const delay = stats.firstEventFrameAt !== null ? idleMs : Math.max(idleMs, 15000);
    idleTimer = setTimeout(() => idleResolve?.('idle'), delay);
  };
  armIdle();

  try {
    for (;;) {
      const raced = await Promise.race([
        reader.read().then(
          (value) => ({ kind: 'chunk', value }),
          (error) => ({ kind: 'read_error', error }),
        ),
        idlePromise.then((kind) => ({ kind })),
      ]);
      if (raced.kind === 'idle') { stopReason = 'idle'; break; }
      if (raced.kind === 'read_error') {
        if (hardTimedOut) stopReason = 'timeout';
        else if (raced.error?.name === 'AbortError') stopReason = 'aborted';
        else stopReason = `read_error: ${raced.error?.message || raced.error}`;
        break;
      }
      const { done, value } = raced.value;
      if (done) { stopReason = 'server_closed'; break; }

      const now = performance.now();
      stats.bytes += value.byteLength;
      if (stats.ttfB === null) {
        stats.ttfB = now - t0;
        stats.firstByteIsOpenComment = new TextDecoder('utf-8').decode(value.slice(0, 32)).startsWith(': open');
      }
      pending += decoder.decode(value, { stream: true });

      let idx;
      while ((idx = pending.indexOf('\n')) >= 0) {
        const rawLine = pending.slice(0, idx);
        pending = pending.slice(idx + 1);
        const hasCR = rawLine.endsWith('\r');
        const line = hasCR ? rawLine.slice(0, -1) : rawLine;
        handleLine(line, Buffer.byteLength(line, 'utf8') + 1 + (hasCR ? 1 : 0), now, stats);
      }

      idlePromise = new Promise((r) => { idleResolve = r; });
      armIdle();
    }
  } finally {
    clearTimeout(idleTimer);
    clearTimeout(hardTimer);
    try { await reader.cancel(); } catch { /* ignore */ }
    try { controller.abort(); } catch { /* ignore */ }
  }

  if (stats._inFrame && stats._currentFrameBytes > 0) stats.frameSizes.push(stats._currentFrameBytes);

  const sizes = stats.frameSizes;
  const frameBytesMin = sizes.length ? Math.min(...sizes) : 0;
  const frameBytesMax = sizes.length ? Math.max(...sizes) : 0;
  const frameBytesAvg = sizes.length ? round3(sizes.reduce((sum, n) => sum + n, 0) / sizes.length) : 0;

  const status = stopReason === 'idle' || stopReason === 'server_closed'
    ? 'complete'
    : stopReason === 'timeout'
      ? 'timeout'
      : stopReason;

  return {
    url,
    httpStatus: res.status,
    contentType: res.headers.get('content-type') || '',
    stats,
    stopReason,
    status,
    frameBytesMin,
    frameBytesMax,
    frameBytesAvg,
    totalMs: round3(performance.now() - t0),
  };
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

async function preflightReachable(base) {
  try {
    const res = await fetch(`${base}/api/runtime/health`, {
      signal: AbortSignal.timeout(10000),
      headers: { accept: 'application/json' },
    });
    await res.arrayBuffer().catch(() => {});
    if (!res.ok) throw new MeasureError(`健康检查返回 HTTP ${res.status}: ${base}/api/runtime/health`, 2);
  } catch (err) {
    if (err instanceof MeasureError) throw err;
    throw new MeasureError(
      `无法连接 runtime-server: ${base}（${err?.cause?.code || err?.message || err}）—— 请确认服务已启动`,
      2,
    );
  }
}

async function main() {
  let opts;
  try {
    opts = parseArgs(process.argv.slice(2));
  } catch (err) {
    fail(err.message, 4);
  }
  if (opts.help) {
    process.stdout.write(HELP);
    return;
  }

  await preflightReachable(opts.base);

  const storePath = discoverStorePath(opts.db);
  let selection;
  if (opts.session) {
    await assertSessionExists(opts.base, opts.session);
    selection = { source: 'explicit', session: opts.session, storePath: storePath || null };
  } else {
    const fromStore = pickSessionFromEventStore(storePath);
    if (fromStore) {
      selection = {
        source: 'event_store_row_count',
        session: fromStore.session,
        storePath: fromStore.storePath,
        totalEventRows: fromStore.totalEventRows,
      };
    } else {
      const fromHttp = await pickSessionFromHttp(opts.base);
      selection = { ...fromHttp, storePath: storePath || null };
    }
    await assertSessionExists(opts.base, selection.session);
  }

  const eventCount = countSessionEvents(storePath, selection.session);
  const measured = await measureDump({
    base: opts.base,
    session: selection.session,
    after: opts.after,
    timeoutMs: opts.timeoutMs,
    idleMs: opts.idleMs,
  });

  const { stats } = measured;
  const result = {
    base: opts.base,
    session: selection.session,
    after: opts.after,
    ttfB_ms: round3(stats.ttfB),
    firstByteIsOpenComment: stats.firstByteIsOpenComment,
    total_ms: measured.totalMs,
    frames: stats.frames,
    bytes: stats.bytes,
    idLines: stats.idLines,
    dataLines: stats.dataLines,
    keepaliveComments: stats.keepaliveComments,
    frameBytesMin: measured.frameBytesMin,
    frameBytesMax: measured.frameBytesMax,
    frameBytesAvg: measured.frameBytesAvg,
    idLinesPresent: stats.idLines > 0,
    status: measured.status,
    selection,
    diagnostics: {
      url: measured.url,
      httpStatus: measured.httpStatus,
      contentType: measured.contentType,
      stopReason: measured.stopReason,
      openComments: stats.openComments,
      otherComments: stats.otherComments,
      zeroValueProvenanceFrames: stats.zeroValueProvenanceFrames,
      firstEventFrame_ms: round3(stats.firstEventFrameAt === null ? null : stats.firstEventFrameAt),
      firstIdValue: stats.firstIdValue,
      lastIdValue: stats.lastIdValue,
      sessionEventRows: eventCount ? eventCount.eventRows : null,
      sessionMaxSeq: eventCount ? eventCount.maxSeq : null,
      eventStorePath: storePath || null,
      idleMs: opts.idleMs,
      timeoutMs: opts.timeoutMs,
      node: process.version,
      measured_at: new Date().toISOString(),
    },
  };

  const json = `${JSON.stringify(result, null, 2)}\n`;
  process.stdout.write(json);
  if (opts.out) {
    try {
      writeFileSync(opts.out, json, 'utf8');
      process.stderr.write(`结果已写入 ${opts.out}\n`);
    } catch (err) {
      fail(`写入 --out 失败: ${err.message}`, 4);
    }
  }
  if (stats.bytes === 0) {
    fail('连接成功但未收到任何响应体字节（流式读取失败）', 5);
  }
}

main().catch((err) => {
  if (err instanceof MeasureError) fail(err.message, err.exitCode);
  fail(`未预期的错误: ${err?.stack || err}`, 1);
});
