#!/usr/bin/env node
/**
 * 轨迹 JSONL 离线分析（P3-3）：输入 P3-2 导出的 JSONL（或 EventStore 拉取转存），
 * 输出稳定诊断指标：
 *
 *   - TTFT（首个文本 token 时延）
 *   - 工具耗时（tool_start → tool_end 按 tool.id 配对）
 *   - 重试率（orchestration 的 route_attempted/fallback_reason + 工具同名重复）
 *   - token 分布（done.result.usage）
 *   - reasoning 占比（reasoning 内容字符 / 文本总字符）
 *
 * 用法：
 *   node scripts/analyze-trajectory.mjs <file.jsonl>          # 人类可读
 *   node scripts/analyze-trajectory.mjs <file.jsonl> --json    # 结构化输出
 *   cat file.jsonl | node scripts/analyze-trajectory.mjs -     # stdin
 *
 * 容错：空行/损坏行跳过并计数，不中断分析；字段缺失按 0/空处理。
 */
import { readFileSync } from "node:fs";

const isJsonOutput = process.argv.includes("--json");
const inputArg = process.argv
  .slice(2)
  .find((arg) => arg !== "--json" && !arg.startsWith("--"));

const RAW_LIMIT = 8 * 1024 * 1024;

function num(value) {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function str(value) {
  return typeof value === "string" ? value : "";
}

function obj(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? value : {};
}

function parseTs(value) {
  const text = str(value);
  if (!text) return Number.NaN;
  const ms = Date.parse(text);
  return Number.isFinite(ms) ? ms : Number.NaN;
}

/** 读取输入：文件路径（- 表示 stdin）。 */
function readInput(path) {
  if (!path || path === "-") {
    return readFileSync(0, "utf8");
  }
  return readFileSync(path, "utf8");
}

/** 解析 JSONL → 行数组；容错返回 { entries, skipped }。 */
export function parseTrajectoryJsonl(text) {
  const entries = [];
  let skipped = 0;
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line) continue;
    try {
      const parsed = JSON.parse(line);
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
        skipped += 1;
        continue;
      }
      entries.push(parsed);
    } catch {
      skipped += 1;
    }
  }
  return { entries, skipped };
}

/** 提取文本 chunk 内容（chunk 事件，type=text）。 */
function textChunkContent(entry) {
  if (entry.kind !== "chunk") return "";
  const payload = obj(entry.payload);
  if (str(payload.type) === "text" && str(payload.content)) {
    return str(payload.content);
  }
  return str(obj(payload.text).content);
}

/** 提取 reasoning 内容（reasoning 事件）。 */
function reasoningContent(entry) {
  if (entry.kind !== "reasoning") return "";
  const payload = obj(entry.payload);
  if (str(payload.content)) return str(payload.content);
  return str(obj(payload.reasoning).content) || str(obj(payload.reasoning).delta);
}

/** 提取工具身份：id 优先 tool.id，name 兜底。 */
function toolIdentity(entry) {
  const payload = obj(entry.payload);
  const tool = obj(payload.tool);
  const toolCall = obj(payload.tool_call);
  const id = str(tool.id) || str(toolCall.id);
  const name =
    str(tool.name) || str(obj(toolCall.function).name) || str(payload.tool_name);
  return { id, name };
}

/** 收集所有 done 事件中的 orchestration payload 与 usage。 */
function collectDoneMeta(entries) {
  const orchestrations = [];
  const usages = [];
  let doneCount = 0;
  for (const entry of entries) {
    if (entry.kind !== "done") continue;
    doneCount += 1;
    const result = obj(obj(entry.payload).result);
    if (obj(result.orchestration)) {
      orchestrations.push(obj(result.orchestration));
    }
    if (obj(result.usage)) {
      usages.push(obj(result.usage));
    }
  }
  return { orchestrations, usages, doneCount };
}

/** 核心分析：输入解析后的条目 → 指标对象。 */
export function analyzeTrajectoryEntries(entries) {
  const kinds = new Map();
  let firstTs = Number.NaN;
  let lastTs = Number.NaN;
  let metaTs = Number.NaN;
  let firstTextChunkTs = Number.NaN;
  let reasoningChars = 0;
  let textChars = 0;

  const toolStarts = new Map(); // id → { entry, ts }
  const toolDurations = []; // { id, name, durationMs }
  const openTools = []; // 未配对 start
  const toolStartCounts = new Map(); // name → count

  for (const entry of entries) {
    const ts = parseTs(entry.ts);
    if (Number.isFinite(ts)) {
      if (!Number.isFinite(firstTs)) firstTs = ts;
      lastTs = ts;
    }
    kinds.set(entry.kind, (kinds.get(entry.kind) ?? 0) + 1);

    if (entry.kind === "meta" && !Number.isFinite(metaTs)) {
      metaTs = ts;
    }

    const text = textChunkContent(entry);
    if (text) {
      textChars += text.length;
      if (!Number.isFinite(firstTextChunkTs)) firstTextChunkTs = ts;
    }
    reasoningChars += reasoningContent(entry).length;

    if (entry.kind === "tool_start") {
      const { id, name } = toolIdentity(entry);
      const key = id || `${name}:${toolStartCounts.get(name) ?? 0}`;
      toolStarts.set(key, { entry, ts, name: name || id });
      toolStartCounts.set(name, (toolStartCounts.get(name) ?? 0) + 1);
    } else if (entry.kind === "tool_end") {
      const { id, name } = toolIdentity(entry);
      const key = id || `${name}:${toolStartCounts.get(name) ?? 0}`;
      const start = toolStarts.get(key);
      if (start && Number.isFinite(start.ts) && Number.isFinite(ts)) {
        toolDurations.push({
          id: id || key,
          name: start.name,
          durationMs: Math.max(0, ts - start.ts),
        });
        toolStarts.delete(key);
      } else {
        openTools.push({ id: id || key, name, endedAt: entry.ts });
      }
    }
  }

  const { orchestrations, usages, doneCount } = collectDoneMeta(entries);
  const fallbackCount = orchestrations.filter(
    (item) => str(item.fallback_reason).length > 0,
  ).length;
  const routeAttemptedCount = orchestrations.filter(
    (item) => item.route_attempted === true,
  ).length;
  const totalOrchestrations = orchestrations.length;

  const repeatedTools = [...toolStartCounts.entries()].filter(
    ([, count]) => count > 1,
  );

  const usage = usages.reduce(
    (acc, item) => {
      acc.promptTokens += num(item.prompt_tokens);
      acc.completionTokens += num(item.completion_tokens);
      acc.totalTokens += num(item.total_tokens);
      acc.cachedTokens += num(item.cached_tokens);
      acc.reasoningTokens += num(item.reasoning_tokens);
      return acc;
    },
    {
      promptTokens: 0,
      completionTokens: 0,
      totalTokens: 0,
      cachedTokens: 0,
      reasoningTokens: 0,
    },
  );

  const totalText = reasoningChars + textChars;
  const toolTotalMs = toolDurations.reduce((sum, item) => sum + item.durationMs, 0);

  return {
    summary: {
      eventCount: entries.length,
      kindDistribution: Object.fromEntries(kinds),
      doneCount,
      durationMs: Number.isFinite(firstTs) && Number.isFinite(lastTs)
        ? Math.max(0, lastTs - firstTs)
        : 0,
    },
    ttftMs: Number.isFinite(metaTs) && Number.isFinite(firstTextChunkTs)
      ? Math.max(0, firstTextChunkTs - metaTs)
      : (Number.isFinite(firstTs) && Number.isFinite(firstTextChunkTs)
          ? Math.max(0, firstTextChunkTs - firstTs)
          : 0),
    tools: {
      callCount: toolStartCounts.size,
      completedCount: toolDurations.length,
      openCount: openTools.length,
      totalDurationMs: toolTotalMs,
      avgDurationMs: toolDurations.length > 0
        ? Math.round(toolTotalMs / toolDurations.length)
        : 0,
      perTool: toolDurations.map((item) => ({
        id: item.id,
        name: item.name,
        durationMs: item.durationMs,
      })),
    },
    retries: {
      fallbackCount,
      routeAttemptedCount,
      totalOrchestrations,
      repeatedToolNames: repeatedTools.map(([name, count]) => ({ name, count })),
    },
    tokens: usage,
    reasoning: {
      reasoningChars,
      textChars,
      ratio: totalText > 0 ? reasoningChars / totalText : 0,
    },
    skippedLines: 0,
  };
}

/** 主入口：读文件 → 分析 → 输出。 */
export function analyzeTrajectoryJsonl(text) {
  const { entries, skipped } = parseTrajectoryJsonl(text);
  const result = analyzeTrajectoryEntries(entries);
  result.skippedLines = skipped;
  return result;
}

function formatHuman(result) {
  const s = result.summary;
  const lines = [
    "== Trajectory analysis ==",
    `events: ${s.eventCount} (done: ${s.doneCount})  duration: ${s.durationMs}ms  skipped lines: ${result.skippedLines}`,
    `kinds: ${Object.entries(s.kindDistribution)
      .map(([kind, count]) => `${kind}=${count}`)
      .join(", ")}`,
    `TTFT: ${result.ttftMs}ms`,
    `tools: ${result.tools.callCount} called, ${result.tools.completedCount} completed, ${result.tools.openCount} open; total ${result.tools.totalDurationMs}ms, avg ${result.tools.avgDurationMs}ms`,
    ...result.tools.perTool.map(
      (item) => `  - ${item.name} (${item.id}): ${item.durationMs}ms`,
    ),
    `retries: ${result.retries.fallbackCount} fallback(s), ${result.retries.routeAttemptedCount}/${result.retries.totalOrchestrations} routes attempted`,
    ...result.retries.repeatedToolNames.map(
      (item) => `  - repeated tool: ${item.name} x${item.count}`,
    ),
    `tokens: prompt=${result.tokens.promptTokens} completion=${result.tokens.completionTokens} total=${result.tokens.totalTokens} cached=${result.tokens.cachedTokens} reasoning=${result.tokens.reasoningTokens}`,
    `reasoning: ${result.reasoning.reasoningChars} chars / ${result.reasoning.textChars} text chars = ${(result.reasoning.ratio * 100).toFixed(1)}%`,
  ];
  return lines.join("\n");
}

if (process.argv[1] && import.meta.url.endsWith(process.argv[1].replace(/\\/g, "/"))) {
  try {
    const text = readInput(inputArg);
    const result = analyzeTrajectoryJsonl(text);
    if (isJsonOutput) {
      process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    } else {
      process.stdout.write(`${formatHuman(result)}\n`);
    }
  } catch (error) {
    process.stderr.write(`analyze-trajectory: ${error.message}\n`);
    process.exit(1);
  }
}
