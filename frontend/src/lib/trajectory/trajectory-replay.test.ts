/**
 * P1-6 replay 等价性测试
 *
 * - 确定性：同一事件序列两次构建快照深相等；
 * - 增量补齐：`after` 补齐事件（分段应用）与一次性全量应用结果一致；
 * - 幂等重放：同一批事件重放不改变快照；
 * - 乱序重放：乱序到达与有序到达最终快照一致（reducer 乱序缓冲保证）。
 */
import { describe, expect, it } from "vitest";

import {
  applyEvent,
  makeTrajectoryEvent,
} from "@/lib/trajectory/trajectory-reducer";
import { createEmptyTrajectory } from "@/lib/trajectory/types";

type EventTuple = [kind: "meta" | "chunk" | "reasoning" | "tool_start" | "tool_call" | "tool_end" | "result" | "done" | "planning", seq: number, payload: Record<string, unknown>];

/** 生成一段含工具调用的完整 turn 事件序列（seq 从 1 连续）。 */
function buildTurnEvents(): EventTuple[] {
  return [
    ["meta", 1, { session_id: "s-1", source: "llm_stream" }],
    ["planning", 2, { plan: ["read file", "run test"] }],
    ["reasoning", 3, { content: "thinking..." }],
    ["chunk", 4, { type: "text", content: "hello " }],
    ["chunk", 5, { type: "text", content: "world" }],
    ["tool_start", 6, { type: "tool_call", tool_call: { id: "call-1", name: "bash" } }],
    ["tool_call", 7, { type: "tool_call", tool_call: { id: "call-1", name: "bash" }, tool: { args_summary: "ls" } }],
    ["chunk", 8, { type: "text", content: " done" }],
    ["tool_end", 9, { type: "tool_call", tool_call: { id: "call-1", name: "bash" }, tool: { output_summary: "src" } }],
    ["result", 10, { success: true, output: "hello world done" }],
    ["done", 11, { status: "completed" }],
  ];
}

function applyTuples(tuples: EventTuple[]) {
  let snapshot = createEmptyTrajectory();
  for (const [kind, seq, payload] of tuples) {
    snapshot = applyEvent(snapshot, makeTrajectoryEvent(kind, seq, payload)).snapshot;
  }
  return snapshot;
}

describe("P1-6 replay 等价性", () => {
  it("确定性：同一序列两次构建快照深相等", () => {
    const events = buildTurnEvents();
    const first = applyTuples(events);
    const second = applyTuples(events);
    expect(first).toEqual(second);
    expect(first.items).toEqual(second.items);
  });

  it("增量补齐：分段应用（after 补齐）与全量应用结果一致", () => {
    const events = buildTurnEvents();
    const full = applyTuples(events);

    // 模拟 after=k 补齐：先应用 seq 1..7，再补齐 8..11
    const splitPoint = 7;
    let incremental = createEmptyTrajectory();
    for (const [kind, seq, payload] of events.slice(0, splitPoint)) {
      incremental = applyEvent(incremental, makeTrajectoryEvent(kind, seq, payload)).snapshot;
    }
    for (const [kind, seq, payload] of events.slice(splitPoint)) {
      incremental = applyEvent(incremental, makeTrajectoryEvent(kind, seq, payload)).snapshot;
    }
    expect(incremental).toEqual(full);
  });

  it("幂等重放：同一批事件重放不改变快照", () => {
    const events = buildTurnEvents();
    const once = applyTuples(events);
    let replayed = once;
    for (const [kind, seq, payload] of events) {
      replayed = applyEvent(replayed, makeTrajectoryEvent(kind, seq, payload)).snapshot;
    }
    expect(replayed).toEqual(once);
  });

  it("乱序重放：乱序到达与有序到达最终快照一致（乱序缓冲）", () => {
    const events = buildTurnEvents();
    const ordered = applyTuples(events);

    // 乱序：3,1,2,6,4,5,11,8,7,9,10
    const shuffled: EventTuple[] = [
      events[2], events[0], events[1],
      events[5], events[3], events[4],
      events[10], events[7], events[6],
      events[8], events[9],
    ];
    expect(shuffled.map(([, seq]) => seq)).toEqual([3, 1, 2, 6, 4, 5, 11, 8, 7, 9, 10]);

    const outOfOrder = applyTuples(shuffled);
    expect(outOfOrder).toEqual(ordered);
  });

  it("终态保护：done 后追加事件不改变 items（仅 lastEventSeq 水位前进）", () => {
    const events = buildTurnEvents();
    const snapshot = applyTuples(events);
    const next = applyEvent(
      snapshot,
      makeTrajectoryEvent("chunk", 12, { type: "text", content: "!" }),
    ).snapshot;
    expect(next.items).toEqual(snapshot.items);
    expect(next.lastEventSeq).toBe(12);
  });
});
