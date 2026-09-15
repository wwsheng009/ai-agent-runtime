// zz-profile-audit：一次性诊断脚本（不是 e2e 用例，不走 playwright.config.ts）。
//
// 目的：给「流式渲染期间的主线程 CPU」做真实采样，而不是靠猜。
// 产出：自耗时（self time）Top-N 函数表 —— 用来决定下一个优化点该打哪里。
//
// 跑法（cwd = frontend）：
//   node e2e/zz-profile-audit.mjs --mode=plain [--seconds=12]
//   node e2e/zz-profile-audit.mjs --mode=table_stream
//   node e2e/zz-profile-audit.mjs --mode=plain --prod   # 先 vite build，再 vite preview
//
// `--prod` 的意义：dev 模式下 React 走 jsxDEV（每次建元素都抓栈），会把「重建元素」
// 的成本放大好几倍，容易把优化方向带偏。生产包才是用户真实看到的开销。
//
// 硬约束遵守点：
//   * 假 SSE 服务与 vite dev server 都用「操作系统分配的空闲端口」，绝不碰 5193 / 8101；
//   * 不修改 frontend/src/ 任何文件（只读探针）；
//   * 不发起真实 LLM 调用；
//   * 结束前 kill 掉所有子进程并打印 PID。

import { spawn } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { chromium } from "@playwright/test";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const FRONTEND_DIR = path.resolve(HERE, "..");
const args = process.argv.slice(2);
const MODE_ARG = (args.find((a) => a.startsWith("--mode=")) ?? "--mode=plain").slice(7);
const SECONDS = Number((args.find((a) => a.startsWith("--seconds=")) ?? "--seconds=12").slice(10));
const PROD = args.includes("--prod");
const OUT_DIR = path.join(FRONTEND_DIR, ".tmp");

const children = [];
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const log = (line) => process.stdout.write(`${line}\n`);

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

async function waitForHttp(url, timeoutMs = 120_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url);
      if (response.ok || response.status === 404) return true;
    } catch {
      /* retry */
    }
    await sleep(300);
  }
  throw new Error(`timeout waiting for ${url}`);
}

function killChildren() {
  const spawned = children.map((child) => `${child.__label}=${child.pid}`);
  for (const child of children) {
    try {
      if (process.platform === "win32") {
        spawn("taskkill", ["/pid", String(child.pid), "/T", "/F"], { stdio: "ignore" });
      } else {
        child.kill("SIGKILL");
      }
    } catch {
      /* ignore */
    }
  }
  log(`[cleanup] done. spawned pids were: ${spawned.join(", ")}`);
}

/** 跑一条命令直到退出（构建用），输出只保留尾部若干行。 */
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

process.on("exit", killChildren);
process.on("SIGINT", () => {
  killChildren();
  process.exit(130);
});

// --- profile 聚合 -----------------------------------------------------------

function shortUrl(url) {
  if (!url) return "(native)";
  if (url.startsWith("http://127.0.0.1") || url.startsWith("http://localhost")) {
    try {
      const parsed = new URL(url);
      const file = parsed.pathname.split("/").pop() ?? parsed.pathname;
      return decodedURIComponentSafe(file);
    } catch {
      return url;
    }
  }
  return url.length > 64 ? `${url.slice(0, 61)}...` : url;
}

function decodedURIComponentSafe(value) {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

/** 按「自身耗时」聚合采样：self = 该帧被采到的 timeDeltas 之和。 */
function aggregateProfile(profile) {
  const byId = new Map(profile.nodes.map((node) => [node.id, node]));
  const selfById = new Map();
  const samples = profile.samples ?? [];
  const deltas = profile.timeDeltas ?? [];
  for (let i = 0; i < samples.length; i += 1) {
    const id = samples[i];
    const delta = deltas[i] ?? 0;
    selfById.set(id, (selfById.get(id) ?? 0) + delta);
  }

  const rows = new Map();
  for (const [id, selfUs] of selfById) {
    const node = byId.get(id);
    if (!node) continue;
    const frame = node.callFrame ?? {};
    const url = shortUrl(frame.url ?? "");
    const key = `${frame.functionName || "(anonymous)"} @ ${url}:${(frame.lineNumber ?? 0) + 1}`;
    const current = rows.get(key) ?? { selfUs: 0, samples: 0, url };
    current.selfUs += selfUs;
    current.samples += 1;
    rows.set(key, current);
  }

  const totalUs = [...rows.values()].reduce((sum, row) => sum + row.selfUs, 0);
  const sorted = [...rows.entries()]
    .map(([key, row]) => ({ key, ...row }))
    .sort((left, right) => right.selfUs - left.selfUs);
  return { totalUs, sorted };
}

function printTop(profile, label, limit = 30) {
  const { totalUs, sorted } = aggregateProfile(profile);
  const top = sorted.slice(0, limit);
  const topSum = top.reduce((sum, row) => sum + row.selfUs, 0);
  log("");
  log(`================ ${label}：自耗时 Top ${limit} ================`);
  log(`samples=${(profile.samples ?? []).length}  profiled=${(totalUs / 1000).toFixed(0)}ms  top${limit}=${(topSum / 1000).toFixed(0)}ms (${((topSum / totalUs) * 100).toFixed(1)}%)`);
  log("self_ms\tpct\tfn @ file:line");
  for (const row of top) {
    log(
      `${(row.selfUs / 1000).toFixed(1)}\t${((row.selfUs / totalUs) * 100).toFixed(1)}\t${row.key}`,
    );
  }

  // 再按「文件」聚合，抵消函数名分散（匿名闭包 / 内联）。
  const byFile = new Map();
  for (const row of sorted) {
    const file = row.url.replace(/:\d+$/, "");
    byFile.set(file, (byFile.get(file) ?? 0) + row.selfUs);
  }
  log("");
  log(`---- ${label}：按文件聚合（Top 15）----`);
  log("self_ms\tpct\tfile");
  for (const [file, selfUs] of [...byFile.entries()]
    .sort((left, right) => right[1] - left[1])
    .slice(0, 15)) {
    log(`${(selfUs / 1000).toFixed(1)}\t${((selfUs / totalUs) * 100).toFixed(1)}\t${file}`);
  }
  return { totalUs, sorted, byFile };
}

const METRIC_NAMES = [
  "TaskDuration",
  "ScriptDuration",
  "LayoutDuration",
  "RecalcStyleDuration",
  "LayoutCount",
  "RecalcStyleCount",
  "JSHeapUsedSize",
  "Nodes",
];

function pickMetrics(response) {
  const picked = {};
  for (const metric of response.metrics ?? []) {
    if (METRIC_NAMES.includes(metric.name)) picked[metric.name] = metric.value;
  }
  return picked;
}

function diffMetrics(before, after) {
  const deltas = {};
  for (const name of METRIC_NAMES) {
    deltas[name] = (after[name] ?? 0) - (before[name] ?? 0);
  }
  return deltas;
}

// --- 主流程 -----------------------------------------------------------------

async function main() {
  fs.mkdirSync(OUT_DIR, { recursive: true });
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
  apiChild.stdout.setEncoding("utf8");
  apiChild.stderr.setEncoding("utf8");
  apiChild.stderr.on("data", (data) => log(`[zz-audit-server:err] ${data.trim()}`));
  let apiPort = 0;
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
  viteChild.stderr.setEncoding("utf8");
  viteChild.stderr.on("data", (data) => log(`[vite:err] ${data.trim()}`));
  const baseUrl = `http://127.0.0.1:${vitePort}`;
  await waitForHttp(baseUrl);
  log(`[env] vite dev pid=${viteChild.pid} url=${baseUrl} (proxy /api -> 127.0.0.1:${apiPort})`);

  const browser = await chromium.launch({
    channel: "chrome",
    headless: true,
    args: [
      "--disable-background-timer-throttling",
      "--disable-backgrounding-occluded-windows",
      "--disable-renderer-backgrounding",
    ],
  });
  const context = await browser.newContext({
    viewport: { width: 1280, height: 900 },
    locale: "en-US",
    timezoneId: "UTC",
    deviceScaleFactor: 1,
  });
  const page = await context.newPage();
  page.on("pageerror", (error) => log(`[page] pageerror: ${error.message}`));
  const cdp = await context.newCDPSession(page);
  await cdp.send("Performance.enable");

  const prompt =
    MODE_ARG === "plain" ? "zzplain audit" : MODE_ARG === "table_stream" ? "zzstream audit" : "zzmount audit";

  await page.goto(`${baseUrl}/workspace`, { waitUntil: "domcontentloaded", timeout: 60_000 });
  await page.waitForSelector(".app-chat-input", { timeout: 60_000, state: "visible" });
  await page.waitForTimeout(1200);

  const composer = page.locator(".app-chat-input");
  await composer.fill(prompt);
  await composer.press("Control+Enter");
  await page.waitForFunction((needle) => document.body.innerText.includes(needle), "token0000", {
    timeout: 60_000,
    polling: 50,
  });
  log(`[${MODE_ARG}] first token visible, starting profiler`);

  const metricsBefore = pickMetrics(await cdp.send("Performance.getMetrics"));
  await cdp.send("Profiler.enable");
  await cdp.send("Profiler.setSamplingInterval", { interval: 200 });
  await cdp.send("Profiler.start");
  const startedAt = Date.now();
  await page.waitForFunction((needle) => document.body.innerText.includes(needle), "token1499", {
    timeout: 120_000,
    polling: 50,
  });
  const streamEndMs = Date.now() - startedAt;
  log(`[${MODE_ARG}] growth phase done in ${streamEndMs}ms`);
  // 多采 250ms 覆盖收尾提交（typewriter 追平 / settled 重渲染）。
  await page.waitForTimeout(250);
  const { profile } = await cdp.send("Profiler.stop");
  await cdp.send("Profiler.disable");
  const metricsAfter = pickMetrics(await cdp.send("Performance.getMetrics"));
  const deltas = diffMetrics(metricsBefore, metricsAfter);
  log(
    `[${MODE_ARG}] window=${((profile.endTime - profile.startTime) / 1000).toFixed(0)}ms TaskDur=${deltas.TaskDuration.toFixed(3)}s ScriptDur=${deltas.ScriptDuration.toFixed(3)}s`,
  );

  const tag = `${MODE_ARG}${PROD ? "-prod" : ""}`;
  const profilePath = path.join(OUT_DIR, `profile-${tag}.cpuprofile`);
  fs.writeFileSync(profilePath, JSON.stringify(profile));
  log(`[${MODE_ARG}] profile saved: ${profilePath}`);
  printTop(profile, `${tag} (${SECONDS}s window)`);

  log("");
  log(`---- ${tag}：CDP 指标增量 ----`);
  for (const [name, value] of Object.entries(deltas)) {
    log(`${name}\t${typeof value === "number" ? value.toFixed(3) : value}`);
  }

  await context.close();
  await browser.close();
}

main()
  .then(() => {
    killChildren();
    process.exit(0);
  })
  .catch((error) => {
    log(`[fatal] ${error?.stack ?? error}`);
    killChildren();
    process.exit(1);
  });
