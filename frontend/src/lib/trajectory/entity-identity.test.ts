import { describe, expect, it } from "vitest";

import {
  entityItemId,
  readTrajectoryEntity,
  subagentChildIdOf,
  subagentItemId,
  subagentRowIdOf,
  subagentStatusOf,
  toolItemId,
  TRAJECTORY_ENTITY_KEY,
} from "./entity-identity";

describe("P1-2 实体身份：item id 的唯一拼装点", () => {
  it("kind:id 拼装（与 reducer 既有 tool:<call_id> 兼容）", () => {
    expect(entityItemId("tool", "call_a")).toBe("tool:call_a");
    expect(toolItemId("call_a")).toBe("tool:call_a");
    expect(subagentItemId("child-1")).toBe("subagent:child-1");
  });
});

describe("P1-2 readTrajectoryEntity：随帧下发的权威实体对", () => {
  it("完整对（kind + id）按可合并处理，degraded 缺省为未设置", () => {
    const entity = readTrajectoryEntity({
      [TRAJECTORY_ENTITY_KEY]: { kind: "tool", id: "call_a" },
    });
    expect(entity).toEqual({ kind: "tool", id: "call_a" });
  });

  it("degraded=true 原样透传（后端已声明是兜底身份）", () => {
    const entity = readTrajectoryEntity({
      [TRAJECTORY_ENTITY_KEY]: { kind: "tool", id: "tool-7", degraded: true },
    });
    expect(entity).toEqual({ kind: "tool", id: "tool-7", degraded: true });
  });

  it("非法输入一律 null（不猜身份）：缺对象 / 未知 kind / 空 id", () => {
    expect(readTrajectoryEntity(undefined)).toBeNull();
    expect(readTrajectoryEntity({})).toBeNull();
    expect(readTrajectoryEntity({ entity: "tool:call_a" })).toBeNull();
    expect(
      readTrajectoryEntity({ entity: { kind: "message", id: "m-1" } }),
    ).toBeNull();
    expect(
      readTrajectoryEntity({ entity: { kind: "tool", id: "   " } }),
    ).toBeNull();
  });
});

describe("P1-1 子代理行身份与状态", () => {
  it("子会话 id 取值优先级：session_id → agent_id，均缺则空", () => {
    expect(subagentChildIdOf({ session_id: " child-1 " })).toBe("child-1");
    expect(subagentChildIdOf({ agent_id: "child-2" })).toBe("child-2");
    expect(subagentChildIdOf({ role: "researcher" })).toBe("");
  });

  it("有子会话 id ⇒ 每个子代理一行；缺 id 才退回 seq 兜底", () => {
    expect(
      subagentRowIdOf({ session_id: "child-1", agent_id: "child-1" }, 0),
    ).toBe("subagent:child-1");
    expect(subagentRowIdOf({ session_id: "child-2" }, 0)).toBe(
      "subagent:child-2",
    );
    expect(subagentRowIdOf({ role: "researcher" }, 9)).toBe("subagent-9");
  });

  it("状态不再恒 running：success 是权威终态，缺席才保持 running", () => {
    expect(subagentStatusOf({ success: true })).toBe("completed");
    expect(subagentStatusOf({ success: false })).toBe("failed");
    // live 镜像没有 success：保持非终态，否则后续镜像会被冻结丢弃。
    expect(subagentStatusOf({ state: "progress" })).toBe("running");
    // 非布尔值不是终态信号（宁可不收尾，也不猜）。
    expect(subagentStatusOf({ success: "true" })).toBe("running");
  });
});
