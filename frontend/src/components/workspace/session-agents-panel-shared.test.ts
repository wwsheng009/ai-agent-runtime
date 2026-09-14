// P2-1A：子代理面板纯派生函数单测。

import { describe, expect, it } from "vitest";

import {
  agentDisplayName,
  agentMetaFacts,
  agentPathSegments,
  agentStatusLabelKey,
  canResumeAgent,
  canStopAgent,
  isAgentRunning,
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
    expect(isAgentRunning("closed")).toBe(false);
    expect(isAgentRunning("unknown")).toBe(false);
  });

  it("状态文案键与状态一一对应", () => {
    expect(agentStatusLabelKey("active")).toBe("panels.agents.status.active");
    expect(agentStatusLabelKey("unknown")).toBe("panels.agents.status.unknown");
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
