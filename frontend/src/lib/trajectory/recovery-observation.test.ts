// P0-2 拆分（原 recovery.test.ts L481-L562）：工具观测帧（chat.sse.observation）不建行。
// 拆出原因：recovery.test.ts 超 500 非空行门禁；该组用例自带 fixture 与重放器，独立成件。

import { describe, expect, it } from "vitest";

import type { SessionRuntimeEvent } from "@/types/runtime";

import { createEmptyTrajectory } from "./types";
import {
  advanceSeqCursor,
  applyEvent,
  makeTrajectoryEvent,
} from "./trajectory-reducer";
import { chatSseEventToTrajectoryPush, trajectoryEventAction } from "./recovery";
import { chatSseEvent } from "./recovery.test-fixtures";

/**
 * 第三个渲染缺陷（用户实测上报：「两个 shell 工具渲染在消息之后又重复地渲染」）。
 *
 * 实测会话 `session_20260915211037_9e9CQmZq`（单轮 953 帧）的行序：
 * `tool:call_00` / `tool:call_01` → 正文消息 → `observation-951` /
 * `observation-952`——后两行的载荷里是同一对 shell 工具的同一份
 * `step` / `tool` / `input` / `output`。
 *
 * 根因：后端 `buildObservedToolEventPayloads*`（tool_end，带 provider call id）
 * 与 `buildObservationEventPayloads`（observation，只有工具名 + step）在**同一
 * 批次**发同一份观测；前端聊天链路把 observation 当阶段信号（不建行），轨迹
 * 链路却按 `observation-<seq>` 又建一行。
 *
 * 契约（本文件钉住）：工具观测帧**不建行**、游标照常推进；无工具身份的观测帧
 * 仍按 G7 结构化行渲染。
 */
describe("工具观测帧（chat.sse.observation）不建行", () => {
  const toolEnd = (seq: number) =>
    chatSseEvent("tool_end", seq, {
      index: 1,
      entity: { kind: "tool", id: "call_00_x" },
      tool: {
        id: "call_00_x",
        name: "shell",
        args: { command: "git status" },
        content: "Exit code: 0",
      },
      tool_call: {
        id: "call_00_x",
        name: "shell",
        arguments: { command: "git status" },
      },
    });

  const toolObservation = (seq: number) =>
    chatSseEvent("observation", seq, {
      index: 1,
      step: "step_1_tool_0",
      tool: "shell",
      success: true,
      input: { command: "git status" },
      output: "Exit code: 0",
    });

  function replay(events: SessionRuntimeEvent[]) {
    let snapshot = createEmptyTrajectory();
    for (const event of events) {
      const action = trajectoryEventAction(event);
      if (action.kind === "push") {
        const seq = (action.push.payload._event as { sequence: number }).sequence;
        snapshot = applyEvent(
          snapshot,
          makeTrajectoryEvent(action.push.kind, seq, action.push.payload),
        ).snapshot;
      } else if (action.kind === "skip") {
        snapshot = advanceSeqCursor(snapshot, action.seq).snapshot;
      }
    }
    return snapshot;
  }

  it("映射层不产出推入：退化为 skip(seq)，游标照常推进", () => {
    expect(chatSseEventToTrajectoryPush(toolObservation(951))).toBeNull();
    const action = trajectoryEventAction(toolObservation(951));
    expect(action.kind).toBe("skip");
    if (action.kind === "skip") {
      expect(action.seq).toBe(951);
    }
  });

  it("同一观测只留下一行工具行，不再多出 observation-<seq>", () => {
    const snapshot = replay([toolEnd(949), toolObservation(951)]);
    expect(snapshot.items.map((item) => item.id)).toEqual(["tool:call_00_x"]);
  });

  it("无工具身份的观测帧语义不变：仍按结构化行渲染", () => {
    const push = chatSseEventToTrajectoryPush(
      chatSseEvent("observation", 7, { observation: "o" }),
    );
    expect(push?.kind).toBe("observation");
  });
});
