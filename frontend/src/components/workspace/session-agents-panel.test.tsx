// @vitest-environment jsdom

// P2-1A：子代理控制面面板渲染与动作单测。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  deriveSessionAgentTree,
  type UseSessionAgentsResult,
} from "@/hooks/use-session-agents";
import { RuntimeApiError } from "@/api/runtime/shared";
import type { RuntimeAgentCatalog, RuntimeAgentRecord } from "@/types/runtime";

import { SessionAgentsPanel, type SessionAgentsPanelProps } from "./session-agents-panel";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const SESSION_ID = "sess-child-1";

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

const rootAgent = agent({
  agentId: "root",
  sessionId: "sess-root",
  agentPath: "/root",
  depth: 0,
  agentType: "root",
  nickname: "main",
});

const currentAgent = agent({
  agentId: "child-1",
  parentAgentId: "root",
  parentSessionId: "sess-root",
  sessionId: SESSION_ID,
  agentPath: "/root/child-1",
  depth: 1,
  agentType: "child",
  nickname: "researcher",
  model: "deepseek-chat",
  provider: "deepseek",
});

const runningChild = agent({
  agentId: "worker-1",
  parentAgentId: "child-1",
  parentSessionId: SESSION_ID,
  sessionId: "sess-worker-1",
  agentPath: "/root/child-1/worker-1",
  depth: 2,
  agentType: "child",
  nickname: "scout",
  model: "deepseek-chat",
  provider: "deepseek",
});

const closedGrand = agent({
  agentId: "worker-2",
  parentAgentId: "child-1",
  parentSessionId: SESSION_ID,
  agentPath: "/root/child-1/worker-2",
  depth: 2,
  agentType: "child",
  status: "closed",
});

const unknownAgent = agent({
  agentId: "worker-3",
  agentPath: "/root/child-1/worker-3",
  depth: 2,
  status: "unknown",
});

function makeResult(
  agents: RuntimeAgentRecord[],
  partial: Partial<UseSessionAgentsResult> = {},
): UseSessionAgentsResult {
  const catalog: RuntimeAgentCatalog = {
    agents,
    count: agents.length,
    source: "agent_control_agents",
    limit: 500,
    includeClosed: true,
  };
  return {
    status: "ready",
    error: null,
    catalog,
    truncated: false,
    tree: deriveSessionAgentTree(agents, SESSION_ID),
    refresh: vi.fn(),
    closeAgent: vi.fn().mockResolvedValue(true),
    resumeAgent: vi.fn().mockResolvedValue(true),
    pendingAgentId: null,
    actionError: null,
    actionErrorAgentId: null,
    ...partial,
  };
}

function flush() {
  return Promise.resolve().then(() => Promise.resolve());
}

describe("SessionAgentsPanel", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  const onClose = vi.fn();

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    onClose.mockReset();
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  async function renderPanel(
    result: UseSessionAgentsResult,
    props: Pick<SessionAgentsPanelProps, "onOpenTranscript"> | Record<string, never> = {},
  ) {
    await act(async () => {
      root?.render(
        <SessionAgentsPanel agents={result} onClose={onClose} open {...props} />,
      );
    });
    await act(flush);
  }

  function panel(): HTMLElement | null {
    return document.body.querySelector('[data-testid="session-agents-panel"]');
  }

  it("控制面不可用：明说原因 + 展示后端错误，不渲染 lineage", async () => {
    await renderPanel(
      makeResult([], {
        status: "unavailable",
        error: new RuntimeApiError(503, { error: "agent session controller not configured" }),
        catalog: null,
        tree: deriveSessionAgentTree([], SESSION_ID),
      }),
    );

    expect(document.body.querySelector('[data-testid="agents-unavailable"]')).not.toBeNull();
    expect(document.body.textContent).toContain("子代理控制面不可用");
    expect(document.body.textContent).toContain("agent session controller not configured");
    expect(document.body.querySelector('[data-testid="agents-lineage"]')).toBeNull();
  });

  it("加载失败：展示后端错误并可重试", async () => {
    const refresh = vi.fn();
    await renderPanel(
      makeResult([], {
        status: "error",
        error: new Error("agents backend down"),
        catalog: null,
        refresh,
        tree: deriveSessionAgentTree([], SESSION_ID),
      }),
    );

    expect(document.body.textContent).toContain("子代理目录加载失败");
    expect(document.body.textContent).toContain("agents backend down");
    const retry = document.body.querySelector('[data-testid="agents-error"] button');
    await act(async () => {
      retry?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it("身份行缺失：如实说明，不伪造 lineage / 空后代", async () => {
    await renderPanel(makeResult([rootAgent, runningChild]));

    expect(document.body.querySelector('[data-testid="agents-no-identity"]')).not.toBeNull();
    expect(document.body.textContent).toContain("当前会话未登记子代理身份");
    expect(document.body.querySelector('[data-testid="agents-descendants"]')).toBeNull();
  });

  it("lineage 面包屑 + 后代分区 + 状态动作", async () => {
    await renderPanel(makeResult([rootAgent, currentAgent, runningChild, closedGrand]));

    const nodes = document.body.querySelectorAll('[data-testid="agents-lineage-node"]');
    expect(nodes).toHaveLength(2);
    expect(nodes[1]?.textContent).toContain("researcher");
    expect(nodes[1]?.getAttribute("data-current")).toBe("true");

    expect(panel()?.textContent).toContain("进行中（1）");
    expect(panel()?.textContent).toContain("已结束（1）");
    expect(panel()?.textContent).toContain("模型 deepseek-chat");

    const runningRow = document.body.querySelector('[data-agent-id="worker-1"]');
    expect(runningRow?.textContent).toContain("停止");
    const closedRow = document.body.querySelector('[data-agent-id="worker-2"]');
    expect(closedRow?.textContent).toContain("恢复");
  });

  it("G8 下钻：有会话键的行渲染只读入口并回传 target，无会话键的行不给入口", async () => {
    const onOpenTranscript = vi.fn();
    await renderPanel(makeResult([rootAgent, currentAgent, runningChild, closedGrand]), {
      onOpenTranscript,
    });

    const entry = document.body.querySelector(
      '[data-agent-id="worker-1"] [data-testid="agent-transcript"]',
    );
    expect(entry).not.toBeNull();
    expect(entry?.textContent).toContain("会话记录");

    await act(async () => {
      entry?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onOpenTranscript).toHaveBeenCalledTimes(1);
    expect(onOpenTranscript).toHaveBeenCalledWith({
      sessionId: "sess-worker-1",
      agentId: "/root/child-1/worker-1",
      role: "child",
      status: "active",
    });

    // 没有后端会话键的行（这里 closedGrand.sessionId 为 null）：宁可不给入口，
    // 也不拿 agentId / agentPath 冒充 runtime/events 的会话键。
    expect(
      document.body.querySelector('[data-agent-id="worker-2"] [data-testid="agent-transcript"]'),
    ).toBeNull();
  });

  it("unknown 状态不给动作并说明原因", async () => {
    await renderPanel(makeResult([rootAgent, currentAgent, unknownAgent]));

    const row = document.body.querySelector('[data-agent-id="worker-3"]');
    expect(row?.textContent).toContain("状态未知，不提供操作");
    expect(row?.querySelector("button")).toBeNull();
  });

  it("点击停止 / 恢复调用对应动作", async () => {
    const result = makeResult([rootAgent, currentAgent, runningChild, closedGrand]);
    await renderPanel(result);

    const stopButton = document.body.querySelector(
      '[data-agent-id="worker-1"] button',
    ) as HTMLButtonElement | null;
    await act(async () => {
      stopButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(result.closeAgent).toHaveBeenCalledWith("worker-1");

    const resumeButton = document.body.querySelector(
      '[data-agent-id="worker-2"] button',
    ) as HTMLButtonElement | null;
    await act(async () => {
      resumeButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(result.resumeAgent).toHaveBeenCalledWith("worker-2");
  });

  it("动作进行中禁用按钮并显示进行态文案", async () => {
    await renderPanel(
      makeResult([rootAgent, currentAgent, runningChild], { pendingAgentId: "worker-1" }),
    );

    const stopButton = document.body.querySelector(
      '[data-agent-id="worker-1"] button',
    ) as HTMLButtonElement | null;
    expect(stopButton?.disabled).toBe(true);
    expect(stopButton?.textContent).toContain("停止中");
  });

  it("动作失败展示后端错误原文（仅该行）", async () => {
    await renderPanel(
      makeResult([rootAgent, currentAgent, runningChild, closedGrand], {
        actionError: new RuntimeApiError(403, { error: "forbidden" }),
        actionErrorAgentId: "worker-2",
      }),
    );

    const failedRow = document.body.querySelector('[data-agent-id="worker-2"]');
    expect(failedRow?.textContent).toContain("操作失败");
    expect(failedRow?.textContent).toContain("forbidden");
    const healthyRow = document.body.querySelector('[data-agent-id="worker-1"]');
    expect(healthyRow?.textContent).not.toContain("操作失败");
  });

  it("目录触顶：显式提示后端 count 与 limit", async () => {
    const result = makeResult([rootAgent, currentAgent, runningChild], {
      truncated: true,
    });
    result.catalog = { ...result.catalog!, count: 900, limit: 500 };
    await renderPanel(result);

    const notice = document.body.querySelector('[data-testid="agents-truncated"]');
    expect(notice?.textContent).toContain("900");
    expect(notice?.textContent).toContain("500");
  });

  it("无后代：空态说明而非空白分区", async () => {
    await renderPanel(makeResult([rootAgent, currentAgent]));

    expect(document.body.querySelector('[data-testid="agents-empty"]')).not.toBeNull();
    expect(document.body.textContent).toContain("当前会话没有子代理");
    expect(document.body.querySelector('[data-testid="agents-running"]')).toBeNull();
  });

  it("未打开时不渲染（portal 不挂载）", async () => {
    await act(async () => {
      root?.render(
        <SessionAgentsPanel agents={makeResult([rootAgent])} onClose={onClose} open={false} />,
      );
    });
    expect(panel()).toBeNull();
  });
});
