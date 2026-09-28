// 重连窗口探针（诊断，非验收用例）：量化「流式中断 → 断线收敛 → 网络恢复 →
// 自动重连在线」整个窗口的主线程代价（长任务 / 帧间隔 / DOM 变更）。
//
// 与 zz-perf-probe.manual.ts 互补：后者测平稳配速流，本探针测断线/恢复两个
// 瞬态窗口。对齐 §10.1 基准工作流 2（活跃重连：流式中断线重连携带基线 →
// 收敛 → 继续流式）。断线用 Playwright `context.setOffline` 真实掐断进行中的
// SSE 长连接，恢复后走前端自动重连循环（use-session-runtime-stream 的
// sleep+重试，建连 onOpen → online）。
//
// 环境变量：
//   PERF_RECONNECT_ROUNDS  历史轮数（默认 8）——重连窗口的承载规模
//   PERF_TAG               报告文件名后缀（默认 reconnect）
//
// 跑法：npm run test:manual -- e2e/zz-reconnect-probe.manual.ts

import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import type { Page } from "@playwright/test";
import { expect, test } from "./fixtures";

import { ARTIFACTS_DIR, resetMockState, seedSessionHistory } from "./support";

const composer = (page: Page) => page.locator(".app-chat-input");
const log = (page: Page) => page.locator('[role="log"]');

const ROUNDS = Number(process.env.PERF_RECONNECT_ROUNDS ?? "8");
const TAG = process.env.PERF_TAG ?? "reconnect";

/** 精简回合历史：user + assistant 一段正文（重连窗口不测历史规模）。 */
function buildHistory(rounds: number): Array<Record<string, unknown>> {
  const history: Array<Record<string, unknown>> = [
    {
      role: "system",
      content: "You are a concise assistant.",
      metadata: { is_system_prompt: true },
    },
  ];
  for (let i = 0; i < rounds; i += 1) {
    history.push({
      role: "user",
      content: `Question ${i}: reconnect window probe?`,
    });
    history.push({
      role: "assistant",
      content: `Answer ${i}: the window is tail-first and converges after reconnect.\n\n- keeps the tail warm\n- drops nothing silently\n`,
    });
  }
  return history;
}

/** 页面内采样器：长任务 / 帧间隔 / DOM 变更（total + removals），带阶段打标。 */
async function installSampler(page: Page): Promise<void> {
  await page.evaluate(() => {
    interface SampleBag {
      longTasks: Array<{ start: number; dur: number }>;
      frames: number[];
      mutationsTotal: number;
      removals: number;
      removalNodes: number;
      perFrameMax: number;
      marks: Record<string, number>;
    }
    const bag: SampleBag = {
      longTasks: [],
      frames: [],
      mutationsTotal: 0,
      removals: 0,
      removalNodes: 0,
      perFrameMax: 0,
      marks: {},
    };
    (window as unknown as { __reconPerf: SampleBag }).__reconPerf = bag;
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        bag.longTasks.push({
          start: Math.round(entry.startTime),
          dur: Math.round(entry.duration),
        });
      }
    }).observe({ entryTypes: ["longtask"] });
    let inFrame = 0;
    new MutationObserver((records) => {
      bag.mutationsTotal += records.length;
      inFrame += records.length;
      for (const record of records) {
        if (record.type === "childList" && record.removedNodes.length > 0) {
          bag.removals += 1;
          bag.removalNodes += record.removedNodes.length;
        }
      }
    }).observe(document.body, { childList: true, subtree: true, characterData: true });
    let last = performance.now();
    const tick = (now: number) => {
      bag.frames.push(Math.round(now - last));
      if (inFrame > bag.perFrameMax) bag.perFrameMax = inFrame;
      inFrame = 0;
      last = now;
      (window as unknown as { __reconPerf: SampleBag }).__reconPerf = bag;
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  });
}

test("reconnect probe: disconnect-recovery main-thread cost", async ({ page }) => {
  test.setTimeout(180_000);
  await resetMockState(page.request);
  await page.goto("/workspace");
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });

  // 1) 热身一轮：先拿到 sessionId（历史 sync 的前置条件）。
  await composer(page).fill("capital check");
  await composer(page).press("Control+Enter");
  await expect(page.getByText("The capital of France is Paris.").first()).toBeVisible({
    timeout: 30_000,
  });

  // 2) 注入历史并刷新 → 控制会话规模。
  await seedSessionHistory(page.request, "e2e-session-1", buildHistory(ROUNDS));
  await page.reload();
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });
  if (ROUNDS > 0) {
    await expect(log(page)).toContainText(`Answer ${ROUNDS - 1}`, { timeout: 30_000 });
  }

  // 3) 安装采样器，提交配速流（scroll 脚本 = 长间隔 chunk）。
  await installSampler(page);
  const messagesBefore = await page.locator("[role='log'] [data-message-id]").count();
  await composer(page).fill("scroll paced stream");
  await composer(page).press("Control+Enter");
  // 等流式首条消息产生（不依赖具体文案，scroll 脚本 chunk 间隔长，留足窗口）。
  await page.waitForFunction(
    (before: number) =>
      document.querySelectorAll("[role='log'] [data-message-id]").length > before,
    messagesBefore,
    { timeout: 30_000 },
  );
  await page.waitForTimeout(1_500);

  // 4) 真实断线：掐断浏览器网络 → 进行中的 SSE 死亡 → 前端收敛为降级。
  await page.context().setOffline(true);
  const offlineBadgeSeen = await page
    .waitForSelector('[data-connection-status="offline"], [data-connection-status="reconnecting"]', {
      timeout: 10_000,
    })
    .then(() => true)
    .catch(() => false);
  await page.evaluate(() => {
    (window as unknown as { __reconPerf: { marks: Record<string, number> } }).__reconPerf.marks.t_offline =
      performance.now();
  });

  // 5) 恢复网络 → 自动重连循环收敛回在线。
  await page.context().setOffline(false);
  await page
    .waitForSelector('[data-connection-status="online"]', { timeout: 20_000 })
    .catch(() => undefined);
  await page.evaluate(() => {
    (window as unknown as { __reconPerf: { marks: Record<string, number> } }).__reconPerf.marks.t_online =
      performance.now();
  });
  await page.waitForTimeout(2_000); // 稳定窗口

  // 6) 取数：按阶段切分（pre = 断线前；recovery = 断线→在线；post = 稳定后）。
  const bag = await page.evaluate(
    (args: { ROUNDS: number; offlineBadgeSeen: boolean; TAG: string }) => {
      const b = (window as unknown as { __reconPerf: unknown }).__reconPerf as {
        longTasks: Array<{ start: number; dur: number }>;
        frames: number[];
        mutationsTotal: number;
        removals: number;
        removalNodes: number;
        perFrameMax: number;
        marks: Record<string, number>;
      };
      const tOffline = b.marks.t_offline ?? Number.POSITIVE_INFINITY;
      const tOnline = b.marks.t_online ?? Number.POSITIVE_INFINITY;
      const slice = (items: Array<{ start: number; dur: number }>) => ({
        pre: items.filter((e) => e.start < tOffline),
        recovery: items.filter((e) => e.start >= tOffline && e.start < tOnline),
        post: items.filter((e) => e.start >= tOnline),
      });
      const long = slice(b.longTasks);
      const summarize = (list: Array<{ start: number; dur: number }>) => ({
        count: list.length,
        totalMs: list.reduce((sum, e) => sum + e.dur, 0),
        maxMs: list.reduce((max, e) => Math.max(max, e.dur), 0),
      });
      return {
        scenario: {
          historyRounds: args.ROUNDS,
          offlineBadgeSeen: args.offlineBadgeSeen,
          report: args.TAG,
        },
        phases: {
          pre: summarize(long.pre),
          recovery: summarize(long.recovery),
          post: summarize(long.post),
        },
        frames: {
          count: b.frames.length,
          p95: b.frames.slice().sort((a, b2) => a - b2)[Math.floor(b.frames.length * 0.95)] ?? 0,
          max: b.frames.reduce((m, f) => Math.max(m, f), 0),
          over33: b.frames.filter((f) => f > 33).length,
          over100: b.frames.filter((f) => f > 100).length,
        },
        mutations: { total: b.mutationsTotal, perFrameMax: b.perFrameMax },
        removals: { count: b.removals, nodes: b.removalNodes },
        marks: b.marks,
      };
    },
    { ROUNDS, offlineBadgeSeen, TAG },
  );

  // 7) 落盘 + 摘要。
  mkdirSync(join(ARTIFACTS_DIR, "perf"), { recursive: true });
  const file = join(ARTIFACTS_DIR, "perf", `reconnect-${TAG}.json`);
  writeFileSync(file, JSON.stringify({ capturedAt: new Date().toISOString(), ...bag }, null, 2));
  const p = bag.phases;
  console.log(
    `[perf:reconnect-${TAG}] rounds=${ROUNDS} offlineBadge=${bag.scenario.offlineBadgeSeen} ` +
      `longTasks pre=${p.pre.count}(${p.pre.maxMs}ms) recovery=${p.recovery.count}(${p.recovery.maxMs}ms) ` +
      `post=${p.post.count}(${p.post.maxMs}ms) frames(p95=${bag.frames.p95} max=${bag.frames.max} ` +
      `over100=${bag.frames.over100}) mutations=${bag.mutations.total}(/frame max ${bag.mutations.perFrameMax}) ` +
      `removals=${bag.removals.count}/${bag.removals.nodes} marks=${JSON.stringify(bag.marks)}`,
  );
  expect(ARTIFACTS_DIR.length).toBeGreaterThan(0);
});