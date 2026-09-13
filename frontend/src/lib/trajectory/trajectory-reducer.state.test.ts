// P0-2 随源拆分：由 trajectory-reducer.test.ts 按关注点切分，describe/it 与断言未改动。
import { describe, expect, it } from "vitest";

import {
  applyEvent,
  makeTrajectoryEvent,
  removeItem,
} from "./trajectory-reducer";
import { createEmptyTrajectory } from "./types";
import { chunk, toolEvent } from "./trajectory-reducer.test-fixtures";

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

  it("live tool.progress（seq=0，tool_call）折叠进既有工具行；终态后到达则被冻结忽略", () => {
    let snapshot = createEmptyTrajectory();
    snapshot = applyEvent(
      snapshot,
      toolEvent("tool_start", 1, "c-9", "bash"),
    ).snapshot;

    // 进行中进度（P1-5 子会话下钻：kind=tool_call、tool_call.id 同一 call）。
    snapshot = applyEvent(
      snapshot,
      toolEvent("tool_call", 0, "c-9", "bash", {
        tool: { output_summary: "partial output" },
      }),
    ).snapshot;
    const running = snapshot.items.find((item) => item.id === "tool:c-9");
    expect(snapshot.items).toHaveLength(1);
    if (running?.head.kind === "tool") {
      expect(running.head.phase).toBe("running");
      expect(running.head.resultSummary).toBe("partial output");
    }

    snapshot = applyEvent(
      snapshot,
      toolEvent("tool_end", 2, "c-9", "bash", {
        tool: { output_summary: "final output" },
      }),
    ).snapshot;

    // 迟到的进度事件（live 流与终态的竞态）：终态冻结 → 不覆盖已定稿的摘要，
    // 也不把 phase 打回 running（upsertItem 对终态 Item 拒绝 upsert）。
    snapshot = applyEvent(
      snapshot,
      toolEvent("tool_call", 0, "c-9", "bash", {
        tool: { output_summary: "late progress" },
      }),
    ).snapshot;
    const done = snapshot.items.find((item) => item.id === "tool:c-9");
    expect(snapshot.items).toHaveLength(1);
    expect(done?.status).toBe("completed");
    if (done?.head.kind === "tool") {
      expect(done.head.phase).toBe("finished");
      expect(done.head.resultSummary).toBe("final output");
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
