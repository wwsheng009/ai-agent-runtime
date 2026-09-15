import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures";
import { ARTIFACTS_DIR, resetMockState, seedSessionHistory } from "./support";

// 诊断探针（非验收用例）：统计流式期间「哪些组件在每帧重渲染」。
//
// 手法：每帧沿 React fiber 树走一遍，记录每个位置的 memoizedProps 对象身份；
// 与上一帧比较，身份变化 = 该组件这一帧真的重跑了 render（memo 命中的子树
// 会保留旧 props 对象，因此不会被计入）。只做归因，不参与耗时断言。
//
// 环境变量：PERF_ROUNDS（历史轮数，默认 0）、PERF_TAG（报告名）
// 跑法：npm run test:manual -- e2e/zz-render-probe.manual.ts

const composer = (page: Page) => page.locator(".app-chat-input");
const log = (page: Page) => page.locator('[role="log"]');

const HISTORY_ROUNDS = Number(process.env.PERF_ROUNDS ?? "0");
const TAG = process.env.PERF_TAG ?? "renders";

function buildHistory(rounds: number): Array<Record<string, unknown>> {
  const history: Array<Record<string, unknown>> = [];
  for (let i = 0; i < rounds; i += 1) {
    history.push({ role: "user", content: `Question ${i}: how is the window bounded?` });
    history.push({
      role: "assistant",
      content: `Answer ${i}: bounded, tail-first.\n\n\`\`\`ts\nconst w = pickTail(events);\n\`\`\`\n\n- one\n- two\n`,
    });
  }
  return history;
}

test("render probe: which components re-render during streaming", async ({ page }) => {
  test.setTimeout(240_000);
  await resetMockState(page.request);
  await page.goto("/workspace");
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });

  await composer(page).fill("capital check");
  await composer(page).press("Control+Enter");
  await expect(page.getByText("The capital of France is Paris.").first()).toBeVisible({
    timeout: 30_000,
  });

  await seedSessionHistory(page.request, "e2e-session-1", buildHistory(HISTORY_ROUNDS));
  await page.reload();
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });
  if (HISTORY_ROUNDS > 0) {
    await expect(log(page)).toContainText(`Answer ${HISTORY_ROUNDS - 1}`, { timeout: 30_000 });
  }

  await page.evaluate(() => {
    interface RenderStats {
      counts: Record<string, number>;
      frames: number;
      walking: boolean;
    }
    const stats: RenderStats = { counts: {}, frames: 0, walking: true };
    (window as unknown as { __renders: RenderStats }).__renders = stats;

    const containerKey = (el: Element) =>
      Object.keys(el).find((key) => key.startsWith("__reactContainer$"));
    const rootElement = document.getElementById("root") ?? document.body;
    const key = containerKey(rootElement);
    const rootFiber = key ? (rootElement as unknown as Record<string, unknown>)[key] : null;

    interface FiberLike {
      child?: FiberLike | null;
      sibling?: FiberLike | null;
      elementType?: unknown;
      type?: unknown;
      memoizedProps?: unknown;
    }

    function componentName(fiber: FiberLike): string | null {
      const type = fiber.elementType ?? fiber.type;
      if (typeof type === "string") return null; // host 元素
      if (typeof type === "function") {
        const fn = type as { displayName?: string; name?: string };
        return fn.displayName || fn.name || "(anon)";
      }
      if (type && typeof type === "object") {
        const wrapper = type as { displayName?: string; type?: unknown; render?: unknown };
        const inner = wrapper.type ?? wrapper.render;
        if (typeof inner === "function") {
          const fn = inner as { displayName?: string; name?: string };
          return `Memo(${fn.displayName || fn.name || "(anon)"})`;
        }
        return wrapper.displayName ? `Wrap(${wrapper.displayName})` : null;
      }
      return null;
    }

    /** 返回 path → { name, props } 的快照。 */
    function snapshot(): Map<string, { name: string; props: unknown }> {
      const out = new Map<string, { name: string; props: unknown }>();
      if (!rootFiber) return out;
      const stack: Array<{ fiber: FiberLike; path: string; depth: number }> = [
        { fiber: rootFiber as FiberLike, path: "r", depth: 0 },
      ];
      while (stack.length > 0) {
        const { fiber, path, depth } = stack.pop()!;
        if (depth > 60) continue;
        const name = componentName(fiber);
        if (name) out.set(path, { name, props: fiber.memoizedProps });
        let child = fiber.child;
        let index = 0;
        while (child) {
          stack.push({ fiber: child, path: `${path}.${index}`, depth: depth + 1 });
          child = child.sibling;
          index += 1;
        }
      }
      return out;
    }

    let previous = snapshot();
    const tick = () => {
      if (!stats.walking) return;
      const current = snapshot();
      stats.frames += 1;
      for (const [path, entry] of current) {
        const before = previous.get(path);
        if (before && before.name === entry.name && before.props !== entry.props) {
          stats.counts[entry.name] = (stats.counts[entry.name] ?? 0) + 1;
        }
      }
      previous = current;
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  });

  await composer(page).fill("perfmark stream load");
  await composer(page).press("Control+Enter");
  await expect(log(page)).toContainText("PERFMARK-END", { timeout: 120_000 });
  await page.waitForTimeout(1500);

  const stats = await page.evaluate(() => {
    const bag = (window as unknown as { __renders: { counts: Record<string, number>; frames: number; walking: boolean } })
      .__renders;
    bag.walking = false;
    return { counts: bag.counts, frames: bag.frames };
  });

  const ranked = Object.entries(stats.counts)
    .map(([name, count]) => ({
      name,
      count,
      perFrame: Math.round((count / Math.max(1, stats.frames)) * 1000) / 1000,
    }))
    .sort((a, b) => b.count - a.count);

  const dir = join(ARTIFACTS_DIR, "perf");
  mkdirSync(dir, { recursive: true });
  const path = join(dir, `${TAG}.json`);
  writeFileSync(
    path,
    JSON.stringify({ capturedAt: new Date().toISOString(), historyRounds: HISTORY_ROUNDS, frames: stats.frames, ranked }, null, 2),
    "utf8",
  );
  console.log(
    `[renders:${TAG}] frames=${stats.frames} components=${ranked.length} ` +
      `top=${ranked.slice(0, 6).map((r) => `${r.name}:${r.perFrame}/f`).join(" ")}`,
  );
});
