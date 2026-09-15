/**
 * 真环境探针（诊断用，非验收用例）：直连正在运行的 dev server（默认 5193），
 * 用真后端的一次流式回复，量三件事：
 *   1. SSE **到达**节奏（谁在缓冲：服务端 / 代理 / 客户端）；
 *   2. 可见文本的**渐进增长**曲线（打字机是否真的在打字，还是结束时整体出现）；
 *   3. 主线程健康度（帧间隔 / >50ms 长任务）——「界面卡住」的直接证据。
 *
 * 只走 `/workspace/chats/new`（新建会话），不读写任何已有会话；不拦截任何请求，
 * 拿到的是真后端真实节奏。产物：`.artifacts/perf/live-<ts>.json`。
 *
 * 运行：
 *   $env:LIVE_BASE_URL="http://localhost:5193"; npm run test:manual -- e2e/zz-live-probe.manual.ts --reporter=line
 */
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { expect, test } from "./fixtures";

const BASE = (process.env.LIVE_BASE_URL ?? "http://localhost:5193").replace(/\/$/, "");
const WINDOW_MS = Number(process.env.LIVE_WINDOW_MS ?? "30000");
const IDLE_STOP_MS = 4000;
const PROVIDER = process.env.LIVE_PROVIDER ?? "opencode.ai";
const MODEL = process.env.LIVE_MODEL ?? "deepseek-v4.1-flash";
const EFFORT = process.env.LIVE_EFFORT ?? "max";
const PROMPT =
  process.env.LIVE_PROMPT ??
  "请用中文输出 60 行编号短句，每行不超过 20 字，不要调用任何工具。";

type Sample = {
  t: number;
  body: number;
  copyMax: number;
  copySum: number;
  lastCopy: number;
  tailMax: number;
  nodes: number;
  /** 助手正文真正落地的容器：消息行锚点（`data-chat-anchor-key`）。 */
  anchorCount: number;
  anchorTotal: number;
  anchorMax: number;
  /** 最后一条消息行锚点里的文本（单轮会话即助手正文），用于确认渲染的是正文。 */
  anchorLastText?: string;
};
type Arrival = { t: number; bytes: number };

declare global {
  interface Window {
    __live?: {
      start: number;
      samples: Sample[];
      arrivals: Arrival[];
      frames: number[];
      timer?: number;
      raf?: number;
      metaHead?: string;
      streams?: Array<{ url: string; t: number; bytes: number }>;
      longtasks?: number[];
      /** 解出的 SSE 帧：事件名 + 该帧携带的文本长度（正文/推理可达成长度）。 */
      events?: Array<{ t: number; ch: string; name: string; len: number }>;
    };
  }
}

test("真环境：流式回复的到达曲线 / 渐进渲染 / 主线程健康度", async ({ page }) => {
  test.setTimeout(180_000);

  await page.addInitScript(() => {
    const w = window as Window;
    const channels: Array<{ url: string; t: number; bytes: number }> = [];
    w.__live = {
      start: performance.now(),
      samples: [],
      arrivals: [],
      frames: [],
      streams: channels,
      longtasks: [],
      events: [],
    };
    const originalFetch = window.fetch;
    window.fetch = async (...args: Parameters<typeof fetch>) => {
      const response = await originalFetch(...args);
      const url = typeof args[0] === "string" ? args[0] : (args[0] as Request).url;
      const isChat = url.includes("/api/agent/chat");
      const isRuntimeStream =
        url.includes("/api/runtime/") && url.includes("stream");
      if (isChat || isRuntimeStream) {
        const body = response.body;
        if (body) {
          const [a, b] = body.tee();
          const key = url.split("?")[0];
          void (async () => {
            const reader = a.getReader();
            const decoder = new TextDecoder();
            let buffer = "";
            let eventName = "message";
            let data = "";
            // 帧解析：把「服务端到底发了什么增量、多大」记下来，与 DOM 曲线对照，
            // 才能分清「上游不给增量」和「前端不渲染增量」这两类完全不同的故障。
            const flushFrame = (t: number) => {
              if (data) {
                let len = data.length;
                try {
                  const parsed = JSON.parse(data) as Record<string, unknown>;
                  const payload = parsed.payload as Record<string, unknown> | undefined;
                  const delta = parsed.delta as Record<string, unknown> | undefined;
                  const candidate =
                    (typeof parsed.content === "string" && parsed.content) ||
                    (typeof parsed.delta === "string" && parsed.delta) ||
                    (typeof payload?.delta === "string" && payload.delta) ||
                    (typeof payload?.content === "string" && payload.content) ||
                    (typeof delta?.content === "string" && delta.content) ||
                    (typeof parsed.text === "string" && parsed.text) ||
                    "";
                  len = candidate ? candidate.length : 0;
                } catch {
                  len = -1;
                }
                const events = w.__live?.events;
                if (events && events.length < 4000) {
                  events.push({
                    t,
                    ch: isChat ? "chat" : "runtime",
                    name: eventName,
                    len,
                  });
                }
              }
              eventName = "message";
              data = "";
            };
            for (;;) {
              const { done, value } = await reader.read();
              if (done) {
                flushFrame(Math.round(performance.now() - (w.__live?.start ?? 0)));
                break;
              }
              if (value) {
                const t = Math.round(performance.now() - (w.__live?.start ?? 0));
                channels.push({ url: key, t, bytes: value.byteLength });
                buffer += decoder.decode(value, { stream: true });
                const lines = buffer.split(/\r?\n/);
                buffer = lines.pop() ?? "";
                for (const line of lines) {
                  if (line === "") {
                    flushFrame(t);
                    continue;
                  }
                  if (line.startsWith("event:")) {
                    eventName = line.slice(6).trim() || "message";
                  } else if (line.startsWith("data:")) {
                    data += line.slice(5).trimStart();
                  }
                }
                if (isChat) {
                  w.__live?.arrivals.push({ t, bytes: value.byteLength });
                  if (!w.__live?.metaHead) {
                    w.__live!.metaHead = new TextDecoder().decode(value).slice(0, 500);
                  }
                }
              }
            }
          })();
          return new Response(b, { status: response.status, statusText: response.statusText, headers: response.headers });
        }
      }
      return response;
    };
    const sample = () => {
      const copies = Array.from(document.querySelectorAll(".app-chat-copy"));
      const lens = copies.map((el) => (el.textContent ?? "").length);
      const tails = Array.from(document.querySelectorAll("[data-streaming-mode]")).map(
        (el) => (el.textContent ?? "").length,
      );
      // 消息行锚点：助手正文的唯一可靠去处。`.app-chat-copy` 在多轮会话里会被
      // 用户气泡占住，单看它会得到「正文只有 prompt 那么长」的假象（2026-09-15 实测）。
      const anchors = Array.from(
        document.querySelectorAll<HTMLElement>("[data-chat-anchor-key]"),
      );
      const anchorLens = anchors.map((el) => (el.textContent ?? "").length);
      const lastAnchor = anchors[anchors.length - 1];
      w.__live?.samples.push({
        t: Math.round(performance.now() - (w.__live?.start ?? 0)),
        body: document.body.innerText.length,
        copyMax: lens.length ? Math.max(...lens) : 0,
        copySum: lens.reduce((sum, len) => sum + len, 0),
        // 最后一条消息（单轮会话里即助手正文）+ 流式尾片段，避免被用户消息掩盖。
        lastCopy: lens.length ? lens[lens.length - 1] : 0,
        tailMax: tails.length ? Math.max(...tails) : 0,
        nodes: lens.length,
        anchorCount: anchors.length,
        anchorTotal: anchorLens.reduce((sum, len) => sum + len, 0),
        anchorMax: anchorLens.length ? Math.max(...anchorLens) : 0,
        anchorLastText: lastAnchor
          ? (lastAnchor.textContent ?? "").replace(/\s+/g, " ").slice(-160)
          : undefined,
      });
    };
    w.__live.timer = window.setInterval(sample, 20);
    try {
      new PerformanceObserver((list) => {
        for (const entry of list.getEntries()) {
          w.__live?.longtasks?.push(Math.round(entry.duration));
        }
      }).observe({ entryTypes: ["longtask"] });
    } catch {
      /* 不支持 longtask 的环境忽略 */
    }
    let last = performance.now();
    const frame = (now: number) => {
      w.__live?.frames.push(Math.round(now - last));
      last = now;
      w.__live!.raf = requestAnimationFrame(frame);
    };
    w.__live.raf = requestAnimationFrame(frame);
  });

  // 把 UI 的 chat 请求钉到指定 provider/model/effort（不改渲染路径，只改请求体）。
  await page.route("**/api/agent/chat", async (route) => {
    const request = route.request();
    if (request.method() !== "POST") {
      await route.continue();
      return;
    }
    let body: Record<string, unknown>;
    try {
      body = JSON.parse(request.postData() ?? "{}") as Record<string, unknown>;
    } catch {
      await route.continue();
      return;
    }
    body.provider = PROVIDER;
    body.model = MODEL;
    body.reasoning_effort = EFFORT;
    // 纯流式：去掉 ReAct 工具循环，使 UI 路径与直连探针同源（否则长间隔来自工具阶段）。
    body.enable_react = false;
    await route.continue({ postData: JSON.stringify(body) });
  });

  await page.goto(`${BASE}/workspace/chats/new`);
  const composer = page.locator(".app-chat-input");
  await expect(composer).toBeVisible({ timeout: 30_000 });

  // CPU 采样：定位极端长任务里的热点函数（hits ≈ 毫秒 @1ms 采样）。
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("Profiler.enable");
  await cdp.send("Profiler.start");

  await composer.fill(PROMPT);
  await composer.press("Control+Enter");

  // 采样窗口：**正文与网络都**停止增长 IDLE_STOP_MS 后才收尾。
  //
  // 旧判据只看 `.app-chat-copy` 的增长：多轮会话里那个容器被用户气泡占住，
  // 提示词一落地就再不长，于是探针在**流式还没结束**时就提前收尾（实测
  // windowMs 6.4s，而 SSE 到达一直持续到 5.1s 之后），拿到的「growthEvents」
  // 全部来自开头的用户气泡 —— 这正是前几轮把「打字机正常」量错的原因。
  const deadline = Date.now() + WINDOW_MS;
  let lastAnchorTotal = 0;
  let lastArrivals = 0;
  let lastGrowthAt = Date.now();
  let grown = false;
  for (;;) {
    await page.waitForTimeout(250);
    const snapshot = await page.evaluate(() => {
      const probe = window.__live;
      const samples = probe?.samples ?? [];
      const last = samples[samples.length - 1];
      return {
        anchorTotal: last?.anchorTotal ?? 0,
        arrivals: probe?.arrivals.length ?? 0,
      };
    });
    if (snapshot.anchorTotal > lastAnchorTotal + 20) {
      lastAnchorTotal = snapshot.anchorTotal;
      lastGrowthAt = Date.now();
      grown = true;
    }
    if (snapshot.arrivals > lastArrivals) {
      lastArrivals = snapshot.arrivals;
      lastGrowthAt = Date.now();
      grown = true;
    }
    if (grown && Date.now() - lastGrowthAt > IDLE_STOP_MS) break;
    if (Date.now() > deadline) break;
  }

  const probe = await page.evaluate(() => {
    const w = window as Window;
    if (w.__live?.timer) window.clearInterval(w.__live.timer);
    if (w.__live?.raf) cancelAnimationFrame(w.__live.raf);
    return w.__live ?? null;
  });

  // 收尾 DOM 结构快照：每个「消息行」的类型与文本长度。用于回答「助手正文到底
  // 落在哪个容器里」——前几轮之所以把正文量成 36 字符（= 用户提示词），就是
  // 因为选了 `.app-chat-copy` 而不是带 flow kind 的消息行。
  const flowRows = await page.evaluate(() =>
    Array.from(
      document.querySelectorAll<HTMLElement>("[data-chat-flow-kind]"),
    ).map((el) => ({
      kind: el.getAttribute("data-chat-flow-kind") ?? "",
      len: (el.textContent ?? "").length,
    })),
  );
  expect(probe).not.toBeNull();

  const profiled = (await cdp.send("Profiler.stop")) as unknown as {
    profile: {
      nodes: Array<{
        callFrame: { functionName: string; url: string };
        hitCount?: number;
      }>;
      samples?: number[];
      timeDeltas?: number[];
    };
  };
  const selfTime = new Map<string, { fn: string; url: string; hits: number }>();
  for (const node of profiled.profile.nodes) {
    const hits = node.hitCount ?? 0;
    if (!hits) continue;
    const fn = node.callFrame.functionName || "(anonymous)";
    const key = `${fn} @ ${node.callFrame.url}`;
    const cur = selfTime.get(key) ?? { fn, url: node.callFrame.url, hits: 0 };
    cur.hits += hits;
    selfTime.set(key, cur);
  }
  const hotFunctions = [...selfTime.values()]
    .sort((a, b) => b.hits - a.hits)
    .slice(0, 25);
  const totalSamples = profiled.profile.samples?.length ?? 0;
  const profiledMs = (profiled.profile.timeDeltas ?? []).reduce((sum, value) => sum + value, 0) / 1000;

  const samples = probe!.samples;
  const growth = (pick: (s: Sample) => number) => {
    const out: { t: number; from: number; to: number }[] = [];
    for (let i = 1; i < samples.length; i += 1) {
      const from = pick(samples[i - 1]);
      const to = pick(samples[i]);
      if (to !== from) out.push({ t: samples[i].t, from, to });
    }
    return out;
  };
  const describe = (points: { t: number; from: number; to: number }[]) => {
    let maxGap = 0;
    let maxGapAt = -1;
    for (let i = 1; i < points.length; i += 1) {
      const gap = points[i].t - points[i - 1].t;
      if (gap > maxGap) {
        maxGap = gap;
        maxGapAt = points[i].t;
      }
    }
    return {
      growthEvents: points.length,
      typingSteps: points.filter((p) => p.to - p.from > 0 && p.to - p.from <= 8).length,
      largeJumps: points.filter((p) => p.to - p.from > 120).length,
      maxGapMs: maxGap,
      maxGapAtMs: maxGapAt,
      finalValue: points.length ? points[points.length - 1].to : 0,
      first: points.slice(0, 8),
      tail: points.slice(-5),
    };
  };

  const copyGrowth = growth((s) => s.copyMax);
  const lastCopyGrowth = growth((s) => s.lastCopy);
  const tailGrowth = growth((s) => s.tailMax);
  // 助手正文曲线（消息行锚点）：这三条才是「打字机到底有没有在打字」的证据。
  const anchorGrowth = growth((s) => s.anchorMax);
  const anchorTotalGrowth = growth((s) => s.anchorTotal);
  const longtasks = probe!.longtasks ?? [];
  const frames = probe!.frames.filter((value) => value > 0);
  const sorted = [...frames].sort((a, b) => a - b);
  const arrivals = probe!.arrivals;
  const arrivalSpread = arrivals.length
    ? {
        count: arrivals.length,
        firstAt: arrivals[0].t,
        lastAt: arrivals[arrivals.length - 1].t,
        spanMs: arrivals[arrivals.length - 1].t - arrivals[0].t,
        maxGapMs: arrivals.reduce(
          (max, cur, index) => (index === 0 ? 0 : Math.max(max, cur.t - arrivals[index - 1].t)),
          0,
        ),
      }
    : { count: 0 };

  const channelMap = new Map<
    string,
    { url: string; count: number; firstAt: number; lastAt: number; bytes: number }
  >();
  for (const item of probe!.streams ?? []) {
    const cur = channelMap.get(item.url) ?? {
      url: item.url,
      count: 0,
      firstAt: item.t,
      lastAt: item.t,
      bytes: 0,
    };
    cur.count += 1;
    cur.lastAt = item.t;
    cur.bytes += item.bytes;
    channelMap.set(item.url, cur);
  }

  // SSE 帧聚合：按（通道，事件名）统计帧数与文本量。若 `*_delta` 帧的 len 都是 0，
  // 说明上游只发状态不发增量；若 len 很大而 DOM 不动，则是前端没渲染。
  const sseEvents = probe!.events ?? [];
  const eventMap = new Map<
    string,
    { ch: string; name: string; count: number; totalLen: number; firstAt: number; lastAt: number }
  >();
  for (const item of sseEvents) {
    const k = `${item.ch}:${item.name}`;
    const cur = eventMap.get(k) ?? {
      ch: item.ch,
      name: item.name,
      count: 0,
      totalLen: 0,
      firstAt: item.t,
      lastAt: item.t,
    };
    cur.count += 1;
    cur.totalLen += Math.max(0, item.len);
    cur.lastAt = item.t;
    eventMap.set(k, cur);
  }

  const summary = {
    base: BASE,
    requested: `${PROVIDER} / ${MODEL} / ${EFFORT}`,
    channels: [...channelMap.values()].map((c) => ({
      ...c,
      spanMs: c.lastAt - c.firstAt,
    })),
    windowMs: samples.length ? samples[samples.length - 1].t : 0,
    prompt: PROMPT,
    metaHead: (probe!.metaHead ?? "").replace(/\n/g, "|").slice(0, 300),
    copy: describe(copyGrowth),
    lastCopy: describe(lastCopyGrowth),
    streamingTail: describe(tailGrowth),
    anchor: describe(anchorGrowth),
    anchorTotal: describe(anchorTotalGrowth),
    anchorLastText: samples.length
      ? (samples[samples.length - 1].anchorLastText ?? "")
      : "",
    flowRows,
    sseEvents: [...eventMap.values()].map((e) => ({ ...e, spanMs: e.lastAt - e.firstAt })),
    longtaskCount: longtasks.length,
    longtaskTotalMs: longtasks.reduce((sum, value) => sum + value, 0),
    longtaskMaxMs: longtasks.length ? Math.max(...longtasks) : 0,
    cpuProfile: { totalSamples, profiledMs, hot: hotFunctions },
    body: describe(growth((s) => s.body)),
    frameP95Ms: sorted[Math.floor(sorted.length * 0.95)] ?? 0,
    framesOver100ms: frames.filter((value) => value > 100).length,
    frameSamples: frames.length,
    arrivals: arrivalSpread,
  };

  mkdirSync(join(process.cwd(), ".artifacts", "perf"), { recursive: true });
  const file = join(process.cwd(), ".artifacts", "perf", `live-${Date.now()}.json`);
  writeFileSync(
    file,
    JSON.stringify({ summary, samples, frames, events: probe!.events ?? [] }, null, 2),
    "utf8",
  );
  process.stdout.write(`\n[live] ${JSON.stringify(summary, null, 2)}\n[live] raw -> ${file}\n`);
});
