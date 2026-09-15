/**
 * live 渲染的顺序契约（用户实测上报：已渲染的消息在下一次渲染时被重排到末尾）。
 *
 * 症状与根因（本批实测，会话 `session_20260915191107_eNMJeqJt` 真实事件流）：
 * - 正文 / 思考行此前共用**全局固定 id**（`assistant` / `reasoning`），行位置被
 *   钉死在创建它的那一帧：整窗 66 行里正文行恒为第 0 项，其后 31 个工具行 +
 *   30 个观察行全部追加在它之后——同一行跨越所有轮次，新内容永远不会出现在它
 *   所属的时间位置上；
 * - `done` 的 `finalizeOpenItems` 把仍在 running 的行冻结为终态，而
 *   `upsertItem` 对终态行**拒绝 upsert** ⇒ 下一轮的增量帧命中同一 id 时被静默
 *   丢弃（要么不渲染，要么被复用旧行）。
 *
 * 修复后的契约（本文件钉住）：
 * 1. 行身份按轮次收敛 `assistant:<turn_id>` / `reasoning:<turn_id>`；
 * 2. 行**只追加**，既有行的下标永不变化（这就是「顺序概念」）；
 * 3. 跨轮的新消息行落在既有行之后，按事件顺序就位；
 * 4. 无 turn 标识（降级帧 / 旧帧）与显式身份帧保持既有语义不变。
 */
import { describe, expect, it } from "vitest";

import {
  applyEvent,
  makeTrajectoryEvent,
} from "@/lib/trajectory/trajectory-reducer";
import {
  TRAJECTORY_ITEM_ID_KEY,
  createEmptyTrajectory,
  type TrajectoryEventKind,
  type TrajectorySnapshot,
} from "@/lib/trajectory/types";

type Tuple = [kind: TrajectoryEventKind, seq: number, payload: Record<string, unknown>];

function applyTuples(tuples: Tuple[]): TrajectorySnapshot {
  let snapshot = createEmptyTrajectory();
  for (const [kind, seq, payload] of tuples) {
    snapshot = applyEvent(snapshot, makeTrajectoryEvent(kind, seq, payload)).snapshot;
  }
  return snapshot;
}

function itemIds(snapshot: TrajectorySnapshot): string[] {
  return snapshot.items.map((item) => item.id);
}

const text = (content: string, turnId: string) => ({
  type: "text",
  content,
  turn_id: turnId,
});

describe("live 渲染顺序契约", () => {
  it("跨轮正文各自成行，新消息行按事件顺序追加在既有行之后", () => {
    const snapshot = applyTuples([
      ["chunk", 1, text("第一轮", "turn-a")],
      ["tool_start", 2, { type: "tool_call", tool_call: { id: "call-1", name: "bash" } }],
      ["chunk", 3, text("第二轮", "turn-b")],
    ]);

    // 消息行不再被钉在第 0 项：第二轮正文出现在工具行之后（= 它所属的时间位置）。
    expect(itemIds(snapshot)).toEqual([
      "assistant:turn-a",
      "tool:call-1",
      "assistant:turn-b",
    ]);
  });

  it("同一轮的增量收敛到同一行（不新增行，内容累加）", () => {
    const snapshot = applyTuples([
      ["chunk", 1, text("hello ", "turn-a")],
      ["chunk", 2, text("world", "turn-a")],
    ]);

    expect(itemIds(snapshot)).toEqual(["assistant:turn-a"]);
    const head = snapshot.items[0].head;
    expect(head.kind === "text" ? head.content : "").toBe("hello world");
  });

  it("轮末 done 之后，下一轮正文仍能落行（终态冻结不再吞掉新消息）", () => {
    const snapshot = applyTuples([
      ["chunk", 1, text("第一轮", "turn-a")],
      ["done", 2, { status: "completed" }],
      ["chunk", 3, text("第二轮", "turn-b")],
    ]);

    expect(itemIds(snapshot)).toEqual(["assistant:turn-a", "assistant:turn-b"]);
    expect(snapshot.items[0].status).toBe("completed");
    // 关键回归：修复前该增量命中已冻结的全局 `assistant` 行，被终态拒绝而丢掉。
    expect(snapshot.items[1].status).toBe("running");
    const head = snapshot.items[1].head;
    expect(head.kind === "text" ? head.content : "").toBe("第二轮");
  });

  it("顺序契约：逐帧增量下既有行的下标永不变化（只追加，不重排）", () => {
    const tuples: Tuple[] = [
      ["chunk", 1, text("A", "turn-a")],
      ["tool_start", 2, { type: "tool_call", tool_call: { id: "call-1", name: "bash" } }],
      ["tool_end", 3, { type: "tool_call", tool_call: { id: "call-1", name: "bash" } }],
      ["chunk", 4, text("B", "turn-a")],
      ["done", 5, { status: "completed" }],
      ["chunk", 6, text("C", "turn-b")],
      ["observation", 7, { content: "obs" }],
      ["tool_start", 8, { type: "tool_call", tool_call: { id: "call-2", name: "view" } }],
    ];

    let snapshot = createEmptyTrajectory();
    let previous: string[] = [];
    for (const [kind, seq, payload] of tuples) {
      snapshot = applyEvent(snapshot, makeTrajectoryEvent(kind, seq, payload)).snapshot;
      const current = itemIds(snapshot);
      // 既有行必须是新顺序的前缀：既不消失，也不改位。
      expect(current.slice(0, previous.length)).toEqual(previous);
      previous = current;
    }

    expect(previous).toEqual([
      "assistant:turn-a",
      "tool:call-1",
      "assistant:turn-b",
      "observation-7",
      "tool:call-2",
    ]);
  });

  it("思考行同样按轮次成行", () => {
    const snapshot = applyTuples([
      ["chunk", 1, { type: "reasoning", content: "想一想", turn_id: "turn-a" }],
      ["chunk", 2, { type: "reasoning", content: "再想想", turn_id: "turn-b" }],
    ]);

    expect(itemIds(snapshot)).toEqual(["reasoning:turn-a", "reasoning:turn-b"]);
  });

  it("降级兼容：无 turn 标识用全局 id，显式身份帧按 completed 落行", () => {
    const snapshot = applyTuples([
      ["chunk", 1, { type: "text", content: "旧帧" }],
      ["chunk", 2, { type: "text", content: "历史兜底", [TRAJECTORY_ITEM_ID_KEY]: "history-1" }],
    ]);

    expect(itemIds(snapshot)).toEqual(["assistant", "history-1"]);
    expect(snapshot.items[0].status).toBe("running");
    expect(snapshot.items[1].status).toBe("completed");
  });
});
