import { describe, expect, it } from "vitest";

import { getPublicationLevel, type PublicationLevel } from "./publication";

function runtimeEvent(type: string) {
  return { type, timestamp: "2026-09-28T00:00:00.000Z" };
}

describe("getPublicationLevel（P1-2 发布分级）", () => {
  it("delta 家族 → animation-frame（正文/推理/图片增量走 120ms 下界 + 三帧门）", () => {
    // getRuntimeDeltaKind 的覆盖名单由事件契约派生（含旧拼写别名），这里只抽代表。
    for (const type of [
      "assistant_delta",
      "assistant.delta",
      "assistant.reasoning",
      "assistant_reasoning",
      "assistant.image_progress",
    ]) {
      expect(getPublicationLevel(runtimeEvent(type))).toBe("animation-frame");
    }
  });

  it("live-only 节流镜像 → animation-frame（有 UI 落点，帧门吸收频率，不冻结）", () => {
    // P1-5：tool/subagent.progress 是后端节流后的 live-only 镜像，驱动工具行 /
    // 子代理进度条——归 none 会让"纯进度流"不提交（进度冻结），故走帧级档。
    for (const type of ["tool.progress", "subagent.progress", "subagent.batch.progress"]) {
      expect(getPublicationLevel(runtimeEvent(type))).toBe("animation-frame");
    }
  });

  it("持久事件 → immediate（结算/工具生命周期/回滚/approval 立即上屏）", () => {
    for (const type of [
      "assistant_message",
      "agent.turn.started",
      "agent.turn.finished",
      "tool_started",
      "tool_finished",
      "tool.requested",
      "tool.completed",
      "backtrack_started",
      "backtrack_finished",
      "approval_requested",
      "approval_resolved",
      "patch.applied",
      "llm.retry",
      "session_start",
      "session_end",
    ]) {
      expect(getPublicationLevel(runtimeEvent(type))).toBe("immediate");
    }
  });

  it("三档枚举存在；none 为对齐参考 usage/finish 的占位语义（当前契约无落点）", () => {
    // 参考 assistant.ts:325 的 usage/finish → none 在本仓库事件流中没有对应类型
    // （usage 走 analytics REST）；分级函数当前只产出 animation-frame / immediate，
    // none 类型保留以便事件契约演进时落地，且不许悄悄删档。
    const levels = new Set<PublicationLevel>([
      "none",
      "animation-frame",
      "immediate",
    ]);
    levels.delete(getPublicationLevel(runtimeEvent("assistant_delta")));
    levels.delete(getPublicationLevel(runtimeEvent("assistant_message")));
    expect(levels).toEqual(new Set(["none"]));
  });

  it("分类覆盖门禁：契约代表性快照全部有确定分级（防漂移）", () => {
    const representative = [
      "agent.reclaimed",
      "agent.turn.finished",
      "assistant.image_progress",
      "assistant_delta",
      "assistant_message",
      "backtrack_finished",
      "checkpoint_created",
      "llm.request.started",
      "llm_request_finished",
      "patch.applied",
      "plan_mode_changed",
      "question_asked",
      "session.interrupted",
      "subagent.batch.progress",
      "subagent.completed",
      "tool.completed",
      "tool.progress",
      "tool_started",
      "turn.resumed",
      "turn.suspended",
    ];
    const seen = new Set<PublicationLevel>();
    for (const type of representative) {
      seen.add(getPublicationLevel(runtimeEvent(type)));
    }
    // 帧级档与立即档都有落点，且立即档是持久事件的默认（覆盖最广）。
    expect(seen).toEqual(new Set(["animation-frame", "immediate"]));
  });
});