// P0-2 随源拆分：由 trajectory-reducer.test.ts 按关注点切分，describe/it 与断言未改动。
import { describe, expect, it } from "vitest";

import {
  advanceSeqCursor,
  applyEvent,
  makeTrajectoryEvent,
} from "./trajectory-reducer";
import { createEmptyTrajectory } from "./types";
import { chunk, reasoning, toolEvent } from "./trajectory-reducer.test-fixtures";

describe("trajectory reducer 基本序列（对齐 TestEncodeBasicSequence）", () => {
  it("meta → reasoning → chunk(text) → tool → result → done 生成有序 Items", () => {
    let snapshot = createEmptyTrajectory();
    const events = [
      makeTrajectoryEvent("meta", 1, { session_id: "s-1", source: "llm_stream" }),
      reasoning(2, "thinking..."),
      chunk(3, "text", "hello "),
      chunk(4, "text", "world"),
      toolEvent("tool_start", 5, "call-1"),
      toolEvent("tool_call", 6, "call-1", "bash", { tool: { args_summary: "ls" } }),
      toolEvent("tool_end", 7, "call-1", "bash", { tool: { output_summary: "src" } }),
      makeTrajectoryEvent("result", 8, { success: true, output: "hello world" }),
      makeTrajectoryEvent("done", 9, { status: "completed" }),
    ];
    for (const event of events) {
      snapshot = applyEvent(snapshot, event).snapshot;
    }

    const byId = Object.fromEntries(snapshot.items.map((item) => [item.id, item]));
    expect(byId["assistant"]?.head.kind).toBe("text");
    if (byId["assistant"]?.head.kind === "text") {
      expect(byId["assistant"].head.content).toBe("hello world");
    }
    expect(byId["assistant"]?.status).toBe("completed"); // done 收尾
    expect(byId["reasoning"]?.head.kind).toBe("reasoning");
    if (byId["reasoning"]?.head.kind === "reasoning") {
      expect(byId["reasoning"].head.content).toBe("thinking...");
    }
    expect(byId["reasoning"]?.status).toBe("completed"); // done 收尾

    const tool = byId["tool:call-1"];
    expect(tool?.head.kind).toBe("tool");
    if (tool?.head.kind === "tool") {
      expect(tool.head.phase).toBe("finished");
      expect(tool.head.name).toBe("bash");
      expect(tool.head.resultSummary).toBe("src");
    }
    expect(tool?.status).toBe("completed");
    expect(byId["result"]?.status).toBe("completed");
    expect(snapshot.lastEventSeq).toBe(9);
    expect(snapshot.pending).toEqual({});
  });

  it("done 收尾仍在运行的块（孤儿 final 直接终态，对齐 TestFinalizeOpenStreams）", () => {
    let snapshot = createEmptyTrajectory();
    snapshot = applyEvent(snapshot, chunk(1, "text", "partial")).snapshot;
    snapshot = applyEvent(snapshot, toolEvent("tool_start", 2, "call-9")).snapshot;
    snapshot = applyEvent(
      snapshot,
      makeTrajectoryEvent("done", 3, { status: "completed" }),
    ).snapshot;

    const assistant = snapshot.items.find((item) => item.id === "assistant");
    const tool = snapshot.items.find((item) => item.id === "tool:call-9");
    expect(assistant?.status).toBe("completed");
    expect(tool?.status).toBe("completed");
  });
});

describe("乱序缓冲（对齐 TestEncodeOutOfOrder：1,3,2 → ABC）", () => {
  it("乱序事件按 seq 缓冲并按序应用", () => {
    const snapshot = createEmptyTrajectory();
    const first = applyEvent(snapshot, chunk(1, "text", "A"));
    expect(first.snapshot.lastEventSeq).toBe(1);

    // seq=3 乱序到达：缓冲，不应用。
    const buffered = applyEvent(first.snapshot, chunk(3, "text", "C"));
    expect(buffered.snapshot.lastEventSeq).toBe(1);
    expect(buffered.snapshot.pending[3]).toBeDefined();
    const bufferedAssistant = buffered.snapshot.items.find(
      (item) => item.id === "assistant",
    );
    expect(bufferedAssistant?.head.kind).toBe("text");
    if (bufferedAssistant?.head.kind === "text") {
      expect(bufferedAssistant.head.content).toBe("A"); // 未被乱序事件 C 污染
    }

    // seq=2 到达：应用 2，随后自动消费缓冲的 3 → 拼接 ABC。
    const resolved = applyEvent(buffered.snapshot, chunk(2, "text", "B"));
    expect(resolved.snapshot.lastEventSeq).toBe(3);
    expect(resolved.snapshot.pending).toEqual({});
    const assistant = resolved.snapshot.items.find(
      (item) => item.id === "assistant",
    );
    expect(assistant?.head.kind).toBe("text");
    if (assistant?.head.kind === "text") {
      expect(assistant.head.content).toBe("ABC");
    }
  });

  it("乱序 reasoning 与 assistant 互不覆盖（对齐 TestEncodeReasoningIndependentOfAssistant）", () => {
    let snapshot = createEmptyTrajectory();
    // 乱序：reasoning(3) 先到（缓冲），text(1)、text(2) 后到补齐。
    const first = applyEvent(snapshot, reasoning(3, "R"));
    expect(first.snapshot.pending[3]).toBeDefined();
    snapshot = applyEvent(first.snapshot, chunk(1, "text", "T")).snapshot;
    expect(snapshot.lastEventSeq).toBe(1);
    expect(snapshot.pending[3]).toBeDefined();
    const resolved = applyEvent(snapshot, chunk(2, "text", "T2"));
    expect(resolved.snapshot.lastEventSeq).toBe(3);
    expect(resolved.snapshot.pending).toEqual({});
    const assistant = resolved.snapshot.items.find(
      (item) => item.id === "assistant",
    );
    const rItem = resolved.snapshot.items.find(
      (item) => item.id === "reasoning",
    );
    expect(assistant?.head.kind).toBe("text");
    expect(rItem?.head.kind).toBe("reasoning");
  });
});

describe("advanceSeqCursor：跳过被过滤事件留下的 seq 空洞", () => {
  it("顺序链被 tool_started 空洞截断后，跳过空洞可续接后续事件（回归：只有 system 行的问题）", () => {
    let snapshot = createEmptyTrajectory();
    // 恢复路径按列表推进：chat.sse 事件 + 被过滤的 tool_started(3)。
    snapshot = applyEvent(snapshot, chunk(1, "text", "A")).snapshot;
    snapshot = applyEvent(snapshot, chunk(2, "text", "B")).snapshot;
    // 空洞 seq=3（被过滤）→ 后续事件 4 若无空洞处理将永久 pending。
    const buffered = applyEvent(snapshot, chunk(4, "text", "D"));
    expect(buffered.snapshot.lastEventSeq).toBe(2);
    expect(buffered.snapshot.pending[4]).toBeDefined();
    // 恢复链路对被过滤事件调用 advanceSeqCursor(3) → 续接 pending 的 4。
    const bridged = advanceSeqCursor(buffered.snapshot, 3);
    expect(bridged.snapshot.lastEventSeq).toBe(4);
    expect(bridged.snapshot.pending).toEqual({});
    const assistant = bridged.snapshot.items.find(
      (item) => item.id === "assistant",
    );
    expect(assistant?.head.kind).toBe("text");
    if (assistant?.head.kind === "text") {
      expect(assistant.head.content).toBe("ABD");
    }
  });

  it("逐个跳过连续空洞后继续消费后续 pending（不丢缓冲的真实事件）", () => {
    let snapshot = createEmptyTrajectory();
    snapshot = applyEvent(snapshot, chunk(1, "text", "A")).snapshot;
    // 实时流已把 5、6 缓冲（2/3/4 是空洞：context.profile.injected 等）。
    snapshot = applyEvent(snapshot, chunk(5, "text", "E")).snapshot;
    snapshot = applyEvent(snapshot, chunk(6, "text", "F")).snapshot;
    // 轮询/恢复逐个推进空洞：2、3 后 5 仍等待 4；推进 4 后消费 5、6。
    snapshot = advanceSeqCursor(snapshot, 2).snapshot;
    expect(snapshot.pending[5]).toBeDefined();
    snapshot = advanceSeqCursor(snapshot, 3).snapshot;
    expect(snapshot.pending[5]).toBeDefined();
    snapshot = advanceSeqCursor(snapshot, 4).snapshot;
    expect(snapshot.lastEventSeq).toBe(6);
    expect(snapshot.pending).toEqual({});
    const assistant = snapshot.items.find((item) => item.id === "assistant");
    if (assistant?.head.kind === "text") {
      expect(assistant.head.content).toBe("AEF");
    }
  });

  it("目标不超过已应用游标时无副作用", () => {
    let snapshot = createEmptyTrajectory();
    snapshot = applyEvent(snapshot, chunk(1, "text", "A")).snapshot;
    const result = advanceSeqCursor(snapshot, 1);
    expect(result.changes).toHaveLength(0);
    expect(result.snapshot.lastEventSeq).toBe(1);
  });
});
