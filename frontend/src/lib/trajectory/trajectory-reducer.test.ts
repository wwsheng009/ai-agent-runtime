import { describe, expect, it } from "vitest";

import {
  advanceSeqCursor,
  applyEvent,
  applyEvents,
  describeRuntimeEvent,
  eventSeqOf,
  makeTrajectoryEvent,
  removeItem,
} from "./trajectory-reducer";
import { createEmptyTrajectory } from "./types";

function chunk(
  seq: number,
  type: string,
  content: string,
  extra: Record<string, unknown> = {},
) {
  return makeTrajectoryEvent("chunk", seq, { type, content, ...extra });
}

function reasoning(seq: number, content: string) {
  return makeTrajectoryEvent("reasoning", seq, { content });
}

function toolEvent(
  kind: "tool_start" | "tool_call" | "tool_end",
  seq: number,
  toolCallId: string,
  name = "bash",
  extra: Record<string, unknown> = {},
) {
  return makeTrajectoryEvent(kind, seq, {
    type: "tool_call",
    tool_call: { id: toolCallId, name },
    ...extra,
  });
}

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
    let snapshot = createEmptyTrajectory();
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

describe("幂等（对齐 TestEncodeIdempotent / TestEncodeToolIdempotent）", () => {
  it("重复 seq 事件跳过；同内容 upsert 跳过", () => {
    let snapshot = createEmptyTrajectory();
    const first = applyEvent(snapshot, chunk(1, "text", "A"));
    snapshot = first.snapshot;
    expect(first.changes).toHaveLength(1);

    // 同 seq 重复事件 → 无变更。
    const duplicate = applyEvent(snapshot, chunk(1, "text", "A"));
    expect(duplicate.changes).toHaveLength(0);
    expect(duplicate.snapshot.revisions["assistant"]).toBe(1);

    // 结构化事件同内容重复 upsert → 无变更（幂等）。
    const planning = applyEvent(
      duplicate.snapshot,
      makeTrajectoryEvent("planning", 2, { step_count: 2 }),
    );
    const samePlanning = applyEvent(
      planning.snapshot,
      makeTrajectoryEvent("planning", 3, { step_count: 2 }),
    );
    expect(samePlanning.changes).toHaveLength(0);
    expect(samePlanning.snapshot.revisions["planning"]).toBe(1);
  });

  it("remove 不存在的 ID 忽略（幂等）", () => {
    const result = removeItem(createEmptyTrajectory(), "missing");
    expect(result.changes).toHaveLength(0);
    expect(result.snapshot.items).toHaveLength(0);
  });
});

describe("终态保护（对齐 TestEncodeTerminalStateFrozen）", () => {
  it("completed 后 upsert 被拒绝", () => {
    let snapshot = createEmptyTrajectory();
    snapshot = applyEvent(snapshot, toolEvent("tool_start", 1, "c-1")).snapshot;
    snapshot = applyEvent(
      snapshot,
      toolEvent("tool_end", 2, "c-1", "bash", { tool: { output_summary: "ok" } }),
    ).snapshot;
    const tool = snapshot.items.find((item) => item.id === "tool:c-1");
    expect(tool?.status).toBe("completed");

    // 终态后再 upsert → 拒绝。
    const late = applyEvent(
      snapshot,
      toolEvent("tool_end", 3, "c-1", "bash", { tool: { output_summary: "new" } }),
    );
    expect(late.changes).toHaveLength(0);
  });
});

describe("工具状态机（对齐 TestEncodeLegacyToolLifecycleUsesCallIdentity）", () => {
  it("tool_start → tool_call → tool_end 折叠为单 Item（started→running→finished）", () => {
    let snapshot = createEmptyTrajectory();
    const started = applyEvent(
      snapshot,
      toolEvent("tool_start", 1, "c-1", "bash", { tool: { args_summary: "ls" } }),
    );
    snapshot = started.snapshot;
    const startedItem = started.snapshot.items.find(
      (item) => item.id === "tool:c-1",
    );
    expect(startedItem?.head.kind).toBe("tool");
    if (startedItem?.head.kind === "tool") {
      expect(startedItem.head.phase).toBe("started");
    }

    snapshot = applyEvent(
      snapshot,
      toolEvent("tool_call", 2, "c-1"),
    ).snapshot;
    const runningItem = snapshot.items.find((item) => item.id === "tool:c-1");
    if (runningItem?.head.kind === "tool") {
      expect(runningItem.head.phase).toBe("running");
    }

    snapshot = applyEvent(
      snapshot,
      toolEvent("tool_end", 3, "c-1", "bash", { tool: { output_summary: "src" } }),
    ).snapshot;
    const items = snapshot.items.filter((item) => item.id === "tool:c-1");
    expect(items).toHaveLength(1); // 折叠为单 Item
    const done = items[0];
    expect(done?.status).toBe("completed");
    if (done?.head.kind === "tool") {
      expect(done.head.phase).toBe("finished");
      expect(done.head.argsSummary).toBe("ls");
      expect(done.head.resultSummary).toBe("src");
    }
  });

  it("tool_end 带错误 → failed/error（对齐 TestEncodeToolCallDisplayHeadRestoresLegacyDetails failed 分支）", () => {
    let snapshot = createEmptyTrajectory();
    snapshot = applyEvent(
      snapshot,
      toolEvent("tool_start", 1, "c-2", "bash"),
    ).snapshot;
    snapshot = applyEvent(
      snapshot,
      toolEvent("tool_end", 2, "c-2", "bash", {
        tool: { error: "command not found" },
      }),
    ).snapshot;
    const tool = snapshot.items.find((item) => item.id === "tool:c-2");
    expect(tool?.status).toBe("failed");
    if (tool?.head.kind === "tool") {
      expect(tool.head.phase).toBe("error");
      expect(tool.head.errorMessage).toBe("command not found");
    }
  });
});

describe("G7 事件映射（planning/orchestration/route/observation/subagent）", () => {
  it("planning/orchestration/route 折叠为单个 structured Item（同 kind upsert）", () => {
    let snapshot = createEmptyTrajectory();
    snapshot = applyEvent(
      snapshot,
      makeTrajectoryEvent("planning", 1, { step_count: 2 }),
    ).snapshot;
    snapshot = applyEvent(
      snapshot,
      makeTrajectoryEvent("planning", 2, { step_count: 3 }),
    ).snapshot;
    snapshot = applyEvent(
      snapshot,
      makeTrajectoryEvent("orchestration", 3, { source: "llm_stream" }),
    ).snapshot;
    snapshot = applyEvent(
      snapshot,
      makeTrajectoryEvent("route", 4, { route_attempted: true }),
    ).snapshot;

    expect(snapshot.items.filter((item) => item.id === "planning")).toHaveLength(1);
    const planning = snapshot.items.find((item) => item.id === "planning");
    expect(planning?.head.kind).toBe("structured");
    if (planning?.head.kind === "structured") {
      expect(planning.head.payload["step_count"]).toBe(3);
    }
    expect(
      snapshot.items.find((item) => item.id === "orchestration")?.status,
    ).toBe("running");
    expect(
      snapshot.items.find((item) => item.id === "route")?.status,
    ).toBe("running");
  });

  it("observation/subagent 每条独立 append（可折叠 Item，身份稳定）", () => {
    let snapshot = createEmptyTrajectory();
    snapshot = applyEvent(
      snapshot,
      makeTrajectoryEvent("observation", 1, { kind: "file_read" }),
    ).snapshot;
    snapshot = applyEvent(
      snapshot,
      makeTrajectoryEvent("observation", 2, { kind: "grep" }),
    ).snapshot;
    snapshot = applyEvent(
      snapshot,
      makeTrajectoryEvent("subagent", 3, { role: "researcher" }),
    ).snapshot;

    expect(
      snapshot.items.filter((item) => item.id === "observation-1"),
    ).toHaveLength(1);
    expect(
      snapshot.items.filter((item) => item.id === "observation-2"),
    ).toHaveLength(1);
    expect(
      snapshot.items.find((item) => item.id === "subagent-3")?.status,
    ).toBe("running");
  });
});

describe("error 事件（对齐 TestEncodeFailedDottedRequestPreservesPartialAndReadableError）", () => {
  it("冻结运行中的块为 failed，并追加可读 system note", () => {
    let snapshot = createEmptyTrajectory();
    snapshot = applyEvent(snapshot, chunk(1, "text", "partial output")).snapshot;
    snapshot = applyEvent(snapshot, toolEvent("tool_start", 2, "c-3")).snapshot;
    const result = applyEvent(
      snapshot,
      makeTrajectoryEvent("error", 3, { message: "upstream timeout" }),
    );
    snapshot = result.snapshot;

    const assistant = snapshot.items.find((item) => item.id === "assistant");
    const tool = snapshot.items.find((item) => item.id === "tool:c-3");
    expect(assistant?.status).toBe("failed");
    expect(tool?.status).toBe("failed");
    // 部分内容保留。
    if (assistant?.head.kind === "text") {
      expect(assistant.head.content).toBe("partial output");
    }
    const note = snapshot.items.find((item) => item.id === "error-3");
    expect(note?.head.kind).toBe("system");
    if (note?.head.kind === "system") {
      expect(note.head.note).toBe("upstream timeout");
    }
    expect(note?.status).toBe("failed");
  });
});

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
