import { mkdirSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { ARTIFACTS_DIR, resetMockState, seedSessionHistory } from "./support";

// 诊断探针（非验收用例）：量化流式输出期间主线程到底在干什么。
//
// 采集：
//   1. `PerformanceObserver("longtask")` —— 阻塞主线程 >50ms 的任务（卡顿的直接证据）；
//   2. rAF 帧间隔分布 —— 掉帧幅度；
//   3. CDP `Performance.getMetrics` 增量 —— Script / Layout / RecalcStyle 各自耗时；
//   4. CDP `Profiler` CPU 采样 —— 自耗时 / 包含耗时 Top-N + 按 bundle 归因 + 热点调用栈。
//   5. Chrome trace（devtools.timeline + stack）—— 「样式重算 / 布局」由谁触发、每次波及多少节点；
//      并用长任务窗口 × CPU 采样还原出「单次卡顿里究竟在跑什么」。
//
// 环境变量：
//   PERF_ROUNDS  历史轮数（默认 40，0 = 空会话）—— 用于区分「整列重渲染」与「流式行内部」开销
//   PERF_TAG     报告文件名后缀（默认 report）
//
// 跑法：npm run test:manual -- e2e/zz-perf-probe.manual.ts

const composer = (page: Page) => page.locator(".app-chat-input");
const log = (page: Page) => page.locator('[role="log"]');

const HISTORY_ROUNDS = Number(process.env.PERF_ROUNDS ?? "40");
const TAG = process.env.PERF_TAG ?? "report";

/** 目录树里最新的 mtime（毫秒）；目录不存在时返回 0。 */
function newestMtime(dir: string): number {
  let newest = 0;
  let entries: ReturnType<typeof readdirSync>;
  try {
    entries = readdirSync(dir, { withFileTypes: true });
  } catch {
    return 0;
  }
  for (const entry of entries) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      newest = Math.max(newest, newestMtime(path));
      continue;
    }
    // 只统计会进 bundle 的源码：测试文件（*.test.ts(x)）不进产物，别的会话顺手
    // 跑一次单测就会把这份新鲜度检查变成常亮红灯（实测踩过）。
    if (/\.test\.[cm]?tsx?$/.test(entry.name)) {
      continue;
    }
    newest = Math.max(newest, statSync(path).mtimeMs);
  }
  return newest;
}

/**
 * 探针跑的是 `vite preview` 提供的 `dist/`：源码改了但没 `npm run build` 时，
 * 报告里的产物哈希不会变，A/B 就变成「同一份 bundle 的两次噪声」。
 * 2026-09 实测踩过这个坑，因此把新鲜度写进报告与控制台。
 */
function buildFreshness() {
  const srcMs = newestMtime(join(process.cwd(), "src"));
  const distMs = newestMtime(join(process.cwd(), "dist", "assets"));
  return { srcMs: Math.round(srcMs), distMs: Math.round(distMs), stale: srcMs > distMs };
}

interface PerfBag {
  longTasks: Array<{ start: number; dur: number }>;
  frames: number[];
  mutations: {
    total: number;
    byType: Record<string, number>;
    byTarget: Record<string, number>;
    perFrame: number[];
    spikes: Array<{ t: number; n: number; top: string; topCount: number; byType: Record<string, number> }>;
    /**
     * 被删除的子树：React 每 chunk 重建一块 DOM 的唯一直接证据。
     * 只采样前 60 次（outerHTML 截断 140 字符），避免观测本身变成瓶颈。
     */
    removals: {
      count: number;
      nodes: number;
      byTarget: Record<string, number>;
      byTag: Record<string, number>;
      samples: Array<{ t: number; target: string; removed: number; tags: string; html: string }>;
    };
  };
  raf: number;
}

interface MetricsResult {
  metrics: Array<{ name: string; value: number }>;
}

interface ProfileCallFrame {
  functionName: string;
  url: string;
  lineNumber: number;
  columnNumber: number;
}

interface ProfileNode {
  id: number;
  hitCount?: number;
  children?: number[];
  callFrame: ProfileCallFrame;
}

interface Profile {
  nodes: ProfileNode[];
  samples?: number[];
  timeDeltas?: number[];
  startTime: number;
  endTime: number;
}

function metricsMap(result: MetricsResult): Record<string, number> {
  return Object.fromEntries(result.metrics.map((m) => [m.name, m.value]));
}

function pickMetrics(map: Record<string, number>) {
  const keys = [
    "ScriptDuration",
    "LayoutDuration",
    "RecalcStyleDuration",
    "TaskDuration",
    "LayoutCount",
    "RecalcStyleCount",
    "JSHeapUsedSize",
    "Nodes",
  ];
  return Object.fromEntries(keys.map((k) => [k, map[k] ?? 0]));
}

function diffMetrics(
  before: Record<string, number>,
  after: Record<string, number>,
): Record<string, number> {
  const out: Record<string, number> = {};
  for (const key of Object.keys(after)) {
    const delta = (after[key] ?? 0) - (before[key] ?? 0);
    out[key] = key.endsWith("Duration") ? Math.round(delta * 1000 * 10) / 10 : Math.round(delta);
  }
  return out;
}

function percentile(sorted: number[], ratio: number): number {
  if (sorted.length === 0) return 0;
  return sorted[Math.min(sorted.length - 1, Math.floor(sorted.length * ratio))];
}

function shortUrl(url: string): string {
  return url
    .replace(/^https?:\/\/[^/]+/, "")
    .replace(/^webpack:\/\//, "")
    .replace(/^.*\/assets\//, "assets/");
}

function labelOf(frame: ProfileCallFrame): string {
  const fn = frame.functionName || "(anonymous)";
  return `${fn} (${shortUrl(frame.url)}:${frame.lineNumber + 1})`;
}

/** 采样数 → 毫秒（intervalMs 为 CDP 采样间隔）。 */
function aggregateProfile(profile: Profile, intervalMs: number) {
  const nodes = new Map<number, ProfileNode>();
  for (const node of profile.nodes) nodes.set(node.id, node);

  const selfMs = new Map<number, number>();
  for (const node of profile.nodes) {
    const samples = node.hitCount ?? 0;
    if (samples > 0) selfMs.set(node.id, samples * intervalMs);
  }

  const parentOf = new Map<number, number>();
  for (const node of profile.nodes) {
    for (const child of node.children ?? []) parentOf.set(child, node.id);
  }

  // 自耗时 Top-N（排除 V8 伪节点，保留真实业务/库函数）
  const isPseudo = (label: string) =>
    label.startsWith("(program)") ||
    label.startsWith("(idle)") ||
    label.startsWith("(garbage collector)") ||
    label.startsWith("(root)") ||
    label.endsWith("(:0)");
  const selfTop = [...selfMs.entries()]
    .map(([id, ms]) => ({ id, ms, label: labelOf(nodes.get(id)!.callFrame) }))
    .filter((entry) => entry.ms >= 8 && !isPseudo(entry.label))
    .sort((a, b) => b.ms - a.ms)
    .slice(0, 30)
    .map((entry) => ({ ...entry, ms: Math.round(entry.ms * 10) / 10 }));

  // 包含耗时：从 root 起后序累加
  const inclusiveMs = new Map<number, number>();
  const root = profile.nodes[0];
  const computeInclusive = (id: number): number => {
    const cached = inclusiveMs.get(id);
    if (cached !== undefined) return cached;
    const node = nodes.get(id);
    if (!node) return 0;
    let total = selfMs.get(id) ?? 0;
    for (const child of node.children ?? []) total += computeInclusive(child);
    inclusiveMs.set(id, total);
    return total;
  };
  if (root) computeInclusive(root.id);

  const inclusiveTop = root
    ? (root.children ?? [])
        .map((id) => ({
          id,
          ms: Math.round((inclusiveMs.get(id) ?? 0) * 10) / 10,
          label: labelOf(nodes.get(id)!.callFrame),
        }))
        .sort((a, b) => b.ms - a.ms)
        .slice(0, 20)
    : [];

  // 按 bundle / URL 自耗时归因
  const byUrl = new Map<string, number>();
  for (const [id, ms] of selfMs) {
    const url = shortUrl(nodes.get(id)!.callFrame.url) || "(native)";
    byUrl.set(url, (byUrl.get(url) ?? 0) + ms);
  }
  const byUrlTop = [...byUrl.entries()]
    .map(([url, ms]) => ({ url, ms: Math.round(ms * 10) / 10 }))
    .sort((a, b) => b.ms - a.ms)
    .slice(0, 15);

  // 热点函数的完整调用栈（自顶向下），用于回答「谁在调它」
  const chainOf = (id: number): string[] => {
    const chain: string[] = [];
    let cursor: number | undefined = id;
    while (cursor !== undefined && chain.length < 40) {
      const node = nodes.get(cursor);
      if (!node) break;
      chain.push(labelOf(node.callFrame));
      cursor = parentOf.get(cursor);
    }
    return chain;
  };
  const hottestChains = selfTop.slice(0, 12).map((entry) => ({
    label: entry.label,
    ms: entry.ms,
    stack: chainOf(entry.id).reverse(),
  }));

  // 采样时钟实况：请求 0.3ms 并不意味着真能采到 0.3ms，长任务归因必须用它校准。
  const spanMs = (profile.endTime - profile.startTime) / 1000;
  const sampleCount = profile.samples?.length ?? 0;
  const sampleStats = {
    count: sampleCount,
    spanMs: Math.round(spanMs),
    avgDeltaMs: sampleCount > 0 ? Math.round((spanMs / sampleCount) * 1000) / 1000 : intervalMs,
  };

  return { sampleStats, selfTop, inclusiveTop, byUrlTop, hottestChains };
}

/**
 * 把长任务（>50ms 阻塞）与 CPU profile 采样对齐，回答「这次卡顿里跑的是什么」。
 * 采样时刻 = profile.startTime/1000 + clockOffset + Σ timeDeltas（µs）。
 */
function attributeLongTasks(
  profile: Profile,
  intervalMs: number,
  longTasks: Array<{ start: number; dur: number }>,
  clockOffsetMs: number,
  topN: number,
) {
  const nodes = new Map<number, ProfileNode>();
  for (const node of profile.nodes) nodes.set(node.id, node);

  const samples = profile.samples ?? [];
  const deltas = profile.timeDeltas ?? [];
  const spanMs = (profile.endTime - profile.startTime) / 1000;
  const weight = samples.length > 0 ? spanMs / samples.length : intervalMs;
  const times: number[] = new Array(samples.length);
  let cursor = profile.startTime / 1000 + clockOffsetMs;
  for (let i = 0; i < samples.length; i += 1) {
    times[i] = cursor;
    cursor += (deltas[i] ?? 0) / 1000;
  }

  return [...longTasks]
    .sort((a, b) => b.dur - a.dur)
    .slice(0, topN)
    .map((task) => {
      const from = task.start;
      const to = task.start + task.dur;
      const byLabel = new Map<string, number>();
      let count = 0;
      for (let i = 0; i < times.length; i += 1) {
        const at = times[i];
        if (at < from) continue;
        if (at > to) break;
        const node = nodes.get(samples[i]);
        if (!node) continue;
        const label = labelOf(node.callFrame);
        byLabel.set(label, (byLabel.get(label) ?? 0) + weight);
        count += 1;
      }
      return {
        start: Math.round(task.start),
        dur: Math.round(task.dur),
        samples: count,
        coveredMs: Math.round(count * weight),
        expectedSamples: Math.round(task.dur / weight),
        top: [...byLabel.entries()]
          .map(([label, ms]) => ({ label, ms: Math.round(ms * 10) / 10 }))
          .sort((a, b) => b.ms - a.ms)
          .slice(0, 6),
      };
    });
}

interface TraceEvent {
  name: string;
  dur?: number;
  ts?: number;
  args?: {
    elementCount?: number;
    beginData?: {
      stackTrace?: TraceFrame[];
      dirtyObjects?: number;
      totalObjects?: number;
      partialLayout?: boolean;
    };
    data?: {
      stackTrace?: TraceFrame[];
      /** 失效追踪：cause=Class/Attribute/Style/... ，nodeName=被失效的元素。 */
      cause?: string;
      type?: string;
      nodeName?: string;
      extraData?: Record<string, unknown>;
    };
  };
}

interface TraceFrame {
  functionName?: string;
  url?: string;
  lineNumber?: number;
}

// 只保留回答「谁触发样式重算/布局、波及多少节点」所需的事件，其余仅计数，防止 trace 撑爆内存。
const TRACE_NAMES = new Set([
  "UpdateLayoutTree",
  "RecalcStyle",
  "Layout",
  "InvalidateLayout",
  "StyleRecalcInvalidationTracking",
  "StyleRecalcInvalidation",
  "LayoutInvalidationTracking",
  "Paint",
  "PrePaint",
  "Layerize",
  "CompositeLayers",
  "UpdateLayer",
  "HitTest",
  "IntersectionObserverController::computeIntersections",
  "EventDispatch",
  "ParseHTML",
]);

/**
 * 把 trace 事件按「长任务窗口」切分：回答单次卡顿里渲染管线各阶段各占多少。
 * trace `ts` 与 CPU profile 同源（单调时钟微秒），用实测 offset 映射到页面时钟。
 */
function traceWindowBreakdown(
  events: TraceEvent[],
  windows: Array<{ start: number; dur: number }>,
  clockOffsetMs: number,
  topN: number,
) {
  return [...windows]
    .sort((a, b) => b.dur - a.dur)
    .slice(0, topN)
    .map((window) => {
      const byName = new Map<string, { count: number; ms: number }>();
      for (const event of events) {
        if (event.ts === undefined) continue;
        const from = event.ts / 1000 + clockOffsetMs;
        const to = from + (event.dur ?? 0) / 1000;
        if (to < window.start || from > window.start + window.dur) continue;
        const entry = byName.get(event.name) ?? { count: 0, ms: 0 };
        entry.count += 1;
        entry.ms += (event.dur ?? 0) / 1000;
        byName.set(event.name, entry);
      }
      return {
        taskStart: Math.round(window.start),
        taskDur: Math.round(window.dur),
        events: [...byName.entries()]
          .map(([name, value]) => ({
            name,
            count: value.count,
            ms: Math.round(value.ms * 10) / 10,
          }))
          .sort((a, b) => b.ms - a.ms),
      };
    });
}

function traceStackTop(event: TraceEvent): string {
  const frames = event.args?.beginData?.stackTrace ?? event.args?.data?.stackTrace ?? [];
  if (frames.length === 0) return "(no-stack)";
  const frame = frames.find((f) => (f.url ?? "").length > 0) ?? frames[0];
  const where = shortUrl(frame.url ?? "");
  return `${frame.functionName || "(anonymous)"} @ ${where || "(native)"}:${frame.lineNumber ?? 0}`;
}

/** 把样式重算/布局归因到触发它的 JS 调用点 + 每次波及的节点数。 */
function summarizeTrace(events: TraceEvent[]) {
  const byName = new Map<string, { count: number; ms: number; elements: number }>();
  const styleEvents: Array<{ durMs: number; elementCount: number; caller: string }> = [];
  const layouts: Array<{ durMs: number; dirtyObjects: number; totalObjects: number; partial: boolean; caller: string }> = [];
  const callerMs = new Map<string, number>();
  /**
   * 失效追踪：`StyleRecalcInvalidationTracking` 带 cause（Class/Attribute/Style/
   * 样式表…）+ 触发它的 JS 调用栈。它回答的是「谁把样式弄脏、波及哪些节点」，
   * 而不是「重算花了多久」——前者才是能改的地方。
   */
  const invalidationByCause = new Map<string, { count: number; nodes: Set<string> }>();
  const invalidationByCaller = new Map<string, { count: number; causes: Map<string, number> }>();
  /** 前 3 条原始失效事件（字段名未知时用来自查，避免猜错 cause 取值路径）。 */
  const invalidationRawSamples: string[] = [];

  for (const event of events) {
    const durMs = (event.dur ?? 0) / 1000;
    const entry = byName.get(event.name) ?? { count: 0, ms: 0, elements: 0 };
    entry.count += 1;
    entry.ms += durMs;
    entry.elements += event.args?.elementCount ?? 0;
    byName.set(event.name, entry);

    if (event.name === "StyleRecalcInvalidationTracking" || event.name === "LayoutInvalidationTracking") {
      if (invalidationRawSamples.length < 3) {
        invalidationRawSamples.push(`${event.name}: ${JSON.stringify(event.args ?? {}).slice(0, 400)}`);
      }
      const cause = event.args?.data?.cause ?? event.args?.data?.type ?? "(unknown)";
      const nodeName = event.args?.data?.nodeName ?? "?";
      const causeEntry = invalidationByCause.get(cause) ?? { count: 0, nodes: new Set<string>() };
      causeEntry.count += 1;
      causeEntry.nodes.add(nodeName);
      invalidationByCause.set(cause, causeEntry);
      const caller = traceStackTop(event);
      const callerEntry = invalidationByCaller.get(caller) ?? { count: 0, causes: new Map<string, number>() };
      callerEntry.count += 1;
      callerEntry.causes.set(cause, (callerEntry.causes.get(cause) ?? 0) + 1);
      invalidationByCaller.set(caller, callerEntry);
    }

    if (event.name !== "Layout" && event.name !== "UpdateLayoutTree" && event.name !== "RecalcStyle") continue;
    const caller = traceStackTop(event);
    callerMs.set(caller, (callerMs.get(caller) ?? 0) + durMs);
    styleEvents.push({ durMs: Math.round(durMs * 100) / 100, elementCount: event.args?.elementCount ?? 0, caller });
    if (event.name === "Layout") {
      layouts.push({
        durMs: Math.round(durMs * 100) / 100,
        dirtyObjects: event.args?.beginData?.dirtyObjects ?? -1,
        totalObjects: event.args?.beginData?.totalObjects ?? -1,
        partial: event.args?.beginData?.partialLayout === true,
        caller,
      });
    }
  }

  // 每次布局重排了多少个对象 —— 直接回答「是整页重排还是局部重排」。
  const dirty = layouts
    .map((entry) => entry.dirtyObjects)
    .filter((value) => value >= 0)
    .sort((a, b) => a - b);

  return {
    byName: [...byName.entries()]
      .map(([name, v]) => ({ name, count: v.count, ms: Math.round(v.ms * 10) / 10, elements: v.elements }))
      .sort((a, b) => b.ms - a.ms),
    callerMs: [...callerMs.entries()]
      .map(([caller, ms]) => ({ caller, ms: Math.round(ms * 10) / 10 }))
      .sort((a, b) => b.ms - a.ms)
      .slice(0, 10),
    worstStyleEvents: [...styleEvents].sort((a, b) => b.durMs - a.durMs).slice(0, 12),
    styleElementCountTotal: styleEvents.reduce((sum, e) => sum + e.elementCount, 0),
    styleInvalidationTop: [...invalidationByCause.entries()]
      .map(([cause, v]) => ({ cause, count: v.count, nodes: [...v.nodes].slice(0, 6) }))
      .sort((a, b) => b.count - a.count)
      .slice(0, 10),
    styleInvalidationByCaller: [...invalidationByCaller.entries()]
      .map(([caller, v]) => ({
        caller,
        count: v.count,
        causes: [...v.causes.entries()].sort((a, b) => b[1] - a[1]).slice(0, 3).map(([cause, count]) => `${cause}×${count}`),
      }))
      .sort((a, b) => b.count - a.count)
      .slice(0, 10),
    styleInvalidationRawSamples: invalidationRawSamples,
    layoutStats: {
      count: layouts.length,
      partialCount: layouts.filter((entry) => entry.partial).length,
      dirtyP50: percentile(dirty, 0.5),
      dirtyP95: percentile(dirty, 0.95),
      dirtyMax: dirty[dirty.length - 1] ?? 0,
      totalObjectsMax: layouts.reduce((max, entry) => Math.max(max, entry.totalObjects), 0),
      worst: [...layouts].sort((a, b) => b.durMs - a.durMs).slice(0, 8),
    },
  };
}

function buildHistory(rounds: number): Array<Record<string, unknown>> {
  const history: Array<Record<string, unknown>> = [];
  for (let i = 0; i < rounds; i += 1) {
    history.push({
      role: "user",
      content: `Question ${i}: how is the session event window bounded?`,
    });
    history.push({
      role: "assistant",
      content:
        `Answer ${i}: the window is bounded and tail-first.\n\n` +
        "```ts\nconst window = pickTail(events, size);\n```\n\n" +
        "- merges deltas by sequence\n- keeps the tail warm\n- drops nothing silently\n",
    });
  }
  return history;
}

test("perf probe: streaming main-thread cost", async ({ page }) => {
  test.setTimeout(240_000);
  // 可选 A/B：给 rAF 加固定延迟（PERF_RAF_DELAY=16 → 逐帧揭示降到 ~30fps），
  // 用来分辨剩余成本是「随帧数走」（逐帧揭示 / 调度）还是「随 chunk 走」（解析 / 提交）。
  if (process.env.PERF_RAF_DELAY) {
    const delayMs = Number(process.env.PERF_RAF_DELAY);
    await page.addInitScript((ms: number) => {
      const original = window.requestAnimationFrame.bind(window);
      window.requestAnimationFrame = (callback: FrameRequestCallback): number =>
        window.setTimeout(() => original(callback), ms) as unknown as number;
      window.cancelAnimationFrame = (handle: number) => window.clearTimeout(handle);
    }, delayMs);
  }
  await resetMockState(page.request);
  await page.goto("/workspace");
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });

  // 1) 热身一轮：首页线程先拿到 sessionId（历史 sync 的前置条件）。
  await composer(page).fill("capital check");
  await composer(page).press("Control+Enter");
  await expect(page.getByText("The capital of France is Paris.").first()).toBeVisible({
    timeout: 30_000,
  });

  // 2) 注入历史并刷新 → 控制会话规模。
  await seedSessionHistory(page.request, "e2e-session-1", buildHistory(HISTORY_ROUNDS));
  await page.reload();
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });
  if (HISTORY_ROUNDS > 0) {
    await expect(log(page)).toContainText(`Answer ${HISTORY_ROUNDS - 1}`, { timeout: 30_000 });
  }

  const rows = await page.locator("[role='log'] [data-message-id]").count();
  const domNodes = await page.evaluate(() => document.querySelectorAll("*").length);

  // 可选：注入样式做 A/B（例如布局包含：PERF_STYLE='[role="log"] article{contain:layout style}'）
  if (process.env.PERF_STYLE) {
    await page.addStyleTag({ content: process.env.PERF_STYLE });
  }

  // 3) 采样器：长任务 + 帧间隔 + DOM 变更（样式/布局失效的源头）。
  await page.evaluate(() => {
    const describeTarget = (node: Node): string => {
      const el = node instanceof Element ? node : node.parentElement;
      if (!el) return "(detached)";
      const cls = typeof el.className === "string" ? el.className.split(/\s+/).filter(Boolean).slice(0, 2).join(".") : "";
      const owner = el.closest("[data-message-id]");
      // 带上消息 id 尾号：定型帧的 593 个变更到底是「一条消息内部」还是「整列 80 行」
      // 全靠这个区分（<msg> 无法分辨）。
      if (owner) {
        const id = (owner as HTMLElement).dataset.messageId ?? "";
        return `${el.tagName.toLowerCase()}${cls ? "." + cls : ""}<msg:${id.slice(-6)}>`;
      }
      if (el.closest("[role='log']")) return `${el.tagName.toLowerCase()}${cls ? "." + cls : ""}<log>`;
      const region = el.closest("aside,nav,header,footer,form,section");
      return `${el.tagName.toLowerCase()}${cls ? "." + cls : ""}<${region ? region.tagName.toLowerCase() : "page"}>`;
    };
    const bag = {
      longTasks: [] as Array<{ start: number; dur: number }>,
      frames: [] as number[],
      mutations: {
        total: 0,
        byType: {} as Record<string, number>,
        byTarget: {} as Record<string, number>,
        perFrame: [] as number[],
        spikes: [] as Array<{ t: number; n: number; top: string; topCount: number; byType: Record<string, number> }>,
        removals: {
          count: 0,
          nodes: 0,
          byTarget: {} as Record<string, number>,
          byTag: {} as Record<string, number>,
          samples: [] as Array<{ t: number; target: string; removed: number; tags: string; html: string }>,
        },
      },
      raf: 0,
    };
    (window as unknown as { __perf: typeof bag }).__perf = bag;
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        bag.longTasks.push({ start: Math.round(entry.startTime), dur: Math.round(entry.duration) });
      }
    }).observe({ entryTypes: ["longtask"] });
    let sinceFrame = 0;
    let frameTargets: Record<string, number> = {};
    let frameTypes: Record<string, number> = {};
    new MutationObserver((records) => {
      for (const record of records) {
        bag.mutations.total += 1;
        sinceFrame += 1;
        bag.mutations.byType[record.type] = (bag.mutations.byType[record.type] ?? 0) + 1;
        const key = describeTarget(record.target);
        bag.mutations.byTarget[key] = (bag.mutations.byTarget[key] ?? 0) + 1;
        frameTargets[key] = (frameTargets[key] ?? 0) + 1;
        frameTypes[record.type] = (frameTypes[record.type] ?? 0) + 1;
        if (record.type === "childList" && record.removedNodes.length > 0) {
          const removals = bag.mutations.removals;
          removals.count += 1;
          removals.nodes += record.removedNodes.length;
          removals.byTarget[key] = (removals.byTarget[key] ?? 0) + record.removedNodes.length;
          for (const node of record.removedNodes) {
            const tag = node instanceof Element ? node.tagName.toLowerCase() : "#text";
            removals.byTag[tag] = (removals.byTag[tag] ?? 0) + 1;
          }
          if (removals.samples.length < 60) {
            const first = record.removedNodes[0];
            removals.samples.push({
              t: Math.round(performance.now()),
              target: key,
              removed: record.removedNodes.length,
              tags: [...record.removedNodes]
                .slice(0, 3)
                .map((node) => (node instanceof Element ? node.tagName.toLowerCase() : "#text"))
                .join(","),
              html: (first instanceof Element ? first.outerHTML : (first.textContent ?? "")).slice(0, 140),
            });
          }
        }
      }
    }).observe(document.documentElement, {
      subtree: true,
      childList: true,
      characterData: true,
      attributes: true,
    });
    let last = performance.now();
    const tick = (now: number) => {
      bag.frames.push(Math.round(now - last));
      bag.mutations.perFrame.push(sinceFrame);
      if (sinceFrame >= 50) {
        const top = Object.entries(frameTargets).sort((a, b) => b[1] - a[1])[0] ?? ["", 0];
        bag.mutations.spikes.push({
          t: Math.round(now),
          n: sinceFrame,
          top: top[0],
          topCount: top[1],
          byType: { ...frameTypes },
        });
      }
      sinceFrame = 0;
      frameTargets = {};
      frameTypes = {};
      last = now;
      bag.raf = requestAnimationFrame(tick);
    };
    bag.raf = requestAnimationFrame(tick);
  });

  const cdp = await page.context().newCDPSession(page);
  await cdp.send("Profiler.enable");
  await cdp.send("Profiler.setSamplingInterval", { interval: 300 });
  await cdp.send("Performance.enable");
  const before = metricsMap(await cdp.send("Performance.getMetrics"));
  await cdp.send("Profiler.start");
  // 页面时钟锚点：用于把 profile 采样时刻映射到 performance.now() 时间轴。
  const profileWallStart = await page.evaluate(() => performance.now());

  // Chrome trace：回答「谁触发样式重算/布局、每次波及多少节点」。
  const traceEvents: TraceEvent[] = [];
  const traceNameCounts = new Map<string, number>();
  const traceCdp = await page.context().newCDPSession(page);
  traceCdp.on("Tracing.dataCollected", (payload: unknown) => {
    for (const event of (payload as { value?: TraceEvent[] }).value ?? []) {
      traceNameCounts.set(event.name, (traceNameCounts.get(event.name) ?? 0) + 1);
      if (TRACE_NAMES.has(event.name)) traceEvents.push(event);
    }
  });
  const traceComplete = new Promise<void>((resolve) => {
    traceCdp.on("Tracing.tracingComplete", () => resolve());
  });
  await traceCdp.send("Tracing.start", {
    // invalidationTracking 才会投递 StyleRecalcInvalidationTracking（cause + 调用栈），
    // 但它单次运行就有 1.7 万条事件、会显著抬高 script/longtask（实测 +1.3s script），
    // 因此只在 PERF_INVALIDATION=1 时开启：定性归因一次，量化用干净配置。
    categories: process.env.PERF_INVALIDATION
      ? "devtools.timeline,devtools.timeline.invalidationTracking," +
        "disabled-by-default-devtools.timeline.invalidationTracking,disabled-by-default-devtools.timeline.stack"
      : "devtools.timeline,disabled-by-default-devtools.timeline.stack",
    transferMode: "ReportEvents",
  });

  // 4) 压力流：约 525 帧 / 10ms / 21KB markdown。
  const startedAt = Date.now();
  // PERF_IDLE=1：不发起流式，只空转同长度窗口 —— 对照组，回答「这些长任务是不是流式造成的」。
  const idleMode = process.env.PERF_IDLE === "1";
  if (idleMode) {
    await page.waitForTimeout(11_000);
  } else {
    await composer(page).fill("perfmark stream load");
    await composer(page).press("Control+Enter");
    // 这里**不能**用 `expect(locator).toContainText()`：断言失败时 Playwright 会在页面里
    // 生成 ARIA snapshot（generateAriaTree → 逐节点 getCSSContent + isElementVisible
    // → 强制样式重算 + 强制布局）。它以 ~100ms 的节奏在整个流式窗口里轮询，等于往被测
    // 页面里插了一个「每 100ms 全量扫一遍 2700 节点」的负载 —— 实测单次窗口 514ms
    // 归因到 `expect`，并制造出每个长任务里 4~5 次 Layout。用 textContent 轮询替代：
    // 只遍历 DOM 树、不读几何、不触发重算布局。
    await page.waitForFunction(
      () => document.body.textContent?.includes("PERFMARK-END") ?? false,
      undefined,
      { timeout: 120_000, polling: 250 },
    );
  }
  const streamedMs = idleMode ? 0 : Date.now() - startedAt;
  await page.waitForTimeout(2000); // 定型 + done 后的整列重渲染也计入

  await traceCdp.send("Tracing.end");
  await traceComplete;
  const traceSummary = summarizeTrace(traceEvents);

  const after = metricsMap(await cdp.send("Performance.getMetrics"));
  const { profile } = (await cdp.send("Profiler.stop")) as { profile: Profile };
  const perf = await page.evaluate(() => {
    const bag = (window as unknown as { __perf: PerfBag }).__perf;
    cancelAnimationFrame(bag.raf);
    return { longTasks: bag.longTasks, frames: bag.frames, mutations: bag.mutations };
  });

  const frames = perf.frames.slice(1).sort((a, b) => a - b);
  const longTasks = perf.longTasks;
  const clockOffsetMs = profileWallStart - profile.startTime / 1000;
  const longTaskAttribution = attributeLongTasks(profile, 0.3, longTasks, clockOffsetMs, 4);
  const report = {
    capturedAt: new Date().toISOString(),
    scenario: { historyRounds: HISTORY_ROUNDS, rows, domNodes, streamedMs, idleMode },
    build: buildFreshness(),
    frames: {
      count: frames.length,
      p50: percentile(frames, 0.5),
      p95: percentile(frames, 0.95),
      max: frames[frames.length - 1] ?? 0,
      over33ms: frames.filter((gap) => gap > 33).length,
      over100ms: frames.filter((gap) => gap > 100).length,
    },
    longTasks: {
      count: longTasks.length,
      totalMs: longTasks.reduce((sum, t) => sum + t.dur, 0),
      maxMs: longTasks.reduce((max, t) => Math.max(max, t.dur), 0),
      over100ms: longTasks.filter((t) => t.dur > 100).length,
      top: [...longTasks].sort((a, b) => b.dur - a.dur).slice(0, 8),
    },
    domMutations: {
      total: perf.mutations.total,
      byType: perf.mutations.byType,
      removals: {
        count: perf.mutations.removals.count,
        nodes: perf.mutations.removals.nodes,
        topTargets: Object.entries(perf.mutations.removals.byTarget)
          .map(([target, count]) => ({ target, count }))
          .sort((a, b) => b.count - a.count)
          .slice(0, 6),
        topTags: Object.entries(perf.mutations.removals.byTag)
          .map(([tag, count]) => ({ tag, count }))
          .sort((a, b) => b.count - a.count)
          .slice(0, 8),
        samples: perf.mutations.removals.samples.slice(0, 25),
      },
      perFrame: {
        p50: percentile([...perf.mutations.perFrame].sort((a, b) => a - b), 0.5),
        p95: percentile([...perf.mutations.perFrame].sort((a, b) => a - b), 0.95),
        max: Math.max(...perf.mutations.perFrame, 0),
      },
      topTargets: Object.entries(perf.mutations.byTarget)
        .map(([target, count]) => ({ target, count }))
        .sort((a, b) => b.count - a.count)
        .slice(0, 12),
      spikes: perf.mutations.spikes.slice(0, 20),
      // 把「单帧突变爆发」与「长任务」对齐：卡顿帧到底在提交什么。
      spikesInLongTasks: longTasks
        .map((task) => ({
          taskStart: task.start,
          taskDur: task.dur,
          spikes: perf.mutations.spikes.filter((s) => s.t >= task.start - 16 && s.t <= task.start + task.dur),
        }))
        .filter((entry) => entry.spikes.length > 0),
    },
    cdpMetricsDelta: diffMetrics(pickMetrics(before), pickMetrics(after)),
    longTaskAttribution: {
      clockOffsetMs: Math.round(clockOffsetMs),
      tasks: longTaskAttribution,
    },
    longTaskTrace: traceWindowBreakdown(traceEvents, longTasks, clockOffsetMs, 4),
    mainThreadTrace: {
      topEventNames: [...traceNameCounts.entries()]
        .map(([name, count]) => ({ name, count }))
        .sort((a, b) => b.count - a.count)
        .slice(0, 25),
      ...traceSummary,
    },
    ...aggregateProfile(profile, 0.3),
  };

  const dir = join(ARTIFACTS_DIR, "perf");
  mkdirSync(dir, { recursive: true });
  const path = join(dir, `${TAG}.json`);
  writeFileSync(path, JSON.stringify(report, null, 2), "utf8");
  console.log(
    `[perf:${TAG}] rows=${rows} nodes=${domNodes} streamed=${streamedMs}ms ` +
      `frames(p95=${report.frames.p95} max=${report.frames.max} over100=${report.frames.over100ms}) ` +
      `longtasks=${report.longTasks.count}(max=${report.longTasks.maxMs}ms total=${report.longTasks.totalMs}ms) ` +
      `script=${report.cdpMetricsDelta.ScriptDuration}ms recalc=${report.cdpMetricsDelta.RecalcStyleDuration}ms ` +
      `layout=${report.cdpMetricsDelta.LayoutDuration}ms ` +
      `worstTask=${longTaskAttribution[0]?.dur ?? 0}ms[${longTaskAttribution[0]?.top[0]?.label ?? "n/a"} ` +
      `${longTaskAttribution[0]?.top[0]?.ms ?? 0}ms] ` +
      `styleRecalcTrace=${traceSummary.byName.find((e) => e.name === "UpdateLayoutTree")?.ms ?? 0}ms/` +
      `${traceSummary.byName.find((e) => e.name === "UpdateLayoutTree")?.count ?? 0}次 ` +
      `elements=${traceSummary.styleElementCountTotal} ` +
      `layout=${traceSummary.layoutStats.count}次(dirty p50=${traceSummary.layoutStats.dirtyP50} ` +
      `p95=${traceSummary.layoutStats.dirtyP95} max=${traceSummary.layoutStats.dirtyMax} ` +
      `objects=${traceSummary.layoutStats.totalObjectsMax}) ` +
      `mutations=${report.domMutations.total}(/frame p95=${report.domMutations.perFrame.p95} ` +
      `max=${report.domMutations.perFrame.max}) topMutation=[${report.domMutations.topTargets[0]?.target ?? "n/a"} ` +
      `${report.domMutations.topTargets[0]?.count ?? 0}] ` +
      `invalidation=[${traceSummary.styleInvalidationTop[0]?.cause ?? "n/a"} ` +
      `${traceSummary.styleInvalidationTop[0]?.count ?? 0}次] ` +
      `invalidatedBy=[${traceSummary.styleInvalidationByCaller[0]?.caller ?? "n/a"} ` +
      `${traceSummary.styleInvalidationByCaller[0]?.count ?? 0}次] ` +
      `removals=${report.domMutations.removals.count}次/${report.domMutations.removals.nodes}节点 ` +
      `removalTop=[${report.domMutations.removals.topTargets[0]?.target ?? "n/a"} ` +
      `${report.domMutations.removals.topTargets[0]?.count ?? 0}] ` +
      `sample=[${report.domMutations.removals.samples[0]?.html.slice(0, 60) ?? "n/a"}] ` +
      `staleBuild=${report.build.stale ? "YES" : "no"}`,
  );
});
