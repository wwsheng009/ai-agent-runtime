// @vitest-environment jsdom

// P2-1A：子代理控制面状态机单测（两段式加载 / lineage 派生 / 动作与降级）。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/api/runtime/agents", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/runtime/agents")>();
  return {
    ...actual,
    closeRuntimeAgent: vi.fn(),
    listRuntimeAgents: vi.fn(),
    resumeRuntimeAgent: vi.fn(),
  };
});

import {
  closeRuntimeAgent,
  listRuntimeAgents,
  resumeRuntimeAgent,
} from "@/api/runtime/agents";
import { RuntimeApiError } from "@/api/runtime/shared";

import {
  buildAgentLineage,
  deriveSessionAgentTree,
  listAgentDescendants,
  pickCurrentAgent,
  useSessionAgents,
} from "./use-session-agents";

import type { RuntimeAgentCatalog, RuntimeAgentRecord } from "@/types/runtime";

const mockList = vi.mocked(listRuntimeAgents);
const mockClose = vi.mocked(closeRuntimeAgent);
const mockResume = vi.mocked(resumeRuntimeAgent);

type HookSnapshot = ReturnType<typeof useSessionAgents>;
type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

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

const rootAgent = agent({
  agentId: "root",
  sessionId: "sess-root",
  agentPath: "/root",
  depth: 0,
  agentType: "root",
  nickname: "main",
});

const childAgent = agent({
  agentId: "child-1",
  parentAgentId: "root",
  parentSessionId: "sess-root",
  sessionId: "sess-child-1",
  agentPath: "/root/child-1",
  depth: 1,
  agentType: "child",
  nickname: "researcher",
  model: "deepseek-chat",
});

const grandChildAgent = agent({
  agentId: "grand-1",
  parentAgentId: "child-1",
  parentSessionId: "sess-child-1",
  sessionId: "sess-grand-1",
  agentPath: "/root/child-1/grand-1",
  depth: 2,
  agentType: "child",
  status: "closed",
});

function catalogOf(agents: RuntimeAgentRecord[], count = agents.length): RuntimeAgentCatalog {
  return { agents, count, source: "agent_control_agents", limit: 500, includeClosed: true };
}

function Harness({
  sessionId,
  onSnapshot,
}: {
  sessionId?: string;
  onSnapshot: (snapshot: HookSnapshot) => void;
}) {
  const snapshot = useSessionAgents({ sessionId });
  onSnapshot(snapshot);
  return null;
}

async function flush(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

describe("agent tree helpers", () => {
  it("pickCurrentAgent：无匹配 / 空会话 id 返回 null", () => {
    expect(pickCurrentAgent([rootAgent, childAgent], "sess-missing")).toBeNull();
    expect(pickCurrentAgent([rootAgent], "")).toBeNull();
  });

  it("pickCurrentAgent：多行时非终态优先、路径更深优先", () => {
    const stale = agent({
      agentId: "dup-old",
      sessionId: "sess-child-1",
      agentPath: "/root/child-1",
      depth: 1,
      status: "closed",
    });
    const picked = pickCurrentAgent([stale, childAgent], "sess-child-1");
    expect(picked?.agentId).toBe("child-1");
  });

  it("buildAgentLineage：root → current 链，按 parent_session_id 回溯", () => {
    const lineage = buildAgentLineage([rootAgent, childAgent, grandChildAgent], grandChildAgent);
    expect(lineage.map((entry) => entry.agentId)).toEqual(["root", "child-1", "grand-1"]);
    expect(buildAgentLineage([rootAgent], null)).toEqual([]);
  });

  it("listAgentDescendants：只取当前路径前缀下的行并按深度排序", () => {
    const descendants = listAgentDescendants(
      [rootAgent, childAgent, grandChildAgent],
      rootAgent,
    );
    expect(descendants.map((entry) => entry.agentId)).toEqual(["child-1", "grand-1"]);

    expect(
      listAgentDescendants([rootAgent, childAgent, grandChildAgent], childAgent),
    ).toEqual([grandChildAgent]);
    expect(listAgentDescendants([rootAgent], agent({ agentId: "no-path" }))).toEqual([]);
  });

  it("deriveSessionAgentTree：无身份行时 hasIdentity=false 且不伪造 lineage", () => {
    const tree = deriveSessionAgentTree([rootAgent, childAgent], "sess-unknown");
    expect(tree.hasIdentity).toBe(false);
    expect(tree.currentAgent).toBeNull();
    expect(tree.lineage).toEqual([]);
    expect(tree.descendants).toEqual([]);
  });
});

describe("useSessionAgents", () => {
  let container: HTMLDivElement;
  let root: Root;
  let latest: HookSnapshot;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    mockList.mockReset();
    mockClose.mockReset();
    mockResume.mockReset();
  });

  afterEach(() => {
    act(() => {
      root.unmount();
    });
    container.remove();
  });

  async function mount(sessionId: string) {
    await act(async () => {
      root.render(
        <Harness
          sessionId={sessionId}
          onSnapshot={(snapshot) => {
            latest = snapshot;
          }}
        />,
      );
    });
    await flush();
  }

  it("会话 id 为空：保持 idle 且不发请求", async () => {
    await mount("");
    expect(latest.status).toBe("idle");
    expect(mockList).not.toHaveBeenCalled();
  });

  it("两段式加载：先按会话查身份，再按 root 拉树；lineage / 后代 / 触顶口径正确", async () => {
    mockList.mockImplementation(async (options) => {
      if (options?.sessionId === "sess-child-1") {
        return catalogOf([childAgent], 1);
      }
      return catalogOf([rootAgent, childAgent, grandChildAgent], 9);
    });

    await mount("sess-child-1");

    expect(mockList).toHaveBeenCalledTimes(2);
    expect(mockList.mock.calls[0]?.[0]).toMatchObject({ sessionId: "sess-child-1" });
    expect(mockList.mock.calls[1]?.[0]).toMatchObject({ rootSessionId: "sess-root" });

    expect(latest.status).toBe("ready");
    expect(latest.tree.currentAgent?.agentId).toBe("child-1");
    expect(latest.tree.lineage.map((entry) => entry.agentId)).toEqual(["root", "child-1"]);
    expect(latest.tree.descendants.map((entry) => entry.agentId)).toEqual(["grand-1"]);
    expect(latest.tree.hasIdentity).toBe(true);
    expect(latest.truncated).toBe(true);
  });

  it("身份行缺失：ready + hasIdentity=false（不伪造 root）", async () => {
    mockList.mockImplementation(async (options) => {
      if (options?.sessionId === "sess-lonely") {
        return catalogOf([], 0);
      }
      return catalogOf([rootAgent], 1);
    });

    await mount("sess-lonely");

    expect(latest.status).toBe("ready");
    expect(latest.tree.hasIdentity).toBe(false);
    expect(latest.tree.lineage).toEqual([]);
  });

  it("503 归类为 unavailable（与真实失败分开）", async () => {
    mockList.mockRejectedValue(new RuntimeApiError(503, { error: "not configured" }));

    await mount("sess-root");

    expect(latest.status).toBe("unavailable");
    expect(latest.error).toBeInstanceOf(RuntimeApiError);
    expect(latest.tree.hasIdentity).toBe(false);
  });

  it("500 按真实失败呈现", async () => {
    mockList.mockRejectedValue(new RuntimeApiError(500, { error: "boom" }));

    await mount("sess-root");

    expect(latest.status).toBe("error");
  });

  it("closeAgent：成功用响应行覆盖本地该行；失败记录 agent 维度错误", async () => {
    mockList.mockImplementation(async (options) => {
      if (options?.sessionId === "sess-child-1") {
        return catalogOf([childAgent], 1);
      }
      return catalogOf([rootAgent, childAgent, grandChildAgent], 3);
    });
    mockClose.mockResolvedValue(agent({ ...childAgent, status: "closed" }));

    await mount("sess-child-1");
    let closed = false;
    await act(async () => {
      closed = await latest.closeAgent("child-1");
    });

    expect(closed).toBe(true);
    expect(mockClose).toHaveBeenCalledWith("sess-child-1", "child-1");
    expect(latest.tree.descendants[0]?.status).toBe("closed");
    expect(latest.pendingAgentId).toBeNull();

    mockResume.mockRejectedValue(new RuntimeApiError(403, { error: "forbidden" }));
    let resumed = true;
    await act(async () => {
      resumed = await latest.resumeAgent("grand-1");
    });

    expect(resumed).toBe(false);
    expect(latest.actionErrorAgentId).toBe("grand-1");
    expect(latest.actionError).toBeInstanceOf(RuntimeApiError);
    expect(latest.pendingAgentId).toBeNull();
    // 失败不做本地乐观改写：grand-1（current=child-1 的唯一后代）仍是 closed。
    expect(latest.tree.descendants.map((entry) => entry.agentId)).toEqual(["grand-1"]);
    expect(latest.tree.descendants[0]?.status).toBe("closed");
  });

  it("closeAgent：空 agent id 不发请求", async () => {
    mockList.mockImplementation(async (options) =>
      options?.sessionId ? catalogOf([rootAgent], 1) : catalogOf([rootAgent], 1),
    );

    await mount("sess-root");
    let result = true;
    await act(async () => {
      result = await latest.closeAgent("  ");
    });

    expect(result).toBe(false);
    expect(mockClose).not.toHaveBeenCalled();
  });
});
