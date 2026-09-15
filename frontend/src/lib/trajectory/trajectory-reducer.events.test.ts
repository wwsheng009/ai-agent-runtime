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
  // P0-2（批次 20）：该分支此前被 recovery 的 kind 白名单挡成死代码，现在恢复
  // 链路放行未知 kind，于是它是「类型漂移」唯一可见的落点——行身份与文案必须钉住。
  it("未知 kind fallback 为 system Item（行身份 unknown-<seq>）", () => {
    const result = applyEvent(
      createEmptyTrajectory(),
      makeTrajectoryEvent("unknown" as never, 1, { note: "x" }),
    );
    expect(result.snapshot.items).toHaveLength(1);
    expect(result.snapshot.items[0].id).toBe("unknown-1");
    expect(result.snapshot.items[0].head).toEqual({
      kind: "system",
      note: "unknown event kind: unknown",
    });
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

  it("配额驱逐（agent.reclaimed）带路径/原因/来源", () => {
    expect(
      describeRuntimeEvent({
        runtime_type: "agent.reclaimed",
        source: "spawn_gate",
        reclaimed: 1,
        rows: 1,
        reasons: ["idle_timeout"],
        agent_paths: ["/root/held-child"],
        agent_path: "/root/held-child",
      }),
    ).toBe("agent reclaimed: /root/held-child (idle_timeout via spawn_gate)");

    // 多子节点只报数量；被截断时补 +N more；失败计数保留。
    expect(
      describeRuntimeEvent({
        runtime_type: "agent.reclaimed",
        source: "manual_cleanup",
        reclaimed: 3,
        rows: 4,
        failed: 1,
        truncated: 2,
        reasons: ["session_missing", "session_terminal"],
        agent_paths: ["/root/a", "/root/b", "/root/c"],
      }),
    ).toBe(
      "agent reclaimed: 3 agents +2 more (session_missing, session_terminal via manual_cleanup) 1 failed",
    );

    // 载荷退化（无 paths/reasons/source）时不产生空括号。
    expect(
      describeRuntimeEvent({ runtime_type: "agent.reclaimed", reclaimed: 1 }),
    ).toBe("agent reclaimed: 1 agent");
    expect(describeRuntimeEvent({ runtime_type: "agent.reclaimed" })).toBe(
      "agent reclaimed: agent",
    );
  });

  it("子会话进度镜像（subagent.progress）带路径/工具/状态/百分比/片段", () => {
    expect(
      describeRuntimeEvent({
        runtime_type: "subagent.progress",
        agent_id: "child-1",
        agent_path: "/root/child-1",
        state: "running",
        tool_name: "bash",
        partial: "compiling module 3/7",
        percent: 42,
      }),
    ).toBe("agent progress: /root/child-1 bash running 42% — compiling module 3/7");

    // 状态变化穿透（无 percent/partial）时只报最小事实。
    expect(
      describeRuntimeEvent({
        runtime_type: "subagent.progress",
        agent_id: "child-1",
        path: "/root/child-1",
        state: "completed",
        tool_name: "bash",
      }),
    ).toBe("agent progress: /root/child-1 bash completed");

    // 载荷退化：无 path 用子会话 ID，无 tool/state 时给默认值。
    expect(
      describeRuntimeEvent({ runtime_type: "subagent.progress", agent_id: "child-1" }),
    ).toBe("agent progress: child-1 running");
    expect(describeRuntimeEvent({ runtime_type: "subagent.progress" })).toBe(
      "agent progress: subagent running",
    );
  });
});

describe("runtime 事件（Q4）", () => {
  it("子会话进度镜像（seq=0）折叠为单一 runtime-0 行并就地更新", () => {
    const first = applyEvents(createEmptyTrajectory(), [
      makeTrajectoryEvent("runtime", 0, {
        runtime_type: "subagent.progress",
        agent_id: "child-1",
        session_id: "child-1",
        parent_session_id: "parent-1",
        agent_path: "/root/child-1",
        state: "running",
        tool_name: "bash",
        partial: "compiling module 3/7",
        live: true,
        _event: { sequence: 0 },
      }),
    ]);
    expect(first.snapshot.items).toHaveLength(1);
    expect(first.snapshot.items[0].id).toBe("runtime-0");
    expect(first.snapshot.items[0].kind).toBe("system");
    if (first.snapshot.items[0].head.kind === "system") {
      expect(first.snapshot.items[0].head.note).toBe(
        "agent progress: /root/child-1 bash running — compiling module 3/7",
      );
    }

    // 后续镜像复用同一 live 槽位：不新增行，只更新摘要（父轨迹只留最新一行）。
    const second = applyEvents(first.snapshot, [
      makeTrajectoryEvent("runtime", 0, {
        runtime_type: "subagent.progress",
        agent_id: "child-1",
        agent_path: "/root/child-1",
        state: "completed",
        tool_name: "bash",
        live: true,
        _event: { sequence: 0 },
      }),
    ]);
    expect(second.snapshot.items).toHaveLength(1);
    expect(second.snapshot.items[0].id).toBe("runtime-0");
    // live 行保持非终态：终态会被 upsertItem 冻结，后续镜像将被丢弃。
    expect(second.snapshot.items[0].status).toBe("running");
    if (second.snapshot.items[0].head.kind === "system") {
      expect(second.snapshot.items[0].head.note).toBe(
        "agent progress: /root/child-1 bash completed",
      );
    }
  });

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

  it("配额驱逐映射为 system 行（subagent 被自动回收可见）", () => {
    const result = applyEvents(createEmptyTrajectory(), [
      makeTrajectoryEvent("runtime", 1, {
        runtime_type: "agent.reclaimed",
        source: "spawn_gate",
        reclaimed: 1,
        reasons: ["idle_timeout"],
        agent_path: "/root/held-child",
        _event: { sequence: 1 },
      }),
    ]);
    expect(result.snapshot.items).toHaveLength(1);
    const item = result.snapshot.items[0];
    expect(item.id).toBe("runtime-1");
    expect(item.kind).toBe("system");
    expect(item.status).toBe("completed");
    if (item.head.kind === "system") {
      expect(item.head.note).toBe(
        "agent reclaimed: /root/held-child (idle_timeout via spawn_gate)",
      );
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
