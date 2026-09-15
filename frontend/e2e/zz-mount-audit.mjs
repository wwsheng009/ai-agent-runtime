// zz-mount-audit：一次性诊断脚本（不是 e2e 用例，不走 playwright.config.ts）。
//
// 目的：量化「前端流式渲染时是否重复挂载 DOM 元素 → 高 CPU」。
//
// 跑法（cwd = frontend）：node e2e/zz-mount-audit.mjs [--mode=both|table|plain] [--prod]
//
// `--prod`：先 vite build 再 vite preview，用生产包跑同一套审计。dev 模式 React 走
// jsxDEV（每次建元素都抓栈），会把 longtask / ScriptDuration 抬高数倍，只有生产包
// 的数字才代表用户端。
//
// 硬约束遵守点：
//   * 自建假 SSE 服务与 vite dev server 都用「操作系统分配的空闲端口」，
//     绝不碰 5193 / 8101；
//   * 不修改 frontend/src/ 任何文件；
//   * 全程不发起真实 LLM 调用（不请求 :8101）；
//   * 结束前 kill 掉所有子进程并打印 PID。

import { spawn } from "node:child_process";
import net from "node:net";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { chromium } from "@playwright/test";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const FRONTEND_DIR = path.resolve(HERE, "..");
const args = process.argv.slice(2);
const MODE_ARG =
  (args.find((arg) => arg.startsWith("--mode=")) ?? "--mode=both").slice(7);
const PROD = args.includes("--prod");

const children = [];
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function log(line) {
  process.stdout.write(`${line}\n`);
}

function getFreePort() {
  return new Promise((resolve, reject) => {
    const probe = net.createServer();
    probe.unref();
    probe.on("error", reject);
    probe.listen(0, "127.0.0.1", () => {
      const { port } = probe.address();
      probe.close(() => resolve(port));
    });
  });
}

function track(label, child) {
  child.__label = label;
  children.push(child);
  return child;
}

async function waitForHttp(url, timeoutMs = 90_000) {
  const deadline = Date.now() + timeoutMs;
  let lastError = "";
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url);
      if (response.ok || response.status === 404) return true;
    } catch (error) {
      lastError = String(error?.message ?? error);
    }
    await sleep(250);
  }
  throw new Error(`timeout waiting for ${url}: ${lastError}`);
}

function killChildren() {
  for (const child of children) {
    if (child.exitCode !== null || child.signalCode) {
      log(`[cleanup] pid=${child.pid} label=${child.__label} already exited`);
      continue;
    }
    try {
      if (process.platform === "win32") {
        const killer = spawn("taskkill", ["/pid", String(child.pid), "/T", "/F"], {
          stdio: "ignore",
        });
        killer.unref();
      } else {
        child.kill("SIGTERM");
      }
      log(`[cleanup] killed pid=${child.pid} label=${child.__label}`);
    } catch (error) {
      log(`[cleanup] FAILED pid=${child.pid} label=${child.__label}: ${error}`);
    }
  }
  children.length = 0;
}

/** 跑一条命令直到退出（构建用），失败时把尾部输出带进错误信息。 */
function runToCompletion(command, commandArgs, env) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, commandArgs, {
      cwd: FRONTEND_DIR,
      env,
      stdio: ["ignore", "pipe", "pipe"],
    });
    const tail = [];
    const collect = (data) => {
      tail.push(String(data).trimEnd());
      if (tail.length > 12) tail.shift();
    };
    child.stdout.on("data", collect);
    child.stderr.on("data", collect);
    child.on("error", reject);
    child.on("exit", (code) => {
      if (code === 0) {
        resolve();
      } else {
        reject(new Error(`command failed (${code}): ${tail.join(" | ")}`));
      }
    });
  });
}

// --- 页面侧探针（addInitScript：每个文档都会先装好 longtask 采集器）---------

function auditInit() {
  const bag = {
    longtasks: [],
    longtaskObserverError: null,
    navStart: performance.now(),
  };
  window.__audit = bag;
  try {
    const observer = new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        bag.longtasks.push({
          start: Math.round(entry.startTime * 10) / 10,
          dur: Math.round(entry.duration * 10) / 10,
        });
      }
    });
    observer.observe({ type: "longtask", buffered: true });
  } catch (error) {
    bag.longtaskObserverError = String(error);
  }
}

function stats(values) {
  if (values.length === 0) return null;
  const sorted = [...values].sort((left, right) => left - right);
  const pick = (q) => sorted[Math.min(sorted.length - 1, Math.floor(q * sorted.length))];
  const sum = sorted.reduce((acc, value) => acc + value, 0);
  return {
    count: sorted.length,
    min: Math.round(sorted[0] * 100) / 100,
    p50: Math.round(pick(0.5) * 100) / 100,
    p90: Math.round(pick(0.9) * 100) / 100,
    max: Math.round(sorted[sorted.length - 1] * 100) / 100,
    mean: Math.round((sum / sorted.length) * 100) / 100,
  };
}

// --- 页面侧：MutationObserver 安装（在发消息前调用）------------------------

function installObserver(selector) {
  const bag = window.__audit;
  const dom = {
    container: null,
    startedAt: performance.now(),
    lastMutationAt: 0,
    childListRecords: 0,
    characterDataRecords: 0,
    addedTotal: 0,
    removedTotal: 0,
    addedByTag: {},
    removedByTag: {},
    tdAdds: 0,
    tdRemoves: 0,
    trAdds: 0,
    trRemoves: 0,
    cellInstances: {},
    cellAdds: [],
    cellLifetimes: [],
    lifetimeByTag: {},
    callbackCount: 0,
    callbackTimes: [],
    maxRecordsPerCallback: 0,
  };
  window.__dom = dom;

  const root =
    document.querySelector(selector) ||
    document.getElementById("root") ||
    document.body;
  dom.container = `${root.tagName.toLowerCase()}.${String(root.className || "").slice(0, 80)}`;

  const bump = (target, key) => {
    target[key] = (target[key] || 0) + 1;
  };
  const round = (value) => Math.round(value * 100) / 100;

  const reactKeyOf = (element) => {
    try {
      const prop = Object.keys(element).find((name) =>
        name.startsWith("__reactFiber$"),
      );
      if (!prop) return null;
      const fiber = element[prop];
      return fiber && fiber.key != null ? String(fiber.key) : null;
    } catch {
      return null;
    }
  };

  const cellPosition = (element) => {
    const row = element.parentElement;
    if (!row) return null;
    const table = row.closest("table");
    if (!table) return null;
    const rows = table.querySelectorAll("tr");
    let rowIndex = -1;
    for (let index = 0; index < rows.length; index += 1) {
      if (rows[index] === row) {
        rowIndex = index;
        break;
      }
    }
    const cellIndex = Array.prototype.indexOf.call(row.children, element);
    const section = row.parentElement
      ? row.parentElement.tagName.toLowerCase()
      : "?";
    return `${section}/r${rowIndex}/c${cellIndex}`;
  };

  const birth = new WeakMap();
  const observer = new MutationObserver((records) => {
    const now = performance.now();
    dom.callbackCount += 1;
    dom.lastMutationAt = now;
    if (dom.callbackTimes.length < 8000) {
      dom.callbackTimes.push(round(now));
    }
    if (records.length > dom.maxRecordsPerCallback) {
      dom.maxRecordsPerCallback = records.length;
    }

    for (const record of records) {
      if (record.type === "characterData") {
        dom.characterDataRecords += 1;
        continue;
      }
      if (record.type !== "childList") continue;
      dom.childListRecords += 1;
      dom.addedTotal += record.addedNodes.length;
      dom.removedTotal += record.removedNodes.length;

      for (const node of record.addedNodes) {
        if (node.nodeType !== 1) {
          bump(dom.addedByTag, "#text");
          continue;
        }
        const tag = node.tagName.toLowerCase();
        bump(dom.addedByTag, tag);
        birth.set(node, now);
        if (tag === "td" || tag === "th") {
          dom.tdAdds += 1;
          const position = cellPosition(node);
          const key = reactKeyOf(node);
          bump(dom.cellInstances, position || "no-table");
          if (dom.cellAdds.length < 6000) {
            dom.cellAdds.push({
              t: round(now),
              pos: position,
              key: key === null ? null : key.slice(0, 56),
              head: (node.textContent || "").slice(0, 20),
            });
          }
        } else if (tag === "tr") {
          dom.trAdds += 1;
        }
      }

      for (const node of record.removedNodes) {
        if (node.nodeType !== 1) {
          bump(dom.removedByTag, "#text");
          continue;
        }
        const tag = node.tagName.toLowerCase();
        bump(dom.removedByTag, tag);
        const start = birth.get(node);
        if (start === undefined) {
          if (tag === "tr") dom.trRemoves += 1;
          continue;
        }
        const lifetime = round(now - start);
        if (!dom.lifetimeByTag[tag]) dom.lifetimeByTag[tag] = [];
        if (dom.lifetimeByTag[tag].length < 6000) {
          dom.lifetimeByTag[tag].push(lifetime);
        }
        if (tag === "td" || tag === "th") {
          dom.tdRemoves += 1;
          if (dom.cellLifetimes.length < 6000) dom.cellLifetimes.push(lifetime);
        } else if (tag === "tr") {
          dom.trRemoves += 1;
        }
      }
    }
  });

  observer.observe(root, {
    childList: true,
    subtree: true,
    characterData: true,
  });
  dom.observer = observer;
  return dom.container;
}

// --- 页面侧：原始计数读取 / DOM 结构探针 --------------------------------------

function readRaw() {
  const dom = window.__dom;
  const round = (value) => Math.round(value * 100) / 100;
  return {
    atMs: round(performance.now() - dom.startedAt),
    childListRecords: dom.childListRecords,
    characterDataRecords: dom.characterDataRecords,
    addedTotal: dom.addedTotal,
    removedTotal: dom.removedTotal,
    addedByTag: { ...dom.addedByTag },
    removedByTag: { ...dom.removedByTag },
    tdAdds: dom.tdAdds,
    tdRemoves: dom.tdRemoves,
    trAdds: dom.trAdds,
    trRemoves: dom.trRemoves,
    callbackCount: dom.callbackCount,
    cellInstances: { ...dom.cellInstances },
    cellLifetimes: dom.cellLifetimes.slice(0, 60),
    lifetimeByTag: Object.fromEntries(
      Object.entries(dom.lifetimeByTag).map(([tag, values]) => [
        tag,
        { count: values.length, meanMs: round(values.reduce((a, b) => a + b, 0) / values.length) },
      ]),
    ),
  };
}

// 页面侧：给「当前承载最新 token 的单元格/段落」打一个标记属性，用来验证
// 同一逻辑位置上的 DOM 元素实例是否在流式期间被卸载重建（标记者存活 = 未 remount）。
function markActiveCell(needles) {
  const list = Array.isArray(needles) ? needles : [needles];
  const all = Array.from(document.querySelectorAll("*"));
  let holders = [];
  let chosen = null;
  for (const needle of list) {
    const hits = all.filter((el) => (el.textContent || "").includes(needle));
    if (hits.length) {
      chosen = needle;
      holders = hits;
    }
  }
  if (!holders.length) return { needle: chosen, marked: null, tag: null };
  const deepest = holders[holders.length - 1];
  const host =
    deepest.closest("td,th,li,p,pre,span,div") || deepest;
  host.setAttribute("data-zz-mark", "1");
  return {
    needle: chosen,
    marked: 1,
    tag: host.tagName.toLowerCase(),
    textLen: (host.textContent || "").length,
  };
}

function readMark() {
  const nodes = Array.from(document.querySelectorAll("[data-zz-mark]"));
  return {
    count: nodes.length,
    tags: nodes.map((el) => el.tagName.toLowerCase()),
    textLen: nodes.map((el) => (el.textContent || "").length),
    connected: nodes.map((el) => el.isConnected),
  };
}

// 注意：本函数整体被 `page.evaluate` 序列化进页面执行，必须完全自包含。
function domProbe(needles) {
  const PROBE_TAGS = [
    "table", "thead", "tbody", "tr", "td", "th", "p", "li", "ul", "ol",
    "pre", "code", "h1", "h2", "h3", "div", "span", "a", "button", "svg",
  ];
  const skeletonOf = (root, maxLength) => {
    const out = [];
    const walk = (node, depth) => {
      if (out.join("").length > maxLength || depth > 12) return;
      if (node.nodeType === 3) {
        const text = (node.nodeValue || "").replace(/\s+/g, " ").trim();
        if (text) out.push(`"${text.slice(0, 28)}"`);
        return;
      }
      if (node.nodeType !== 1) return;
      const tag = node.tagName.toLowerCase();
      const marks = [];
      const mode = node.getAttribute("data-streaming-mode");
      const active = node.getAttribute("data-streaming-active");
      const klass = typeof node.className === "string" ? node.className : "";
      if (mode) marks.push(`dsm=${mode}`);
      if (active) marks.push(`active=${active}`);
      if (klass) marks.push(`cls=${klass.split(/\s+/).slice(0, 3).join(" ").slice(0, 40)}`);
      out.push(`<${tag}${marks.length ? " " + marks.join(" ") : ""}>`);
      for (const child of Array.from(node.childNodes)) walk(child, depth + 1);
      out.push(`</${tag}>`);
    };
    walk(root, 0);
    return out.join("").slice(0, maxLength);
  };
  const list = Array.isArray(needles) ? needles : [needles];
  const counts = {};
  for (const tag of PROBE_TAGS) counts[tag] = document.getElementsByTagName(tag).length;
  const all = Array.from(document.querySelectorAll("*"));
  // 取「最新到达的」标记：数组中最后一个在 DOM 里有命中的 needle。
  let chosen = null;
  let holders = [];
  for (const needle of list) {
    const hits = all.filter((el) => (el.textContent || "").includes(needle));
    if (hits.length) {
      chosen = needle;
      holders = hits;
    }
  }
  const deepest = holders.length ? holders[holders.length - 1] : null;
  const chain = [];
  let node = deepest;
  while (node && node.nodeType === 1 && chain.length < 12) {
    const attrs = {};
    for (const attr of Array.from(node.attributes || [])) {
      if (/^data-|^aria-busy$|^aria-relevant$|^role$/.test(attr.name)) {
        attrs[attr.name] = String(attr.value).slice(0, 80);
      }
    }
    chain.push({
      tag: node.tagName.toLowerCase(),
      cls: (typeof node.className === "string" ? node.className : "").slice(0, 90),
      attrs,
      textLen: (node.textContent || "").length,
    });
    node = node.parentElement;
  }
  const streamingNodes = Array.from(
    document.querySelectorAll(
      "[data-streaming-mode],[data-streaming-active],[data-streaming-tail]",
    ),
  );
  return {
    needle: chosen,
    counts,
    totalElements: all.length,
    markerHolders: holders.length,
    chain,
    streamingNodeCount: streamingNodes.length,
    streamingNodes: streamingNodes.slice(0, 10).map((el) => ({
      tag: el.tagName.toLowerCase(),
      attrs: Array.from(el.attributes).reduce((acc, attr) => {
        acc[attr.name] = String(attr.value).slice(0, 50);
        return acc;
      }, {}),
    })),
    holderSkeleton: deepest ? skeletonOf(deepest.parentElement || deepest, 700) : null,
  };
}

// --- 页面侧：快照 ------------------------------------------------------------

function collectSnapshot() {
  const bag = window.__audit;
  const dom = window.__dom;
  const round = (value) => Math.round(value * 100) / 100;
  const summarize = (values) => {
    if (!values || values.length === 0) return null;
    const sorted = [...values].sort((left, right) => left - right);
    const at = (q) =>
      sorted[Math.min(sorted.length - 1, Math.floor(q * sorted.length))];
    const sum = sorted.reduce((acc, value) => acc + value, 0);
    return {
      count: sorted.length,
      min: round(sorted[0]),
      p50: round(at(0.5)),
      p90: round(at(0.9)),
      max: round(sorted[sorted.length - 1]),
      mean: round(sum / sorted.length),
    };
  };

  const positions = Object.entries(dom.cellInstances)
    .map(([pos, instances]) => ({ pos, instances }))
    .sort((left, right) => right.instances - left.instances);

  const top = positions[0];
  let topDetail = null;
  if (top) {
    const adds = dom.cellAdds.filter((entry) => entry.pos === top.pos);
    const keys = adds.map((entry) => entry.key);
    const uniqueKeys = [...new Set(keys)];
    const gaps = [];
    for (let index = 1; index < adds.length; index += 1) {
      gaps.push(round(adds[index].t - adds[index - 1].t));
    }
    topDetail = {
      pos: top.pos,
      instances: top.instances,
      keySampleFirst: keys.slice(0, 3),
      keySampleLast: keys.slice(-2),
      uniqueKeys: uniqueKeys.length,
      uniqueKeySample: uniqueKeys.slice(0, 3),
      addsPerSecond: gaps.length
        ? round(1000 / (gaps.reduce((acc, value) => acc + value, 0) / gaps.length))
        : null,
      insertionGapMs: summarize(gaps),
      cellLifetimeMs: summarize(dom.cellLifetimes),
      firstAddAt: adds.length ? adds[0].t : null,
      lastAddAt: adds.length ? adds[adds.length - 1].t : null,
    };
  }

  const callbackGaps = [];
  for (let index = 1; index < dom.callbackTimes.length; index += 1) {
    callbackGaps.push(round(dom.callbackTimes[index] - dom.callbackTimes[index - 1]));
  }

  const longtasks = bag.longtasks ?? [];
  const longtaskDurations = longtasks.map((entry) => entry.dur);

  const lifetimeStats = {};
  for (const [tag, values] of Object.entries(dom.lifetimeByTag)) {
    lifetimeStats[tag] = summarize(values);
  }

  dom.observer.disconnect();

  return {
    container: dom.container,
    observerWindowMs: round(dom.lastMutationAt - dom.startedAt),
    childListRecords: dom.childListRecords,
    characterDataRecords: dom.characterDataRecords,
    addedTotal: dom.addedTotal,
    removedTotal: dom.removedTotal,
    addedByTag: dom.addedByTag,
    removedByTag: dom.removedByTag,
    tdAdds: dom.tdAdds,
    tdRemoves: dom.tdRemoves,
    trAdds: dom.trAdds,
    trRemoves: dom.trRemoves,
    mutationCallbacks: dom.callbackCount,
    maxRecordsPerCallback: dom.maxRecordsPerCallback,
    callbackGapMs: summarize(callbackGaps),
    callbackSpanMs: dom.callbackTimes.length
      ? round(dom.callbackTimes[dom.callbackTimes.length - 1] - dom.callbackTimes[0])
      : 0,
    cellLifetimeMs: summarize(dom.cellLifetimes),
    lifetimeByTag: lifetimeStats,
    topPositions: positions.slice(0, 6),
    topPositionDetail: topDetail,
    longtaskCount: longtasks.length,
    longtaskTotalMs: round(longtaskDurations.reduce((acc, value) => acc + value, 0)),
    longtaskMaxMs: longtaskDurations.length
      ? round(Math.max(...longtaskDurations))
      : 0,
    longtaskStats: summarize(longtaskDurations),
    longtaskTop5: [...longtasks]
      .sort((left, right) => right.dur - left.dur)
      .slice(0, 5),
    domElementCountNow: document.getElementsByTagName("*").length,
    bodyTextLength: (document.body.innerText || "").length,
  };
}

// --- CDP 指标 ---------------------------------------------------------------

const METRIC_NAMES = [
  "TaskDuration",
  "ScriptDuration",
  "LayoutDuration",
  "RecalcStyleDuration",
  "Nodes",
  "JSHeapUsedSize",
  "JSHeapTotalSize",
  "Documents",
  "Frames",
  "LayoutCount",
  "RecalcStyleCount",
];

function pickMetrics(raw) {
  const map = {};
  for (const entry of raw.metrics ?? []) {
    if (METRIC_NAMES.includes(entry.name)) {
      map[entry.name] = Math.round(entry.value * 1000) / 1000;
    }
  }
  return map;
}

function diffMetrics(before, after) {
  const out = {};
  for (const name of METRIC_NAMES) {
    if (before[name] === undefined || after[name] === undefined) continue;
    out[name] = Math.round((after[name] - before[name]) * 1000) / 1000;
  }
  return out;
}

// --- 单模式测量 --------------------------------------------------------------

async function runMode({ browser, baseUrl, apiPort, mode, markers }) {
  const context = await browser.newContext({
    viewport: { width: 1280, height: 900 },
    locale: "en-US",
    timezoneId: "UTC",
    deviceScaleFactor: 1,
  });
  await context.addInitScript(auditInit);
  const page = await context.newPage();
  page.on("pageerror", (error) => log(`[${mode}] pageerror: ${error.message}`));
  const consoleLines = [];
  page.on("console", (message) => {
    if (consoleLines.length < 60) {
      consoleLines.push(`${message.type()}: ${message.text().slice(0, 300)}`);
    }
  });

  const cdp = await context.newCDPSession(page);
  await cdp.send("Performance.enable");
  const readMetrics = async () => pickMetrics(await cdp.send("Performance.getMetrics"));

  const result = { mode, apiPort };
  try {
    await page.goto(`${baseUrl}/workspace`, {
      waitUntil: "domcontentloaded",
      timeout: 60_000,
    });
    try {
      await page.waitForSelector(".app-chat-input", { timeout: 60_000, state: "visible" });
    } catch (bootError) {
      const diag = await page.evaluate(() => ({
        url: location.href,
        title: document.title,
        text: (document.body.innerText || "").slice(0, 1500),
        inputs: document.querySelectorAll("textarea, input").length,
        rootChildren: document.getElementById("root")?.childElementCount ?? -1,
      }));
      log(`[${mode}] BOOT FAILURE diag = ${JSON.stringify(diag)}`);
      log(`[${mode}] console = ${JSON.stringify(consoleLines)}`);
      const serverLog = await (await fetch(`http://127.0.0.1:${apiPort}/__stats`)).json();
      log(`[${mode}] api requests = ${JSON.stringify(serverLog.requests)}`);
      throw bootError;
    }
    await page.waitForTimeout(1200);

    result.container = await page.evaluate(installObserver, '[role="log"]');
    result.domBefore = await page.evaluate(() => document.getElementsByTagName("*").length);
    result.metricsBefore = await readMetrics();

    const composer = page.locator(".app-chat-input");
    await composer.fill(`${markers.prompt}`);
    const sentAt = Date.now();
    await composer.press("Control+Enter");
    log(`[${mode}] prompt sent, waiting for first token`);

    await page.waitForFunction(
      (needle) => document.body.innerText.includes(needle),
      markers.first,
      { timeout: 60_000, polling: 50 },
    );
    result.firstChunkVisibleMs = Date.now() - sentAt;
    log(`[${mode}] first token visible after ${result.firstChunkVisibleMs}ms`);
    await page.waitForTimeout(1500);
    result.probeMidStream = await page.evaluate(domProbe, markers.midCandidates);
    result.mutationMidStream = await page.evaluate(readRaw);
    result.markAtMid = await page.evaluate(markActiveCell, markers.midCandidates);
    result.markReadMid = await page.evaluate(readMark);

    await page.waitForFunction(
      (needle) => document.body.innerText.includes(needle),
      markers.last,
      { timeout: 120_000, polling: 50 },
    );
    result.lastChunkVisibleMs = Date.now() - sentAt;
    log(`[${mode}] last token visible after ${result.lastChunkVisibleMs}ms`);
    result.metricsStreamEnd = await readMetrics();
    result.probeStreamEnd = await page.evaluate(domProbe, markers.lastCandidates);
    result.mutationStreamEnd = await page.evaluate(readRaw);
    result.markReadStreamEnd = await page.evaluate(readMark);

    await page.waitForTimeout(3000);
    result.settleMs = Date.now() - sentAt;
    result.metricsAfter = await readMetrics();
    result.probeSettle = await page.evaluate(domProbe, markers.lastCandidates);
    result.mutationSettle = await page.evaluate(readRaw);
    result.markReadSettle = await page.evaluate(readMark);
    result.snapshot = await page.evaluate(collectSnapshot);
    result.domAfter = result.snapshot.domElementCountNow;
    result.metricDeltas = diffMetrics(result.metricsBefore, result.metricsAfter);
    result.streamDeltas = diffMetrics(result.metricsBefore, result.metricsStreamEnd);

    const serverStats = await (await fetch(`http://127.0.0.1:${apiPort}/__stats`)).json();
    result.serverStats = {
      chunks: serverStats.chunks,
      chatRequests: serverStats.chatRequests,
      mode: serverStats.mode,
      lastAborted: serverStats.lastAborted,
    };
  } catch (error) {
    try {
      const diag = await page.evaluate(() => {
        const text = document.body.innerText || "";
        const found = Array.from(text.matchAll(/token(\d{4})/g)).map((m) => Number(m[1]));
        const rawText = document.body.textContent || "";
        const rawFound = Array.from(rawText.matchAll(/token(\d{4})/g)).map((m) => Number(m[1]));
        return {
          bodyInnerTextLen: text.length,
          tokensInInnerText: found.length,
          maxTokenInInnerText: found.length ? Math.max(...found) : null,
          tokensInTextContent: rawFound.length,
          maxTokenInTextContent: rawFound.length ? Math.max(...rawFound) : null,
          tail: text.slice(-300),
          roleLog: document.querySelectorAll('[role="log"]').length,
          table: document.querySelectorAll("table").length,
          tr: document.querySelectorAll("tr").length,
          td: document.querySelectorAll("td").length,
          alertText: (document.querySelector('[role="alert"]')?.textContent || "").slice(0, 200),
        };
      });
      log(`[${mode}] DIAG ${JSON.stringify(diag)}`);
      const stats = await (await fetch(`http://127.0.0.1:${apiPort}/__stats`)).json();
      log(
        `[${mode}] SERVER chunks=${stats.chunks} chatRequests=${stats.chatRequests} mode=${stats.mode} aborted=${stats.lastAborted} requests=${JSON.stringify(stats.requests.slice(0, 14))}`,
      );
    } catch (inner) {
      log(`[${mode}] DIAG FAILED ${inner?.message ?? inner}`);
    }
    log(`[${mode}] console = ${JSON.stringify(consoleLines.slice(0, 25))}`);
    throw error;
  } finally {
    await context.close();
  }
  return result;
}

// --- 报告 -------------------------------------------------------------------

function report(result) {
  const snap = result.snapshot;
  log("");
  log(`================ MODE=${result.mode} ================`);
  log(`container=${snap.container}  observerWindow=${snap.observerWindowMs}ms  wallClock=${result.settleMs}ms`);
  log(`server chunks=${result.serverStats.chunks} aborted=${result.serverStats.lastAborted}  firstChunkVisible=${result.firstChunkVisibleMs}ms lastChunkVisible=${result.lastChunkVisibleMs}ms`);
  log(`--- [1] MutationObserver（整条流）---`);
  log(`childList records = ${snap.childListRecords}`);
  log(`addedNodes total  = ${snap.addedTotal}`);
  log(`removedNodes total= ${snap.removedTotal}`);
  log(`characterData records = ${snap.characterDataRecords}`);
  log(`addedByTag   = ${JSON.stringify(snap.addedByTag)}`);
  log(`removedByTag = ${JSON.stringify(snap.removedByTag)}`);
  log(`mutation callbacks = ${snap.mutationCallbacks}  span=${snap.callbackSpanMs}ms  maxRecordsPerCallback=${snap.maxRecordsPerCallback}`);
  log(`callbackGapMs = ${JSON.stringify(snap.callbackGapMs)}`);
  log(`--- [2] 表格单元格实例/存活 ---`);
  log(`td adds=${snap.tdAdds} removes=${snap.tdRemoves} | tr adds=${snap.trAdds} removes=${snap.trRemoves}`);
  log(`cellLifetimeMs(all td) = ${JSON.stringify(snap.cellLifetimeMs)}`);
  log(`topPositions = ${JSON.stringify(snap.topPositions)}`);
  log(`topPositionDetail = ${JSON.stringify(snap.topPositionDetail)}`);
  log(`lifetimeByTag(td/th/tr/table/p/li/div/span) = ${JSON.stringify({
    td: snap.lifetimeByTag.td ?? null,
    th: snap.lifetimeByTag.th ?? null,
    tr: snap.lifetimeByTag.tr ?? null,
    table: snap.lifetimeByTag.table ?? null,
    div: snap.lifetimeByTag.div ?? null,
    p: snap.lifetimeByTag.p ?? null,
    span: snap.lifetimeByTag.span ?? null,
    li: snap.lifetimeByTag.li ?? null,
  })}`);
  log(`--- [3] longtask ---`);
  log(`count=${snap.longtaskCount} totalMs=${snap.longtaskTotalMs} maxMs=${snap.longtaskMaxMs}`);
  log(`longtaskStats = ${JSON.stringify(snap.longtaskStats)}`);
  log(`longtaskTop5 = ${JSON.stringify(snap.longtaskTop5)}`);
  log(`--- [4] CDP Performance.getMetrics ---`);
  log(`before      = ${JSON.stringify(result.metricsBefore)}`);
  log(`streamEnd   = ${JSON.stringify(result.metricsStreamEnd)}`);
  log(`afterSettle = ${JSON.stringify(result.metricsAfter)}`);
  log(`delta(before→afterSettle) = ${JSON.stringify(result.metricDeltas)}`);
  log(`delta(before→streamEnd)   = ${JSON.stringify(result.streamDeltas)}`);
  log(`DOM elements: before=${result.domBefore} after=${result.domAfter} bodyTextLen=${snap.bodyTextLength}`);
  log(`--- [5] DOM 结构探针 / 分窗口计数 ---`);
  log(`  markAtMid = ${JSON.stringify(result.markAtMid)}`);
  log(
    `  markRead(mid → streamEnd → settle) = ${JSON.stringify([
      result.markReadMid,
      result.markReadStreamEnd,
      result.markReadSettle,
    ])}`,
  );
  const probes = [
    ["midStream", result.probeMidStream, result.mutationMidStream],
    ["streamEnd", result.probeStreamEnd, result.mutationStreamEnd],
    ["settle   ", result.probeSettle, result.mutationSettle],
  ];
  for (const [label, probe, mutation] of probes) {
    if (!probe) continue;
    const c = probe.counts;
    log(
      `  [${label}] needle=${probe.needle} elements=${probe.totalElements} ` +
        `table=${c.table} tr=${c.tr} td=${c.td} th=${c.th} li=${c.li} ul=${c.ul} pre=${c.pre} code=${c.code} p=${c.p} div=${c.div} span=${c.span}`,
    );
    log(`      streamingNodes=${probe.streamingNodeCount} ${JSON.stringify(probe.streamingNodes)}`);
    log(`      chain=${JSON.stringify(probe.chain)}`);
    log(`      skeleton=${(probe.holderSkeleton || "").slice(0, 420)}`);
    if (mutation) {
      log(
        `      counters@${mutation.atMs}ms childList=${mutation.childListRecords} added=${mutation.addedTotal} removed=${mutation.removedTotal} charData=${mutation.characterDataRecords} cb=${mutation.callbackCount} td(+${mutation.tdAdds}/-${mutation.tdRemoves}) tr(+${mutation.trAdds}/-${mutation.trRemoves})`,
      );
      log(`      addedByTag=${JSON.stringify(mutation.addedByTag)}`);
      log(`      removedByTag=${JSON.stringify(mutation.removedByTag)}`);
      log(`      cellInstances=${JSON.stringify(mutation.cellInstances)}`);
    }
  }
}

// --- main -------------------------------------------------------------------

async function main() {
  const vitePort = await getFreePort();
  const apiChild = track(
    "zz-mount-audit-server",
    spawn(process.execPath, [path.join(HERE, "zz-mount-audit-server.mjs")], {
      cwd: FRONTEND_DIR,
      env: {
        ...process.env,
        AUDIT_PORT: "0",
        AUDIT_CHUNK_MS: "20",
        AUDIT_GROWTH_CHUNKS: "500",
      },
      stdio: ["ignore", "pipe", "pipe"],
    }),
  );

  let apiPort = 0;
  apiChild.stdout.setEncoding("utf8");
  apiChild.stderr.setEncoding("utf8");
  apiChild.stderr.on("data", (data) => log(`[zz-audit-server:err] ${data.trim()}`));

  const portFound = new Promise((resolve) => {
    let buffer = "";
    apiChild.stdout.on("data", (data) => {
      buffer += data;
      const match = /AUDIT_PORT=(\d+)/.exec(buffer);
      if (match) resolve(Number(match[1]));
    });
  });
  apiPort = await Promise.race([
    portFound,
    sleep(10_000).then(() => {
      throw new Error("zz-audit-server did not report a port");
    }),
  ]);
  log(`[env] fake SSE server pid=${apiChild.pid} port=${apiPort}`);

  const viteEnv = { ...process.env, VITE_API_PROXY_PORT: String(apiPort), VITE_DEV_HOST: "127.0.0.1" };
  delete viteEnv.VITE_API_PROXY_TARGET;
  delete viteEnv.VITE_DEV_PUBLIC_ORIGIN;
  delete viteEnv.VITE_DEV_PORT;
  const viteBin = path.join(FRONTEND_DIR, "node_modules", "vite", "bin", "vite.js");
  if (PROD) {
    log("[prod] vite build …");
    await runToCompletion(process.execPath, [viteBin, "build"], viteEnv);
    log("[prod] build done");
  }
  const viteChild = track(
    PROD ? "vite-preview" : "vite-dev",
    spawn(
      process.execPath,
      [
        viteBin,
        ...(PROD ? ["preview"] : []),
        "--host",
        "127.0.0.1",
        "--port",
        String(vitePort),
        "--strictPort",
      ],
      { cwd: FRONTEND_DIR, env: viteEnv, stdio: ["ignore", "pipe", "pipe"] },
    ),
  );
  viteChild.stdout.setEncoding("utf8");
  viteChild.stderr.setEncoding("utf8");
  viteChild.stderr.on("data", (data) => log(`[vite:err] ${data.trim()}`));
  const baseUrl = `http://127.0.0.1:${vitePort}`;
  await waitForHttp(baseUrl, 120_000);
  log(
    `[env] vite ${PROD ? "preview(prod)" : "dev"} pid=${viteChild.pid} url=${baseUrl} (proxy /api -> 127.0.0.1:${apiPort})`,
  );

  const browser = await chromium.launch({
    channel: "chrome",
    headless: true,
    args: [
      "--disable-background-timer-throttling",
      "--disable-backgrounding-occluded-windows",
      "--disable-renderer-backgrounding",
    ],
  });
  log(`[env] chrome version=${browser.version()}`);

  const KNOWN_MODES = ["table", "table_stream", "plain"];
  const modes =
    MODE_ARG === "both"
      ? ["table", "plain"]
      : MODE_ARG === "all"
        ? KNOWN_MODES
        : MODE_ARG.split(",")
            .map((entry) => entry.trim())
            .filter((entry) => KNOWN_MODES.includes(entry));
  if (modes.length === 0) {
    throw new Error(`unknown --mode=${MODE_ARG}; known: ${KNOWN_MODES.join(",")}, both, all`);
  }
  const promptFor = (mode) =>
    mode === "plain" ? "zzplain audit" : mode === "table_stream" ? "zzstream audit" : "zzmount audit";
  const results = [];
  for (const mode of modes) {
    const markers = {
      prompt: promptFor(mode),
      first: "token0000",
      midCandidates: Array.from({ length: 17 }, (_, i) => `token${1000 + i * 25}`),
      last: "token1499",
      lastCandidates: ["token1499", "token1495", "token1490"],
    };
    // 每次跑之前清掉服务端计数（chunks/requests），便于按模式归因。
    await fetch(`http://127.0.0.1:${apiPort}/__reset`).catch(() => {});
    results.push(await runMode({ browser, baseUrl, apiPort, mode, markers }));
  }
  await browser.close();

  for (const result of results) report(result);

  log("");
  log("================ 对照表（raw）================ ");
  const header = [
    "mode",
    "chunks",
    "childList",
    "added",
    "removed",
    "charData",
    "tdAdds",
    "tdRemoves",
    "topCellInstances",
    "topCellKeyUnique",
    "cellLifeP50ms",
    "callbacks",
    "callbacks/s",
    "longtasks",
    "longtaskTotalMs",
    "longtaskMaxMs",
    "TaskDur(s)",
    "ScriptDur(s)",
    "LayoutDur(s)",
    "RecalcStyle(s)",
    "NodesDelta",
    "HeapDelta(MB)",
  ];
  log(header.join("\t"));
  for (const result of results) {
    const snap = result.snapshot;
    const detail = snap.topPositionDetail ?? {};
    const delta = result.metricDeltas;
    const callbacksPerSecond =
      snap.callbackSpanMs > 0
        ? Math.round((snap.mutationCallbacks / (snap.callbackSpanMs / 1000)) * 10) / 10
        : 0;
    log(
      [
        result.mode,
        result.serverStats.chunks,
        snap.childListRecords,
        snap.addedTotal,
        snap.removedTotal,
        snap.characterDataRecords,
        snap.tdAdds,
        snap.tdRemoves,
        detail.instances ?? 0,
        detail.uniqueKeys ?? 0,
        snap.cellLifetimeMs ? snap.cellLifetimeMs.p50 : 0,
        snap.mutationCallbacks,
        callbacksPerSecond,
        snap.longtaskCount,
        snap.longtaskTotalMs,
        snap.longtaskMaxMs,
        delta.TaskDuration ?? 0,
        delta.ScriptDuration ?? 0,
        delta.LayoutDuration ?? 0,
        delta.RecalcStyleDuration ?? 0,
        delta.Nodes ?? 0,
        Math.round(((delta.JSHeapUsedSize ?? 0) / 1048576) * 100) / 100,
      ].join("\t"),
    );
  }
  log("");
  log("[raw-json] " + JSON.stringify(results.map((result) => ({
    mode: result.mode,
    childListRecords: result.snapshot.childListRecords,
    addedTotal: result.snapshot.addedTotal,
    removedTotal: result.snapshot.removedTotal,
    characterDataRecords: result.snapshot.characterDataRecords,
    addedByTag: result.snapshot.addedByTag,
    removedByTag: result.snapshot.removedByTag,
    tdAdds: result.snapshot.tdAdds,
    tdRemoves: result.snapshot.tdRemoves,
    trAdds: result.snapshot.trAdds,
    trRemoves: result.snapshot.trRemoves,
    topPositions: result.snapshot.topPositions,
    topPositionDetail: result.snapshot.topPositionDetail,
    cellLifetimeMs: result.snapshot.cellLifetimeMs,
    lifetimeByTag: result.snapshot.lifetimeByTag,
    mutationCallbacks: result.snapshot.mutationCallbacks,
    callbackGapMs: result.snapshot.callbackGapMs,
    longtask: {
      count: result.snapshot.longtaskCount,
      totalMs: result.snapshot.longtaskTotalMs,
      maxMs: result.snapshot.longtaskMaxMs,
      top5: result.snapshot.longtaskTop5,
    },
    metricsBefore: result.metricsBefore,
    metricsStreamEnd: result.metricsStreamEnd,
    metricsAfter: result.metricsAfter,
    metricDeltas: result.metricDeltas,
    streamDeltas: result.streamDeltas,
    domBefore: result.domBefore,
    domAfter: result.domAfter,
    serverStats: result.serverStats,
    timing: {
      firstChunkVisibleMs: result.firstChunkVisibleMs,
      lastChunkVisibleMs: result.lastChunkVisibleMs,
      settleMs: result.settleMs,
    },
  }))));
}

let exitCode = 0;
try {
  await main();
} catch (error) {
  exitCode = 1;
  log(`[fatal] ${error?.stack ?? error}`);
} finally {
  const spawned = children.map((child) => `${child.__label}=${child.pid}`).join(", ");
  killChildren();
  await sleep(500);
  log(`[cleanup] done. spawned pids were: ${spawned || "(none)"}`);
}
process.exit(exitCode);
