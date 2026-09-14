// P2-9 子片 3：goal 只读派生 + live 捕获 store 单测。
//
// 断言只依赖结构与后端原始字段：解析失败不猜、缺字段不补零、goal_missing 如实清空。

import { beforeEach, describe, expect, it } from "vitest";

import { type TrajectoryItem } from "@/lib/trajectory/types";

import {
  deriveSessionGoal,
  formatGoalTokens,
  parseGoalToolResult,
  sessionGoalFromTrajectoryItems,
} from "./derive";
import {
  getSessionGoal,
  recordGoalToolEnd,
  resetSessionGoalStore,
  subscribeSessionGoal,
} from "./store";

const activeGoal = {
  goal_id: "goal_1",
  session_id: "session-1",
  objective: "完成批次 11 目标指示",
  status: "active",
  token_budget: 20000,
  tokens_used: 1200,
  created_at: "2026-09-13T10:00:00+08:00",
  updated_at: "2026-09-13T10:30:00+08:00",
};

function trajectoryToolItem(partial: {
  name: string;
  resultSummary?: string;
  updatedAt: number;
  phase?: "started" | "running" | "finished" | "error";
}): TrajectoryItem {
  return {
    id: `tool-${partial.updatedAt}`,
    seq: partial.updatedAt,
    kind: "tool",
    causeId: "",
    status: "completed",
    head: {
      kind: "tool",
      name: partial.name,
      phase: partial.phase ?? "finished",
      resultSummary: partial.resultSummary,
    },
    createdAt: partial.updatedAt,
    updatedAt: partial.updatedAt,
  };
}

describe("parseGoalToolResult", () => {
  it("get_goal 结果 → 四相 active + token 字段 + remaining_tokens", () => {
    const observation = parseGoalToolResult(
      JSON.stringify({ goal: activeGoal, remaining_tokens: 18800 }),
    );
    expect(observation.kind).toBe("goal");
    if (observation.kind !== "goal") {
      return;
    }
    expect(observation.goal.phase).toBe("active");
    expect(observation.goal.objective).toBe("完成批次 11 目标指示");
    expect(observation.goal.tokenBudget).toBe(20000);
    expect(observation.goal.tokensUsed).toBe(1200);
    expect(observation.goal.remainingTokens).toBe(18800);
    expect(observation.goal.updatedAt).toBe("2026-09-13T10:30:00+08:00");
  });

  it("四相其余状态如实映射（paused / budget_limited / complete）", () => {
    for (const status of ["paused", "budget_limited", "complete"]) {
      const observation = parseGoalToolResult(
        JSON.stringify({
          goal: {
            ...activeGoal,
            status,
            completed_by: "session",
            completion_summary: "done",
            completed_at: "2026-09-13T11:00:00+08:00",
          },
        }),
      );
      expect(observation.kind).toBe("goal");
      if (observation.kind === "goal") {
        expect(observation.goal.phase).toBe(status);
      }
    }
  });

  it("未知状态不映射到四相，原始 status 保真透出", () => {
    const observation = parseGoalToolResult(
      JSON.stringify({ goal: { ...activeGoal, status: "quota_exhausted" } }),
    );
    expect(observation.kind).toBe("goal");
    if (observation.kind === "goal") {
      expect(observation.goal.phase).toBeNull();
      expect(observation.goal.statusRaw).toBe("quota_exhausted");
    }
  });

  it("goal 为 null / goal_missing 视为「此刻没有目标」（absent）", () => {
    expect(parseGoalToolResult(JSON.stringify({ goal: null })).kind).toBe("absent");
    expect(
      parseGoalToolResult(
        JSON.stringify({ updated: false, goal: null, reason: "goal_missing" }),
      ).kind,
    ).toBe("absent");
  });

  it("截断 / 非法 / 非 JSON 一律 unknown（不猜）", () => {
    expect(parseGoalToolResult("{").kind).toBe("unknown");
    expect(parseGoalToolResult('{"goal":{"goal_id":"goal_1","objective":"半').kind).toBe(
      "unknown",
    );
    expect(parseGoalToolResult("").kind).toBe("unknown");
    expect(parseGoalToolResult("[]").kind).toBe("unknown");
    expect(parseGoalToolResult(JSON.stringify({ updated: true })).kind).toBe("unknown");
  });

  it("缺失与非法 token 字段不补零；裸 goal 对象也可解析", () => {
    const bare = parseGoalToolResult(
      JSON.stringify({ goal_id: "goal_2", objective: "无预算目标", status: "active" }),
    );
    expect(bare.kind).toBe("goal");
    if (bare.kind === "goal") {
      expect(bare.goal.tokenBudget).toBeUndefined();
      expect(bare.goal.tokensUsed).toBeUndefined();
      expect(bare.goal.remainingTokens).toBeUndefined();
    }

    const stringy = parseGoalToolResult(
      JSON.stringify({
        goal: { ...activeGoal, token_budget: "20000", tokens_used: -5 },
      }),
    );
    expect(stringy.kind).toBe("goal");
    if (stringy.kind === "goal") {
      expect(stringy.goal.tokenBudget).toBeUndefined();
      expect(stringy.goal.tokensUsed).toBeUndefined();
    }
  });
});

describe("deriveSessionGoal", () => {
  it("unknown 不覆盖既有结论；absent 明确清空", () => {
    const goal = parseGoalToolResult(JSON.stringify({ goal: activeGoal }));
    expect(deriveSessionGoal([goal, { kind: "unknown" }])?.objective).toBe(
      "完成批次 11 目标指示",
    );
    expect(deriveSessionGoal([goal, { kind: "absent" }])).toBeNull();
    expect(deriveSessionGoal([{ kind: "absent" }, goal])?.phase).toBe("active");
  });
});

describe("sessionGoalFromTrajectoryItems（降级路径）", () => {
  it("按事件序取最后一次可解析结果", () => {
    const items = [
      trajectoryToolItem({
        name: "get_goal",
        resultSummary: JSON.stringify({ goal: activeGoal, remaining_tokens: 1 }),
        updatedAt: 5,
      }),
      trajectoryToolItem({
        name: "update_goal",
        resultSummary: JSON.stringify({
          updated: false,
          goal: null,
          reason: "goal_missing",
        }),
        updatedAt: 9,
      }),
    ];
    expect(sessionGoalFromTrajectoryItems(items)).toBeNull();
  });

  it("非 goal 工具与被截断的摘要都不产出目标", () => {
    expect(
      sessionGoalFromTrajectoryItems([
        trajectoryToolItem({ name: "bash", resultSummary: "{}", updatedAt: 1 }),
        trajectoryToolItem({
          name: "get_goal",
          resultSummary: '{"goal":{"goal_id":"goal_1","objective":"被截',
          updatedAt: 2,
        }),
        trajectoryToolItem({
          name: "get_goal",
          resultSummary: JSON.stringify({ goal: activeGoal }),
          updatedAt: 3,
          phase: "running",
        }),
      ]),
    ).toBeNull();
  });
});

describe("formatGoalTokens", () => {
  it("千 / 百万进位且不产生多余小数", () => {
    expect(formatGoalTokens(999)).toBe("999");
    expect(formatGoalTokens(1200)).toBe("1.2k");
    expect(formatGoalTokens(20000)).toBe("20k");
    expect(formatGoalTokens(123456)).toBe("123k");
    expect(formatGoalTokens(1_500_000)).toBe("1.5M");
    expect(formatGoalTokens(Number.NaN)).toBe("");
  });
});

describe("goal live 捕获 store", () => {
  beforeEach(() => {
    resetSessionGoalStore();
  });

  it("tool_end 的完整 tool.content 写入并按会话读取", () => {
    recordGoalToolEnd("session-1", {
      tool: {
        name: "get_goal",
        content: JSON.stringify({ goal: activeGoal, remaining_tokens: 18800 }),
      },
    });
    expect(getSessionGoal("session-1")?.phase).toBe("active");
    expect(getSessionGoal("session-2")).toBeNull();
    expect(getSessionGoal(undefined)).toBeNull();
  });

  it("非 goal 工具与无法判定的结果都不写入（不覆盖既有投影）", () => {
    recordGoalToolEnd("session-1", {
      tool: { name: "get_goal", content: JSON.stringify({ goal: activeGoal }) },
    });
    recordGoalToolEnd("session-1", { tool: { name: "bash", content: "{}" } });
    recordGoalToolEnd("session-1", {
      tool: { name: "get_goal", content: '{"goal":{"goal_id":"goal_1"' },
    });
    expect(getSessionGoal("session-1")?.phase).toBe("active");
  });

  it("goal_missing 清空该会话投影并通知订阅者", () => {
    let notified = 0;
    const unsubscribe = subscribeSessionGoal(() => {
      notified += 1;
    });
    recordGoalToolEnd("session-1", {
      tool: { name: "get_goal", content: JSON.stringify({ goal: activeGoal }) },
    });
    expect(notified).toBe(1);
    recordGoalToolEnd("session-1", {
      tool: {
        name: "update_goal",
        content: JSON.stringify({
          updated: false,
          goal: null,
          reason: "goal_missing",
        }),
      },
    });
    expect(getSessionGoal("session-1")).toBeNull();
    expect(notified).toBe(2);
    unsubscribe();
    recordGoalToolEnd("session-1", {
      tool: { name: "get_goal", content: JSON.stringify({ goal: activeGoal }) },
    });
    expect(notified).toBe(2);
  });

  it("投影引用稳定：同一会话重复读取返回同一对象", () => {
    recordGoalToolEnd("session-1", {
      tool: { name: "get_goal", content: JSON.stringify({ goal: activeGoal }) },
    });
    expect(getSessionGoal("session-1")).toBe(getSessionGoal("session-1"));
  });
});
