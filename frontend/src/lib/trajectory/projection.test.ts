import { describe, expect, it, vi } from "vitest";

import {
  applyEvent,
  makeTrajectoryEvent,
} from "@/lib/trajectory/trajectory-reducer";
import { createEmptyTrajectory } from "@/lib/trajectory/types";
import {
  compareTrajectoryVsSegments,
  debugTrajectoryConsistency,
  summarizeMessageSegments,
  summarizeTrajectorySnapshot,
  trajectoryItemsToMessageSegments,
} from "./projection";

/** 事件流构造快照（与 reducer 测试同模式）。 */
function snapshotFrom(
  events: Array<{
    kind: Parameters<typeof makeTrajectoryEvent>[0];
    seq: number;
    payload: Record<string, unknown>;
  }>,
) {
  let snapshot = createEmptyTrajectory();
  for (const event of events) {
    snapshot = applyEvent(snapshot, makeTrajectoryEvent(event.kind, event.seq, event.payload)).snapshot;
  }
  return snapshot;
}

function fullTurnSnapshot() {
  return snapshotFrom([
    { kind: "meta", seq: 1, payload: { session_id: "s-1", source: "llm_stream" } },
    { kind: "reasoning", seq: 2, payload: { content: "thinking..." } },
    { kind: "chunk", seq: 3, payload: { type: "text", content: "hello " } },
    { kind: "chunk", seq: 4, payload: { type: "text", content: "world" } },
    {
      kind: "tool_start",
      seq: 5,
      payload: { type: "tool_call", tool_call: { id: "call-1", name: "bash" } },
    },
    {
      kind: "tool_call",
      seq: 6,
      payload: {
        type: "tool_call",
        tool_call: { id: "call-1", name: "bash" },
        tool: { args_summary: "ls" },
      },
    },
    {
      kind: "tool_end",
      seq: 7,
      payload: {
        type: "tool_call",
        tool_call: { id: "call-1", name: "bash" },
        tool: { output_summary: "src" },
      },
    },
    { kind: "result", seq: 8, payload: { success: true, output: "hello world" } },
    { kind: "done", seq: 9, payload: { status: "completed" } },
  ]);
}

describe("trajectoryItemsToMessageSegments（chat 投影）", () => {
  it("空轨迹 → 空 segments", () => {
    expect(trajectoryItemsToMessageSegments([])).toEqual([]);
  });

  it("text/reasoning/tool 映射为 MessageSegment（message-list 零改动）", () => {
    const segments = trajectoryItemsToMessageSegments(fullTurnSnapshot().items);
    expect(segments).toEqual([
      { type: "reasoning", content: "thinking...", running: false },
      { type: "text", content: "hello world" },
      {
        type: "tool",
        toolCallId: "call-1",
        name: "bash",
        status: "finished",
        argsSummary: "ls",
        resultSummary: "src",
        errorMessage: undefined,
      },
    ]);
  });

  it("G7 structured 事件不渲染（Phase 2 轨迹视图消费）", () => {
    const snapshot = snapshotFrom([
      { kind: "planning", seq: 1, payload: { plan: ["a"] } },
      { kind: "orchestration", seq: 2, payload: { steps: 2 } },
      { kind: "route", seq: 3, payload: { route: "reasoning" } },
      { kind: "observation", seq: 4, payload: { observation: "o" } },
      { kind: "subagent", seq: 5, payload: { subagent_id: "a-1" } },
    ]);
    expect(trajectoryItemsToMessageSegments(snapshot.items)).toEqual([]);
  });

  it("reasoning running 状态透传（流未结束为 running，done 后 completed）", () => {
    const snapshot = snapshotFrom([
      { kind: "reasoning", seq: 1, payload: { content: "thinking" } },
    ]);
    const segments = trajectoryItemsToMessageSegments(snapshot.items);
    expect(segments[0]).toMatchObject({ type: "reasoning", running: true });

    const done = snapshotFrom([
      { kind: "reasoning", seq: 1, payload: { content: "thinking" } },
      { kind: "done", seq: 2, payload: { status: "completed" } },
    ]);
    const segmentsAfterDone = trajectoryItemsToMessageSegments(done.items);
    expect(segmentsAfterDone[0]).toMatchObject({
      type: "reasoning",
      running: false,
    });
  });
});

describe("摘要与双跑校验", () => {
  it("summarizeTrajectorySnapshot 提取 text/reasoning/tools", () => {
    const summary = summarizeTrajectorySnapshot(fullTurnSnapshot());
    expect(summary).toEqual({
      text: "hello world",
      reasoning: "thinking...",
      tools: [{ name: "bash", phase: "finished" }],
    });
  });

  it("投影后再摘要 == 轨迹摘要（映射器无信息丢失）", () => {
    const snapshot = fullTurnSnapshot();
    const projected = summarizeMessageSegments(
      trajectoryItemsToMessageSegments(snapshot.items),
    );
    expect(projected).toEqual(summarizeTrajectorySnapshot(snapshot));
  });

  it("一致时 compareTrajectoryVsSegments 返回空数组", () => {
    const snapshot = fullTurnSnapshot();
    const segments = trajectoryItemsToMessageSegments(snapshot.items);
    expect(compareTrajectoryVsSegments(snapshot, segments)).toEqual([]);
  });

  it("text 不一致时报告差异", () => {
    const snapshot = fullTurnSnapshot();
    const segments = trajectoryItemsToMessageSegments(snapshot.items);
    const differences = compareTrajectoryVsSegments(snapshot, [
      ...segments,
      { type: "text", content: " extra" },
    ]);
    expect(differences.length).toBe(1);
    expect(differences[0]).toContain("text mismatch");
  });

  it("工具序列不一致时报告差异", () => {
    const snapshot = fullTurnSnapshot();
    const segments = trajectoryItemsToMessageSegments(snapshot.items);
    const differences = compareTrajectoryVsSegments(snapshot, [
      ...segments.slice(0, -1),
      {
        type: "tool",
        toolCallId: "call-1",
        name: "bash",
        status: "error",
        argsSummary: "ls",
        resultSummary: "boom",
        errorMessage: "failed",
      },
    ]);
    expect(differences.length).toBe(1);
    expect(differences[0]).toContain("tools mismatch");
  });

  it("debugTrajectoryConsistency 一致时不告警", () => {
    const snapshot = fullTurnSnapshot();
    const segments = trajectoryItemsToMessageSegments(snapshot.items);
    const spy = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    debugTrajectoryConsistency(snapshot, segments);
    expect(spy).not.toHaveBeenCalled();
    spy.mockRestore();
  });
});
