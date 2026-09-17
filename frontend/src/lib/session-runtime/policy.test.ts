/**
 * Batch 4（多会话并发运行时 §4.2 / §4.6）：订阅策略纯函数回归。
 *
 * 策略层只回答「谁该被订阅 / 以什么理由 / 谁该被释放」；live 名额与模式切换由
 * `registry.ts` 收敛（预算用例见 `registry.test.ts`）。这里锁定三件事：
 * - 选中 / 活跃回合 / 最近活动 / 陈旧 四类信号到 ensure·release 的映射；
 * - 选中会话即便同时出现在候选里也只结算一次（不产生第二次 ensure）；
 * - 10 个会话的规模下不丢候选、不误释放（策略与预算解耦）。
 */

import { describe, expect, it } from "vitest";

import {
  DEFAULT_RECENT_WINDOW_MS,
  planSessionSubscriptions,
} from "@/lib/session-runtime/policy";

const NOW = Date.parse("2026-09-17T12:00:00.000Z");

function iso(offsetMs: number): string {
  return new Date(NOW + offsetMs).toISOString();
}

describe("planSessionSubscriptions（订阅策略）", () => {
  it("选中 / 活跃回合 / 最近活动 / 陈旧分别映射到 selected / active-turn / recent / idle", () => {
    const plan = planSessionSubscriptions({
      selectedSessionId: "session-selected",
      candidates: [
        {
          sessionId: "session-active",
          hasActiveTurn: true,
          // 陈旧时间也要按活跃回合升级：有在途回合的会话不参与「最近活动」判定。
          updatedAt: iso(-60 * 60_000),
        },
        { sessionId: "session-recent", updatedAt: iso(-60_000) },
        { sessionId: "session-stale", updatedAt: iso(-30 * 60_000) },
        { sessionId: "session-unknown-time" },
      ],
      knownSessionIds: ["session-gone"],
      now: NOW,
    });

    expect(plan.ensure).toEqual([
      { sessionId: "session-selected", reason: "selected" },
      { sessionId: "session-active", reason: "active-turn" },
      { sessionId: "session-recent", reason: "recent" },
    ]);
    expect(plan.release).toEqual([
      { sessionId: "session-stale", reason: "idle" },
      { sessionId: "session-unknown-time", reason: "idle" },
      { sessionId: "session-gone", reason: "deselected" },
    ]);
  });

  it("窗口边界：正好等于最近活动窗口仍按 recent 订阅", () => {
    const plan = planSessionSubscriptions({
      selectedSessionId: null,
      candidates: [{ sessionId: "session-edge", updatedAt: iso(-DEFAULT_RECENT_WINDOW_MS) }],
      now: NOW,
    });

    expect(plan.ensure).toEqual([
      { sessionId: "session-edge", reason: "recent" },
    ]);
    expect(plan.release).toEqual([]);
  });

  it("选中会话同时出现在候选里时只结算一次，且不因候选释放", () => {
    const plan = planSessionSubscriptions({
      selectedSessionId: "session-a",
      candidates: [
        { sessionId: "session-a", hasActiveTurn: true },
        { sessionId: "session-b", updatedAt: iso(-1_000) },
      ],
      now: NOW,
    });

    expect(plan.ensure).toEqual([
      { sessionId: "session-a", reason: "selected" },
      { sessionId: "session-b", reason: "recent" },
    ]);
    expect(plan.release).toEqual([]);
  });

  it("重复候选去重（后一条生效）且跳过空白 sessionId", () => {
    const plan = planSessionSubscriptions({
      selectedSessionId: null,
      candidates: [
        { sessionId: " session-a " },
        { sessionId: "session-a", hasActiveTurn: true },
        { sessionId: "   " },
      ],
      now: NOW,
    });

    expect(plan.ensure).toEqual([
      { sessionId: "session-a", reason: "active-turn" },
    ]);
    expect(plan.release).toEqual([]);
  });

  it("10 个会话：所有候选都下发 ensure（live 数量由注册表预算收敛，策略不截断）", () => {
    const candidates = Array.from({ length: 9 }, (_, index) => ({
      sessionId: `session-bg-${index}`,
      updatedAt: iso(-index * 1_000),
    }));
    const plan = planSessionSubscriptions({
      selectedSessionId: "session-selected",
      // 其中一个是活跃回合（应按 active-turn 升级）。
      candidates: [...candidates, { sessionId: "session-running", hasActiveTurn: true }],
      now: NOW,
    });

    expect(plan.ensure).toHaveLength(11);
    expect(plan.ensure.filter((entry) => entry.reason === "selected")).toEqual([
      { sessionId: "session-selected", reason: "selected" },
    ]);
    expect(plan.ensure.filter((entry) => entry.reason === "active-turn")).toEqual([
      { sessionId: "session-running", reason: "active-turn" },
    ]);
    expect(plan.ensure.filter((entry) => entry.reason === "recent")).toHaveLength(9);
    expect(plan.release).toEqual([]);
  });
});
