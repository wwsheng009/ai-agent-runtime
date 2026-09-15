/**
 * 打字机探针（诊断用，非验收用例）——「网络到达」与「DOM 更新」双通道采样。
 *
 * 三条曲线一次拿齐：
 * 1. `/api/agent/chat` 的 SSE **到达时刻**（谁在缓冲：服务端/代理/客户端）；
 * 2. `/api/runtime/.../runtime/stream` 的 live 事件到达时刻（方案B 打字机数据源）；
 * 3. 渲染结果（body / 消息锚点 / copy 容器的文本长度）与帧间隔。
 *
 * 运行：
 *   npm run test:manual -- e2e/zz-typewriter-probe.manual.ts --reporter=line
 *   $env:PROBE_BASE_URL="http://localhost:5193"; npm run test:manual -- e2e/zz-typewriter-probe.manual.ts
 *
 * 产物：`.artifacts/perf/typewriter-<ts>.json`
 */
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { expect, test } from "./fixtures";
import { seedSession } from "./support";

const BASE = process.env.PROBE_BASE_URL?.replace(/\/$/, "") ?? "";
const PROMPT = process.env.PROBE_PROMPT ?? "scroll typewriter probe";
const SAMPLE_MS = 20;
const WINDOW_MS = Number(process.env.PROBE_WINDOW_MS ?? "12000");

function url(path: string): string {
  return BASE ? `${BASE}${path}` : path;
}

type SelectorStat = { count: number; total: number; max: number };
type Sample = { t: number; body: number; anchor: SelectorStat };
type Arrival = {
  t: number;
  kind: "read" | "event";
  bytes?: number;
  name?: string;
  len?: number;
  chan?: "chat" | "runtime";
  /** 前若干条事件的 data 摘录：用于核对 turn 身份/事件类型（不解析正文全文）。 */
  sample?: string;
};
type Mark = { t: number; owners: string[]; tail: string };

type Probe = {
  start: number;
  samples: Sample[];
  frames: [number, number][];
  marks: Mark[];
  arrivals: Arrival[];
  timer?: number;
};

test("打字机探针：网络到达 vs DOM 更新", async ({ page }) => {
  test.setTimeout(240_000);

  // 在页面里给 fetch 装分流阀：只观察，不改变主链路（clone 出第二条读流）。
  await page.addInitScript(() => {
    const w = window as unknown as { __tw?: Probe; __twStart?: number };
    const origFetch = window.fetch.bind(window);
    w.__twStart = performance.now();
    window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
      const target = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      const response = await origFetch(input as RequestInfo, init);
      const isChat = target.includes("/api/agent/chat");
      const isRuntime = target.includes("/runtime/stream");
      if (!isChat && !isRuntime) return response;
      const chan = isChat ? ("chat" as const) : ("runtime" as const);
      let sampled = 0;
      const started = performance.now();
      const push = (a: Omit<Arrival, "t">) => {
        w.__tw?.arrivals.push({ t: Math.round(performance.now() - started), ...a } as Arrival);
      };
      const sink = response.clone();
      void (async () => {
        try {
          const reader = sink.body?.getReader();
          if (!reader) return;
          const decoder = new TextDecoder();
          let buffer = "";
          let name = "message";
          let data = "";
          while (true) {
            const { done, value } = await reader.read();
            if (done) {
              push({ kind: "read", bytes: -1 });
              break;
            }
            push({ kind: "read", bytes: value.length });
            buffer += decoder.decode(value, { stream: true });
            const lines = buffer.split(/\r?\n/);
            buffer = lines.pop() ?? "";
            for (const line of lines) {
              if (line === "") {
                if (data) {
                  let len = 0;
                  try {
                    const p = JSON.parse(data) as Record<string, unknown>;
                    const text = p.content ?? (p.delta as Record<string, unknown> | undefined)?.content ?? p.delta;
                    len = typeof text === "string" ? text.length : 0;
                  } catch {
                    len = data.length;
                  }
                  // 只留前 12 条事件的 data 摘录：够看清事件类型与 turn 身份，
                  // 不会把 4000 条事件的正文塞进产物。
                  const sample = sampled < 12 ? data.slice(0, 220) : undefined;
                  sampled += 1;
                  push({ kind: "event", name, len, chan, sample });
                }
                name = "message";
                data = "";
                continue;
              }
              if (line.startsWith("event:")) name = line.slice(6).trim() || "message";
              else if (line.startsWith("data:")) data += line.slice(5).trimStart();
            }
          }
        } catch {
          /* tee 失败不影响主链路 */
        }
      })();
      return response;
    };
  });

  await page.request.post(url("/api/_test/reset"));
  const sessionId = BASE
    ? ((
        await (
          await page.request.post(url("/api/runtime/sessions"), {
            data: { title: "typewriter probe" },
          })
        ).json()
      ).session_id as string)
    : await seedSession(page.request, { title: "typewriter probe" });

  await page.goto(url(`/workspace/chats/${sessionId}`));
  const composer = page.locator(".app-chat-input");
  await expect(composer).toBeVisible({ timeout: 30_000 });

  await page.evaluate(
    ({ sampleMs }) => {
      const w = window as unknown as { __tw?: Probe };
      const stat = (selector: string) => {
        const nodes = document.querySelectorAll(selector);
        let total = 0;
        let max = 0;
        nodes.forEach((node) => {
          const len = (node.textContent ?? "").length;
          total += len;
          if (len > max) max = len;
        });
        return { count: nodes.length, total, max };
      };
      const prev = w.__tw;
      w.__tw = {
        start: performance.now(),
        samples: [],
        frames: [],
        marks: [],
        arrivals: prev?.arrivals ?? [],
      };
      let lastFrame = performance.now();
      const frame = () => {
        const now = performance.now();
        w.__tw!.frames.push([Math.round(now - w.__tw!.start), Math.round(now - lastFrame)]);
        lastFrame = now;
        requestAnimationFrame(frame);
      };
      requestAnimationFrame(frame);
      w.__tw.timer = window.setInterval(() => {
        w.__tw!.samples.push({
          t: Math.round(performance.now() - w.__tw!.start),
          body: document.body.textContent.length,
          anchor: stat("[data-chat-anchor-key]"),
        });
      }, sampleMs);
    },
    { sampleMs: SAMPLE_MS },
  );

  const mark = async (waitMs: number) => {
    await page.waitForTimeout(waitMs);
    await page.evaluate(() => {
      const w = window as unknown as { __tw?: Probe };
      // 文本「所有者」：自身有长文本、且没有更长的子元素——即真正承载正文的节点。
      const owners: string[] = [];
      document.querySelectorAll("div,p,pre,span,li,td").forEach((el) => {
        const len = (el.textContent ?? "").length;
        if (len < 80) return;
        let childMax = 0;
        el.querySelectorAll(":scope > *").forEach((child) => {
          childMax = Math.max(childMax, (child.textContent ?? "").length);
        });
        if (childMax >= len * 0.9) return;
        const cls = typeof el.className === "string" ? el.className.slice(0, 60) : "";
        owners.push(`${el.tagName.toLowerCase()}.${cls} len=${len}`);
      });
      w.__tw?.marks.push({
        t: Math.round(performance.now() - (w.__tw?.start ?? 0)),
        owners: owners.slice(0, 10),
        tail: document.body.innerText.replace(/\s+/g, " ").slice(-260),
      });
    });
  };

  await composer.fill(PROMPT);
  await composer.press("Control+Enter");

  await mark(1500);
  await mark(2500);
  await mark(4000);
  await mark(Math.max(1000, WINDOW_MS - 9500));

  const probe = (await page.evaluate(() => {
    const w = window as unknown as { __tw?: Probe };
    if (w.__tw?.timer) window.clearInterval(w.__tw.timer);
    return w.__tw ?? null;
  })) as Probe | null;

  expect(probe).not.toBeNull();
  const samples = probe!.samples;

  const growthOf = (pick: (s: Sample) => number) => {
    const out: { t: number; from: number; to: number }[] = [];
    for (let i = 1; i < samples.length; i += 1) {
      const from = pick(samples[i - 1]);
      const to = pick(samples[i]);
      if (to !== from) out.push({ t: samples[i].t, from, to });
    }
    return out;
  };

  const describe = (
    growth: { t: number; from: number; to: number }[],
    finalValue: number,
  ) => {
    let maxGap = 0;
    let maxGapAt = -1;
    for (let i = 1; i < growth.length; i += 1) {
      const gap = growth[i].t - growth[i - 1].t;
      if (gap > maxGap) {
        maxGap = gap;
        maxGapAt = growth[i].t;
      }
    }
    return {
      growthEvents: growth.length,
      typingSteps: growth.filter((g) => g.to - g.from > 0 && g.to - g.from <= 8).length,
      largeJumps: growth.filter((g) => g.to - g.from > 120).length,
      maxGapMs: maxGap,
      maxGapAtMs: maxGapAt,
      finalValue,
      firstGrowth: growth.slice(0, 20),
      tailGrowth: growth.slice(-6),
    };
  };

  const frames = probe!.frames.map(([, d]) => d).filter((d) => d > 0);
  const sorted = [...frames].sort((a, b) => a - b);

  const chatArrivals = probe!.arrivals.filter((a) => a.kind === "event");
  const summary = {
    prompt: PROMPT,
    base: BASE || "(e2e preview)",
    finalBodyLength: samples[samples.length - 1]?.body ?? 0,
    finalAnchor: samples[samples.length - 1]?.anchor ?? null,
    body: describe(growthOf((s) => s.body), samples[samples.length - 1]?.body ?? 0),
    anchorTotal: describe(growthOf((s) => s.anchor.total), samples[samples.length - 1]?.anchor.total ?? 0),
    frameP95Ms: sorted[Math.floor(sorted.length * 0.95)] ?? 0,
    framesOver100ms: frames.filter((d) => d > 100).length,
    // SSE 到达（两条流合并，按 t 排序）：读事件与解出的事件各记一次
    arrivals: probe!.arrivals,
    chatEventCount: chatArrivals.length,
    // 前若干条事件的 data 摘录（chat/runtime 各带 chan），用于核对真后端
    // 实际投递的事件类型与 turn 身份，避免再靠猜。
    firstEvents: probe!.arrivals
      .filter((a) => a.kind === "event" && a.sample)
      .slice(0, 16)
      .map((a) => ({ t: a.t, chan: a.chan, name: a.name, sample: a.sample })),
    marks: probe!.marks,
  };

  const dir = join(process.cwd(), ".artifacts", "perf");
  mkdirSync(dir, { recursive: true });
  const file = join(dir, `typewriter-${Date.now()}.json`);
  writeFileSync(file, JSON.stringify({ summary, probe }, null, 2), "utf8");

  process.stdout.write(`\n[typewriter] ${JSON.stringify(summary, null, 2)}\n`);
  process.stdout.write(`[typewriter] raw -> ${file}\n`);
});
