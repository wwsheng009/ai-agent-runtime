// P0-2 随源拆分：由 trajectory-reducer.test.ts 按关注点切分，describe/it 与断言未改动。
import { describe, expect, it } from "vitest";

import {
  applyEvent,
  applyEvents,
  describeRuntimeEvent,
  eventSeqOf,
  makeTrajectoryEvent,
} from "./trajectory-reducer";
import { createEmptyTrajectory } from "./types";
import { chunk, reasoning, toolEvent } from "./trajectory-reducer.test-fixtures";

describe("未知事件与 seq 契约（对齐 TestEncodeUnknownEvent）", () => {
  it("未知 kind fallback 为 system Item", () => {
    const result = applyEvent(
      createEmptyTrajectory(),
      makeTrajectoryEvent("unknown" as never, 1, { note: "x" }),
    );
    expect(result.snapshot.items).toHaveLength(1);
    expect(result.snapshot.items[0].head.kind).toBe("system");
  });

  it("eventSeqOf 从 _event.sequence 提取 seq（P0-2 持久化 seq）", () => {
    expect(eventSeqOf({ _event: { sequence: 42 } })).toBe(42);
    expect(eventSeqOf({ _event: { sequence: "42" } })).toBe(42);
    expect(eventSeqOf({ content: "no envelope" })).toBe(0);
  });
});

describe("批量与重放（对齐 TestEncodeReplay / spec §6 去重合并）", () => {
  it("applyEvents 合并同一 Item 的多次变更（保留最新）", () => {
    const result = applyEvents(createEmptyTrajectory(), [
      chunk(1, "text", "A"),
      chunk(2, "text", "B"),
      chunk(3, "text", "C"),
    ]);
    // assistant 的三次变更合并为一次。
    const assistantChanges = result.changes.filter(
      (change) => change.itemId === "assistant",
    );
    expect(assistantChanges).toHaveLength(1);
    const item = assistantChanges[0].item;
    expect(item?.head.kind).toBe("text");
    if (item?.head.kind === "text") {
      expect(item.head.content).toBe("ABC");
    }
    expect(result.snapshot.lastEventSeq).toBe(3);
  });

  it("replay：同一事件序列两次构建快照深相等", () => {
    const events = [
      makeTrajectoryEvent("meta", 1, { session_id: "s-1" }),
      reasoning(2, "R1"),
      chunk(3, "text", "hello "),
      chunk(4, "text", "world"),
      toolEvent("tool_start", 5, "c-1", "bash", { tool: { args_summary: "ls" } }),
      toolEvent("tool_end", 6, "c-1", "bash", { tool: { output_summary: "src" } }),
      makeTrajectoryEvent("result", 7, { success: true }),
      makeTrajectoryEvent("done", 8, { status: "completed" }),
    ];

    let first = createEmptyTrajectory();
    let second = createEmptyTrajectory();
    for (const event of events) {
      first = applyEvent(first, event).snapshot;
      second = applyEvent(second, event).snapshot;
    }
    expect(second).toEqual(first);
    expect(second.items.map((item) => item.id)).toEqual(
      first.items.map((item) => item.id),
    );
  });
});

describe("describeRuntimeEvent（Q4）", () => {
  it("approval 事件带工具名", () => {
    expect(
      describeRuntimeEvent({ runtime_type: "approval_requested", tool_name: "shell" }),
    ).toBe("approval requested: shell");
    expect(
      describeRuntimeEvent({ runtime_type: "approval_resolved", tool_name: "shell", approved: false }),
    ).toBe("approval rejected: shell");
    expect(describeRuntimeEvent({ runtime_type: "approval_resolved", tool_name: "shell", allowed: false })).toBe(
      "approval rejected: shell",
    );
    expect(describeRuntimeEvent({ runtime_type: "approval_resolved", allowed: true })).toBe("approval approved");
  });

  it("compact 事件带 token 变化", () => {
    expect(
      describeRuntimeEvent({ runtime_type: "session_compact_completed", token_before: 371, token_after: 120 }),
    ).toBe("context compacted: 371 → 120 tokens");
    expect(
      describeRuntimeEvent({ runtime_type: "session_compact_skipped", reason: "below_limit" }),
    ).toBe("context compaction skipped: below_limit");
    expect(describeRuntimeEvent({ runtime_type: "session_compact_failed", error: "boom" })).toBe("context compaction failed: boom");
  });

  it("未知/缺失类型回退", () => {
    expect(describeRuntimeEvent({})).toBe("runtime event");
    expect(describeRuntimeEvent({ runtime_type: "job_output" })).toBe("job_output");
  });
});

describe("runtime 事件（Q4）", () => {
  it("映射为 system 行（note 可读摘要，status completed）", () => {
    const result = applyEvents(createEmptyTrajectory(), [
      makeTrajectoryEvent("runtime", 1, {
        runtime_type: "approval_requested",
        tool_name: "shell",
        _event: { sequence: 1 },
      }),
    ]);
    expect(result.snapshot.items).toHaveLength(1);
    const item = result.snapshot.items[0];
    expect(item.id).toBe("runtime-1");
    expect(item.kind).toBe("system");
    expect(item.status).toBe("completed");
    if (item.head.kind === "system") {
      expect(item.head.note).toBe("approval requested: shell");
    }
  });

  it("同 seq 重复 push 幂等（恢复 + 实时流重叠安全）", () => {
    const event = makeTrajectoryEvent("runtime", 4, {
      runtime_type: "session_compact_started",
      token_before: 500,
      _event: { sequence: 4 },
    });
    // seq=4 从空快照开始会乱序缓冲，先补 seq=1..3 的前序再推 runtime。
    const seeded = applyEvents(createEmptyTrajectory(), [
      makeTrajectoryEvent("meta", 1, { session_id: "s-1" }),
      makeTrajectoryEvent("chunk", 2, { type: "text", content: "a" }),
      makeTrajectoryEvent("chunk", 3, { type: "text", content: "b" }),
    ]);
    const first = applyEvents(seeded.snapshot, [event]);
    // seeded：meta 不建 item、两个 chunk 合并为一个 assistant item（1 项）→ +runtime = 2 项。
    expect(first.snapshot.items).toHaveLength(2);
    const second = applyEvents(first.snapshot, [event]);
    expect(second.snapshot.items).toHaveLength(2);
    expect(second.changes).toEqual([]);
  });
});
