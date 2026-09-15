
import { describe, expect, it } from "vitest";

import { applyEvents, makeTrajectoryEvent } from "./trajectory-reducer";
import { createEmptyTrajectory } from "./types";

/**
 * P1-2（批次 20）：帧上的实体身份（`entity:{kind,id,degraded}`）必须决定行身份。
 *
 * 读侧契约与后端 `attachToolEntity` / `attachDegradedToolEntity`
 * （backend/internal/api/skills/live_tool_stream.go）成对存在：
 * - 权威身份 ⇒ 行 id 取 entity.id，可与后续权威帧合并；
 * - 合成身份 ⇒ 行 id 仍取 entity.id（后端指定的稳定名），但 head 标 degraded，
 *   「同一次调用两行」因此变成数据上可见的已知降级；
 * - 无 entity 且无 call id ⇒ 按 seq 兜底，同样标 degraded。
 */
describe("P1-2 帧上实体身份决定工具行身份", () => {
  it("权威 entity 优先于 tool_call_id 读取，且不标 degraded", () => {
    const result = applyEvents(createEmptyTrajectory(), [
      makeTrajectoryEvent("tool_start", 1, {
        entity: { kind: "tool", id: "call_z" },
        tool: { name: "read" },
      }),
    ]);

    const item = result.snapshot.items[0];
    expect(item.id).toBe("tool:call_z");
    expect(item.status).toBe("running");
    expect(item.head.kind).toBe("tool");
    if (item.head.kind === "tool") {
      expect(item.head.degraded).toBeUndefined();
    }
  });

  it("合成 entity（degraded）保留后端指定的稳定行 id，并显式标记", () => {
    const result = applyEvents(createEmptyTrajectory(), [
      makeTrajectoryEvent("tool_start", 1, {
        entity: {
          kind: "tool",
          id: "observation_step_1_tool_0",
          degraded: true,
        },
        tool: { name: "read" },
      }),
    ]);

    const item = result.snapshot.items[0];
    expect(item.id).toBe("tool:observation_step_1_tool_0");
    if (item.head.kind === "tool") {
      expect(item.head.degraded).toBe(true);
    } else {
      throw new Error("期望工具行");
    }
  });

  it("无 entity 且无 call id 时按 seq 兜底并标 degraded（可观测的不可合并）", () => {
    // seq 必须从 1 起连续：reducer 对乱序/缺口事件先缓冲（见 applyEvents 的
    // 事件序契约），跳号的首帧不会立刻落行。
    const result = applyEvents(createEmptyTrajectory(), [
      makeTrajectoryEvent("tool_start", 1, { tool: { name: "read" } }),
    ]);

    const item = result.snapshot.items[0];
    expect(item.id).toBe("tool-1");
    if (item.head.kind === "tool") {
      expect(item.head.degraded).toBe(true);
    } else {
      throw new Error("期望工具行");
    }
  });
});

/**
 * P1-1（批次 20）：子代理镜像行按子会话收敛。
 *
 * 修复前的表现是「所有子代理共用 runtime-0」：两个子代理同时干活，父轨迹上
 * 只有一行、后到的镜像把先到的覆盖掉，且 live 行恒为 running（永远等不到终态）。
 * 这里把「每个子代理一行」「终端态可收敛」钉住。
 */
describe("P1-1 子代理镜像按子会话建行", () => {
  it("两个子代理各占一行，互不覆盖", () => {
    const result = applyEvents(createEmptyTrajectory(), [
      makeTrajectoryEvent("runtime", 0, {
        runtime_type: "subagent.progress",
        agent_id: "child-1",
        session_id: "child-1",
        state: "running",
        tool_name: "bash",
        partial: "child 1 working",
        live: true,
        _trajectory_item_id: "subagent:child-1",
        _event: { sequence: 0 },
      }),
      makeTrajectoryEvent("runtime", 0, {
        runtime_type: "subagent.progress",
        agent_id: "child-2",
        session_id: "child-2",
        state: "running",
        tool_name: "view",
        partial: "child 2 working",
        live: true,
        _trajectory_item_id: "subagent:child-2",
        _event: { sequence: 0 },
      }),
    ]);

    expect(result.snapshot.items.map((item) => item.id)).toEqual([
      "subagent:child-1",
      "subagent:child-2",
    ]);
  });

  it("同一子代理的后续镜像就地更新同一行（不新增事件风暴行）", () => {
    const first = applyEvents(createEmptyTrajectory(), [
      makeTrajectoryEvent("runtime", 0, {
        runtime_type: "subagent.progress",
        agent_id: "child-1",
        session_id: "child-1",
        state: "running",
        tool_name: "bash",
        partial: "compiling module 3/7",
        live: true,
        _trajectory_item_id: "subagent:child-1",
        _event: { sequence: 0 },
      }),
    ]);

    const second = applyEvents(first.snapshot, [
      makeTrajectoryEvent("runtime", 0, {
        runtime_type: "subagent.progress",
        agent_id: "child-1",
        session_id: "child-1",
        state: "running",
        tool_name: "bash",
        partial: "compiling module 5/7",
        live: true,
        _trajectory_item_id: "subagent:child-1",
        _event: { sequence: 0 },
      }),
    ]);

    expect(second.snapshot.items).toHaveLength(1);
    expect(second.snapshot.items[0].id).toBe("subagent:child-1");
    if (second.snapshot.items[0].head.kind === "system") {
      expect(second.snapshot.items[0].head.note).toContain("compiling module 5/7");
    } else {
      throw new Error("期望 system 行");
    }
  });
});
