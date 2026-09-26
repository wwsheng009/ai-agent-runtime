// P2-1A：子代理面板纯派生函数单测。

import { describe, expect, it } from "vitest";

import {
  agentDisplayName,
  agentDisplayStatus,
  agentDurationSpan,
  agentMetaFacts,
  agentPathSegments,
  agentReadOnlyReason,
  agentStatusLabelKey,
  agentStatusToneClass,
  agentTranscriptTarget,
  buildAgentForest,
  canResumeAgent,
  canStopAgent,
  flattenAgentTree,
  formatAgentDuration,
  formatAgentDurationExact,
  isAgentRunning,
  projectParkedTurnTaskCounts,
  splitSessionAgents,
} from "./session-agents-panel-shared";

import type { RuntimeAgentRecord } from "@/types/runtime";

function agent(partial: Partial<RuntimeAgentRecord> & { agentId: string }): RuntimeAgentRecord {
  return {
    rootSessionId: "sess-root",
    parentAgentId: null,
    parentSessionId: null,
    sessionId: null,
    agentPath: null,
    depth: null,
    agentType: null,
    nickname: null,
    workflow: null,
    teamId: null,
    teammateId: null,
    provider: null,
    model: null,
    difficulty: null,
    status: "active",
    runtimeState: "unknown",
    createdAt: null,
    updatedAt: null,
    closedAt: null,
    routeWarnings: [],
    ...partial,
  };
}

describe("agentDisplayName", () => {
  it("昵称 → 类型 → agent_id 依次回退", () => {
    expect(agentDisplayName(agent({ agentId: "a1", nickname: "researcher" }))).toBe(
      "researcher",
    );
    expect(agentDisplayName(agent({ agentId: "a1", agentType: "child" }))).toBe("child");
    expect(agentDisplayName(agent({ agentId: "a1" }))).toBe("a1");
  });
});

describe("动作可用性（未知状态不给动作）", () => {
  it("active / stale 可停止", () => {
    expect(canStopAgent("active")).toBe(true);
    expect(canStopAgent("stale")).toBe(true);
    expect(canStopAgent("closed")).toBe(false);
    expect(canStopAgent("unknown")).toBe(false);
  });

  it("仅 closed 可恢复", () => {
    expect(canResumeAgent("closed")).toBe(true);
    expect(canResumeAgent("active")).toBe(false);
    expect(canResumeAgent("stale")).toBe(false);
    expect(canResumeAgent("unknown")).toBe(false);
  });

  it("running 划分：active / stale 属运行中", () => {
    expect(isAgentRunning("active")).toBe(true);
    expect(isAgentRunning("stale")).toBe(true);
    expect(isAgentRunning("waiting_approval")).toBe(true);
    expect(isAgentRunning("waiting_input")).toBe(true);
    expect(isAgentRunning("closed")).toBe(false);
    expect(isAgentRunning("ended")).toBe(false);
    expect(isAgentRunning("unknown")).toBe(false);
  });

  it("状态文案键与状态一一对应", () => {
    expect(agentStatusLabelKey("active")).toBe("panels.agents.status.active");
    expect(agentStatusLabelKey("waiting_approval")).toBe(
      "panels.agents.status.waiting_approval",
    );
    expect(agentStatusLabelKey("waiting_input")).toBe("panels.agents.status.waiting_input");
    expect(agentStatusLabelKey("unknown")).toBe("panels.agents.status.unknown");
  });

  it("等待态用警示色（与 stale 同档）", () => {
    expect(agentStatusToneClass("waiting_approval")).toBe("text-analytics-warning");
    expect(agentStatusToneClass("waiting_input")).toBe("text-analytics-warning");
  });
});

describe("agentDisplayStatus（身份状态 + 运行态 → 展示状态）", () => {
  it("子代理身份 active 但容器 idle/stopped → ended", () => {
    expect(
      agentDisplayStatus(agent({ agentId: "c1", agentType: "child", runtimeState: "idle" })),
    ).toBe("ended");
    expect(
      agentDisplayStatus(agent({ agentId: "c2", agentType: "child", runtimeState: "stopped" })),
    ).toBe("ended");
  });

  it("容器在跑 / 运行态未知 → 保留身份 active（读不到 ≠ 已结束）", () => {
    expect(
      agentDisplayStatus(agent({ agentId: "c1", agentType: "child", runtimeState: "running" })),
    ).toBe("active");
    expect(
      agentDisplayStatus(agent({ agentId: "c2", agentType: "child", runtimeState: "unknown" })),
    ).toBe("active");
  });

  it("等待审批 / 等待输入原样透出，不收敛成 running / ended", () => {
    expect(
      agentDisplayStatus(
        agent({ agentId: "c1", agentType: "child", runtimeState: "waiting_approval" }),
      ),
    ).toBe("waiting_approval");
    expect(
      agentDisplayStatus(
        agent({ agentId: "c2", agentType: "child", runtimeState: "waiting_input" }),
      ),
    ).toBe("waiting_input");
  });

  it("身份终态优先透传，不被运行态覆盖", () => {
    expect(
      agentDisplayStatus(agent({ agentId: "c1", agentType: "child", status: "closed", runtimeState: "idle" })),
    ).toBe("closed");
    expect(
      agentDisplayStatus(agent({ agentId: "c2", agentType: "child", status: "stale", runtimeState: "idle" })),
    ).toBe("stale");
    expect(
      agentDisplayStatus(agent({ agentId: "c3", agentType: "child", status: "unknown", runtimeState: "idle" })),
    ).toBe("unknown");
  });

  it("根行按会话容器看待：轮次之间的 idle 不算结束", () => {
    expect(
      agentDisplayStatus(agent({ agentId: "root", agentType: "root", runtimeState: "idle" })),
    ).toBe("active");
  });

  it("等待态透出只针对子代理行；根行仍回退身份状态（不臆断）", () => {
    expect(
      agentDisplayStatus(
        agent({ agentId: "root", agentType: "root", runtimeState: "waiting_approval" }),
      ),
    ).toBe("active");
  });
});

describe("splitSessionAgents", () => {
  it("按状态分区，保持原顺序", () => {
    const running = agent({ agentId: "r", status: "active" });
    const stale = agent({ agentId: "s", status: "stale" });
    const closed = agent({ agentId: "c", status: "closed" });
    const unknown = agent({ agentId: "u", status: "unknown" });

    const split = splitSessionAgents([running, closed, stale, unknown]);

    expect(split.running.map((entry) => entry.agentId)).toEqual(["r", "s"]);
    expect(split.settled.map((entry) => entry.agentId)).toEqual(["c", "u"]);
  });

  it("身份行仍 active 但容器已停的子代理进「已结束」；根行 idle 留在「运行中」", () => {
    const endedChild = agent({
      agentId: "child-ended",
      agentType: "child",
      status: "active",
      runtimeState: "idle",
    });
    const liveChild = agent({
      agentId: "child-live",
      agentType: "child",
      status: "active",
      runtimeState: "running",
    });
    const idleRoot = agent({
      agentId: "root",
      agentType: "root",
      status: "active",
      runtimeState: "idle",
    });
    const waitingChild = agent({
      agentId: "child-waiting",
      agentType: "child",
      status: "active",
      runtimeState: "waiting_approval",
    });
    const waitingInputChild = agent({
      agentId: "child-waiting-input",
      agentType: "child",
      status: "active",
      runtimeState: "waiting_input",
    });

    const split = splitSessionAgents([
      endedChild,
      liveChild,
      idleRoot,
      waitingChild,
      waitingInputChild,
    ]);

    expect(split.running.map((entry) => entry.agentId)).toEqual([
      "child-live",
      "root",
      "child-waiting",
      "child-waiting-input",
    ]);
    expect(split.settled.map((entry) => entry.agentId)).toEqual(["child-ended"]);
  });
});

describe("projectParkedTurnTaskCounts", () => {
  it("active → 运行中；closed / ended → 完成；stale → 异常", () => {
    const counts = projectParkedTurnTaskCounts([
      agent({ agentId: "live", agentType: "child", status: "active", runtimeState: "running" }),
      agent({ agentId: "closed", status: "closed" }),
      agent({
        agentId: "ended",
        agentType: "child",
        status: "active",
        runtimeState: "stopped",
      }),
      agent({ agentId: "stale", status: "stale" }),
    ]);

    expect(counts).toEqual({ running: 1, completed: 2, failed: 1 });
  });

  it("unknown 不归入任何一档（宁可少算，不把未知说成异常）", () => {
    const counts = projectParkedTurnTaskCounts([
      agent({ agentId: "unknown", status: "unknown" }),
      agent({ agentId: "stale", status: "stale" }),
    ]);

    expect(counts).toEqual({ running: 0, completed: 0, failed: 1 });
  });

  it("等待审批 / 等待输入计入运行中（未结束的挂起任务）", () => {
    const counts = projectParkedTurnTaskCounts([
      agent({ agentId: "wait-approval", agentType: "child", runtimeState: "waiting_approval" }),
      agent({ agentId: "wait-input", agentType: "child", runtimeState: "waiting_input" }),
      agent({ agentId: "closed", status: "closed" }),
    ]);

    expect(counts).toEqual({ running: 2, completed: 1, failed: 0 });
  });

  it("空目录（后端未覆盖）返回全 0，由呈现端降级为义务数", () => {
    expect(projectParkedTurnTaskCounts([])).toEqual({
      running: 0,
      completed: 0,
      failed: 0,
    });
  });
});

describe("agentPathSegments", () => {
  it("拆段并剔除空段", () => {
    expect(agentPathSegments("/root/child-1/grand-1")).toEqual([
      "root",
      "child-1",
      "grand-1",
    ]);
    expect(agentPathSegments("root//child/")).toEqual(["root", "child"]);
    expect(agentPathSegments(null)).toEqual([]);
    expect(agentPathSegments("  ")).toEqual([]);
  });
});

describe("agentMetaFacts", () => {
  it("只上报真实存在的字段（缺项不补默认值）", () => {
    expect(agentMetaFacts(agent({ agentId: "a1" }))).toEqual([]);

    const facts = agentMetaFacts(
      agent({
        agentId: "a1",
        model: "deepseek-chat",
        provider: "deepseek",
        workflow: "research",
        teamId: "team-1",
        teammateId: "mate-1",
        routeWarnings: ["w1", "w2"],
      }),
    );

    expect(facts).toEqual([
      { key: "model", value: "deepseek-chat" },
      { key: "provider", value: "deepseek" },
      { key: "workflow", value: "research" },
      { key: "team", value: "mate-1" },
      { key: "warnings", value: "2" },
    ]);
  });

  it("team 缺 teammate 时回退 team_id", () => {
    const facts = agentMetaFacts(agent({ agentId: "a1", teamId: "team-1" }));
    expect(facts).toEqual([{ key: "team", value: "team-1" }]);
  });
});

describe("agentDurationSpan（记录跨度：不推算、不补零）", () => {
  it("createdAt → updatedAt；有 closedAt 时以 closedAt 为终点", () => {
    const updated = agentDurationSpan(
      agent({
        agentId: "a1",
        createdAt: "2026-09-13T10:00:00.000Z",
        updatedAt: "2026-09-13T10:01:30.000Z",
      }),
    );
    expect(updated).toEqual({
      ms: 90_000,
      from: "2026-09-13T10:00:00.000Z",
      to: "2026-09-13T10:01:30.000Z",
    });

    const closed = agentDurationSpan(
      agent({
        agentId: "a1",
        createdAt: "2026-09-13T10:00:00.000Z",
        updatedAt: "2026-09-13T12:00:00.000Z",
        closedAt: "2026-09-13T10:30:00.000Z",
      }),
    );
    expect(closed?.to).toBe("2026-09-13T10:30:00.000Z");
    expect(closed?.ms).toBe(1_800_000);
  });

  it("端点缺失 / 无法解析 / 终点早于起点 → null", () => {
    expect(agentDurationSpan(agent({ agentId: "a1" }))).toBeNull();
    expect(
      agentDurationSpan(agent({ agentId: "a1", createdAt: "not-a-date", updatedAt: "2026-09-13T10:00:00.000Z" })),
    ).toBeNull();
    expect(
      agentDurationSpan(
        agent({
          agentId: "a1",
          createdAt: "2026-09-13T11:00:00.000Z",
          updatedAt: "2026-09-13T10:00:00.000Z",
        }),
      ),
    ).toBeNull();
  });
});

describe("formatAgentDuration（秒 → 分 → 时 → 天 → 月 → 年梯度）", () => {
  it("按量级选择档位与精度", () => {
    expect(formatAgentDuration(0)).toEqual({
      key: "duration.seconds",
      values: { seconds: "0" },
    });
    expect(formatAgentDuration(59_400)).toEqual({
      key: "duration.seconds",
      values: { seconds: "59" },
    });
    expect(formatAgentDuration(90_000)).toEqual({
      key: "duration.minutes",
      values: { minutes: "1", seconds: "30" },
    });
    expect(formatAgentDuration(3_600_000 + 125_000)).toEqual({
      key: "duration.hours",
      values: { hours: "1", minutes: "02", seconds: "05" },
    });
    expect(formatAgentDuration(2 * 86_400_000)).toEqual({
      key: "duration.days",
      values: { days: "2" },
    });
    expect(formatAgentDuration(2 * 86_400_000 + 3 * 3_600_000)).toEqual({
      key: "duration.daysHours",
      values: { days: "2", hours: "3" },
    });
    expect(formatAgentDuration(45 * 86_400_000)).toEqual({
      key: "duration.monthsDays",
      values: { months: "1", days: "15" },
    });
    expect(formatAgentDuration(400 * 86_400_000)).toEqual({
      key: "duration.yearsMonths",
      values: { years: "1", months: "1" },
    });
  });

  it("NaN / 负数 → null（不猜）", () => {
    expect(formatAgentDuration(Number.NaN)).toBeNull();
    expect(formatAgentDuration(Number.POSITIVE_INFINITY)).toBeNull();
    expect(formatAgentDuration(-1)).toBeNull();
  });
});

describe("formatAgentDurationExact", () => {
  it("不足一天与紧凑文案一致；≥ 一天补零到秒", () => {
    expect(formatAgentDurationExact(90_000)).toEqual({
      key: "duration.minutes",
      values: { minutes: "1", seconds: "30" },
    });
    expect(formatAgentDurationExact(86_400_000 + 3_661_000)).toEqual({
      key: "duration.exactDays",
      values: { days: "1", hours: "01", minutes: "01", seconds: "01" },
    });
  });
});

describe("buildAgentForest / flattenAgentTree", () => {
  const rootAgent = agent({ agentId: "root", agentPath: "/root" });
  const child = agent({ agentId: "c1", agentPath: "/root/c1", parentAgentId: "root" });
  const grand = agent({ agentId: "g1", agentPath: "/root/c1/g1", parentAgentId: "c1" });

  it("按 parentAgentId 建树并派生层级 / 后代计数", () => {
    const forest = buildAgentForest([grand, rootAgent, child]);

    expect(forest).toHaveLength(1);
    expect(forest[0]!.agent.agentId).toBe("root");
    expect(forest[0]!.descendantCount).toBe(2);
    expect(forest[0]!.children[0]!.agent.agentId).toBe("c1");
    expect(forest[0]!.children[0]!.depth).toBe(1);
    expect(forest[0]!.children[0]!.children[0]!.depth).toBe(2);
  });

  it("parentAgentId 缺失时按 agent_path 最长前缀祖先回退", () => {
    const orphanedGrand = { ...grand, parentAgentId: null };
    const forest = buildAgentForest([rootAgent, child, orphanedGrand]);

    expect(forest).toHaveLength(1);
    expect(forest[0]!.children[0]!.children.map((node) => node.agent.agentId)).toEqual(["g1"]);
  });

  it("解析不到父行的身份行保留为顶层（不丢弃）", () => {
    const detached = agent({ agentId: "x1", agentPath: "/other/x1" });
    const forest = buildAgentForest([rootAgent, child, detached]);

    expect(forest.map((node) => node.agent.agentId).sort()).toEqual(["root", "x1"]);
  });

  it("环数据断开闭环边，不递归爆栈", () => {
    const a = agent({ agentId: "a", agentPath: "/a", parentAgentId: "b" });
    const b = agent({ agentId: "b", agentPath: "/b", parentAgentId: "a" });

    const rows = flattenAgentTree(buildAgentForest([a, b]));

    expect(rows.map((row) => row.agent.agentId).sort()).toEqual(["a", "b"]);
  });

  it("折叠只隐藏该节点的后代（其余行保持可见）", () => {
    const forest = buildAgentForest([rootAgent, child, grand]);
    const expanded = flattenAgentTree(forest);
    expect(expanded.map((row) => row.agent.agentId)).toEqual(["root", "c1", "g1"]);
    expect(expanded[1]).toMatchObject({ depth: 1, childCount: 1, hiddenDescendantCount: 1 });

    const collapsed = flattenAgentTree(forest, new Set(["c1"]));
    expect(collapsed.map((row) => row.agent.agentId)).toEqual(["root", "c1"]);
  });
});

describe("agentReadOnlyReason（只回答能证实的两种原因）", () => {
  it("已关闭身份行 → closed-record", () => {
    expect(agentReadOnlyReason(agent({ agentId: "a1", status: "closed" }), null)).toBe(
      "closed-record",
    );
  });

  it("父身份行不在 active（stale / closed / unknown）→ parent-offline", () => {
    for (const status of ["stale", "closed", "unknown"] as const) {
      expect(
        agentReadOnlyReason(
          agent({ agentId: "a1", status: "active" }),
          agent({ agentId: "p1", status }),
        ),
      ).toBe("parent-offline");
    }
  });

  it("父身份行 active 或无父行 → null（无理由不展示只读解释）", () => {
    expect(
      agentReadOnlyReason(
        agent({ agentId: "a1", status: "active" }),
        agent({ agentId: "p1", status: "active" }),
      ),
    ).toBeNull();
    expect(agentReadOnlyReason(agent({ agentId: "a1", status: "active" }), null)).toBeNull();
  });

  it("自身已关闭优先于父离线", () => {
    expect(
      agentReadOnlyReason(
        agent({ agentId: "a1", status: "closed" }),
        agent({ agentId: "p1", status: "closed" }),
      ),
    ).toBe("closed-record");
  });
});

describe("agentTranscriptTarget（G8 下钻目标：只认后端上报的 sessionId）", () => {
  it("有会话键：sessionId 原样、标题用身份路径、role 取身份类型", () => {
    expect(
      agentTranscriptTarget(
        agent({
          agentId: "c1",
          sessionId: "sess-c1",
          agentPath: "/root/c1",
          agentType: "child",
          status: "active",
          runtimeState: "running",
        }),
      ),
    ).toEqual({
      sessionId: "sess-c1",
      agentId: "/root/c1",
      role: "child",
      status: "active",
    });
  });

  it("无 agent_path 时标题回退 agentId；状态用展示状态（含 ended 收敛）", () => {
    expect(
      agentTranscriptTarget(
        agent({
          agentId: "c2",
          sessionId: "sess-c2",
          agentType: "child",
          status: "active",
          runtimeState: "idle",
        }),
      ),
    ).toEqual({
      sessionId: "sess-c2",
      agentId: "c2",
      role: "child",
      status: "ended",
    });
  });

  it("sessionId 缺失 / 空白 → null（不用 agentId 冒充会话键）", () => {
    expect(agentTranscriptTarget(agent({ agentId: "c3" }))).toBeNull();
    expect(agentTranscriptTarget(agent({ agentId: "c4", sessionId: "   " }))).toBeNull();
  });
});
