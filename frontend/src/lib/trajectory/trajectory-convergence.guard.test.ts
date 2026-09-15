/**
 * P0-1 收敛前提守卫（批次 20）。
 *
 * 批次 18 的实时工具行收敛以「真实 provider call id」为唯一合并键
 * （`tool:<call_id>`，见 trajectory-reducer/apply.ts 的 applyToolEvent）。id 缺失
 * 时投影只能退化出合成身份（`tool-<seq>` / `history-tool-<i>-<j>`），此时同一次
 * 调用的两次到达**无法合并**。
 *
 * 本文件把三种降级表现钉成断言：任何后续改动若假定「已经收敛」，会先在这里失败，
 * 而不是在降级数据（缺 id 的帧 / 历史消息）上无声失效。
 */
import { describe, expect, it } from "vitest";

import type { SessionHistoryMessage } from "@/types/runtime";

import { sessionHistoryMessageToTrajectoryPushes } from "./session-history";
import { applyEvents, makeTrajectoryEvent } from "./trajectory-reducer";
import { createEmptyTrajectory } from "./types";

describe("P0-1 降级数据下的工具行身份（分支 b2）", () => {
  it("缺 call id 的工具帧退化为合成身份 tool-<seq>，与带 id 的权威帧分叉为两行", () => {
    const result = applyEvents(createEmptyTrajectory(), [
      makeTrajectoryEvent("tool_start", 1, { tool: { name: "read" } }),
      makeTrajectoryEvent("tool_end", 2, {
        tool: { id: "call_a", name: "read", output: "ok" },
      }),
    ]);

    // 身份不同 ⇒ 两个 Item：合成行永远等不到自己的终态帧。
    expect(result.snapshot.items.map((item) => item.id)).toEqual([
      "tool-1",
      "tool:call_a",
    ]);

    const synthetic = result.snapshot.items.find((item) => item.id === "tool-1");
    expect(synthetic?.status).toBe("running");
    expect(synthetic?.head.kind).toBe("tool");
    if (synthetic?.head.kind === "tool") {
      expect(synthetic.head.toolCallId).toBeUndefined();
      expect(synthetic.head.phase).toBe("started");
    }

    const authoritative = result.snapshot.items.find(
      (item) => item.id === "tool:call_a",
    );
    expect(authoritative?.status).toBe("completed");
  });

  it("合成身份按 seq 派生：同一工具的两条缺 id 帧也各自成行（不收敛）", () => {
    const result = applyEvents(createEmptyTrajectory(), [
      makeTrajectoryEvent("tool_start", 1, { tool: { name: "read" } }),
      makeTrajectoryEvent("tool_end", 2, { tool: { name: "read", output: "ok" } }),
    ]);

    expect(result.snapshot.items.map((item) => item.id)).toEqual([
      "tool-1",
      "tool-2",
    ]);
  });
});

describe("P0-1 悬挂终态（分支 a3 + c4）", () => {
  it("工具行只认同一身份的终态帧：缺席时保持 running，唯一的兜底是回合末 done", () => {
    const opened = applyEvents(createEmptyTrajectory(), [
      makeTrajectoryEvent("tool_start", 1, { tool: { id: "call_b", name: "bash" } }),
      makeTrajectoryEvent("chunk", 2, { type: "text", content: "…" }),
    ]);
    expect(
      opened.snapshot.items.find((item) => item.id === "tool:call_b")?.status,
    ).toBe("running");

    // done 是唯一会把悬挂行收尾为 completed 的事件（freezeOpenItems 只处理 error）。
    const finalized = applyEvents(opened.snapshot, [
      makeTrajectoryEvent("done", 3, {}),
    ]);
    expect(
      finalized.snapshot.items.find((item) => item.id === "tool:call_b")?.status,
    ).toBe("completed");
  });

  it("历史工具结果缺 tool_call_id 时整帧丢弃（既无行、也无终态帧）", () => {
    const message: SessionHistoryMessage = {
      role: "tool",
      content: "command finished",
      // 故意不带 tool_call_id：与「带 id 的 tool_start 行」无法对接。
    };
    expect(sessionHistoryMessageToTrajectoryPushes(message, 0)).toEqual([]);
  });

  it("历史工具结果带 tool_call_id 时产出 tool_end（对照组：收敛前提成立）", () => {
    const message: SessionHistoryMessage = {
      role: "tool",
      content: "command finished",
      tool_call_id: "call_b",
    };
    const pushes = sessionHistoryMessageToTrajectoryPushes(message, 0);
    expect(pushes).toHaveLength(1);
    expect(pushes[0]?.kind).toBe("tool_end");
  });
});
