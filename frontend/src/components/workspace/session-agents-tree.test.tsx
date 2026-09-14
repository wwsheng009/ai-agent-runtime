// @vitest-environment jsdom

// P2-9 子片 2：子代理树渲染单测——折叠到末级、只读原因、记录跨度。
// 断言只依赖结构（aria-level / data-depth / testid）与原始 ISO 端点，不依赖具体语言文案。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { RuntimeAgentRecord } from "@/types/runtime";

import { SessionAgentsTree } from "./session-agents-tree";

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

const rootAgent = agent({ agentId: "root", agentPath: "/root", agentType: "root" });
const child = agent({
  agentId: "child-1",
  parentAgentId: "root",
  agentPath: "/root/child-1",
  agentType: "child",
  nickname: "researcher",
});
const grandChild = agent({
  agentId: "worker-1",
  parentAgentId: "child-1",
  agentPath: "/root/child-1/worker-1",
  agentType: "child",
  nickname: "scout",
});

describe("SessionAgentsTree", () => {
  let container: HTMLDivElement;
  let root: Root;
  const onClose = vi.fn();
  const onResume = vi.fn();

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    onClose.mockReset();
    onResume.mockReset();
  });

  afterEach(() => {
    act(() => {
      root.unmount();
    });
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function render(agents: RuntimeAgentRecord[]) {
    act(() => {
      root.render(
        <SessionAgentsTree
          actionError={null}
          actionErrorAgentId={null}
          agents={agents}
          onClose={onClose}
          onResume={onResume}
          pendingAgentId={null}
        />,
      );
    });
  }

  function rows(): HTMLElement[] {
    return Array.from(container.querySelectorAll<HTMLElement>('[data-testid="agent-row"]'));
  }

  function rowIds(): (string | null)[] {
    return rows().map((row) => row.getAttribute("data-agent-id"));
  }

  it("乱序输入也按父子层级渲染到末级", () => {
    render([grandChild, rootAgent, child]);

    expect(rowIds()).toEqual(["root", "child-1", "worker-1"]);
    expect(rows().map((row) => row.getAttribute("aria-level"))).toEqual(["1", "2", "3"]);
    expect(rows().map((row) => row.getAttribute("data-depth"))).toEqual(["0", "1", "2"]);

    const tree = container.querySelector('[data-testid="agents-tree"]');
    expect(tree).not.toBeNull();
    expect(tree!.tagName).toBe("UL");
    // 叶子行不声明展开态，分支行声明。
    expect(rows()[2]!.hasAttribute("aria-expanded")).toBe(false);
    expect(rows()[1]!.getAttribute("aria-expanded")).toBe("true");
  });

  it("折叠只隐藏该节点后代，展开后回到末级", () => {
    render([rootAgent, child, grandChild]);

    const toggle = container.querySelector<HTMLButtonElement>(
      '[data-agent-id="child-1"] [data-testid="agent-branch-toggle"]',
    );
    expect(toggle).not.toBeNull();
    expect(toggle!.getAttribute("aria-label")).toBeTruthy();

    act(() => {
      toggle!.click();
    });
    expect(rowIds()).toEqual(["root", "child-1"]);
    expect(
      container
        .querySelector('[data-agent-id="child-1"]')!
        .getAttribute("aria-expanded"),
    ).toBe("false");

    act(() => {
      container
        .querySelector<HTMLButtonElement>(
          '[data-agent-id="child-1"] [data-testid="agent-branch-toggle"]',
        )!
        .click();
    });
    expect(rowIds()).toEqual(["root", "child-1", "worker-1"]);
  });

  it("只读原因：已关闭记录与父代理离线各说各话，无依据时不渲染", () => {
    render([
      agent({ agentId: "root", agentPath: "/root" }),
      agent({
        agentId: "closed-1",
        parentAgentId: "root",
        agentPath: "/root/closed-1",
        status: "closed",
      }),
      agent({
        agentId: "stale-child",
        parentAgentId: "root",
        agentPath: "/root/stale-child",
        status: "stale",
      }),
      agent({
        agentId: "offline-child",
        parentAgentId: "stale-child",
        agentPath: "/root/stale-child/offline-child",
        status: "active",
      }),
      agent({
        agentId: "healthy-child",
        parentAgentId: "root",
        agentPath: "/root/healthy-child",
        status: "active",
      }),
    ]);

    const reason = (agentId: string) =>
      container
        .querySelector(`[data-agent-id="${agentId}"] [data-testid="agent-readonly"]`)
        ?.textContent ?? null;

    expect(reason("closed-1")).toBeTruthy();
    expect(reason("offline-child")).toBeTruthy();
    // 已关闭且父不可用的行，按「一次性记录」优先解释。
    expect(reason("stale-child")).toBeNull();
    expect(reason("healthy-child")).toBeNull();
    expect(reason("root")).toBeNull();
  });

  it("记录跨度：端点齐备才渲染 chip，title 同时给出精确跨度与原始端点", () => {
    render([
      agent({ agentId: "root", agentPath: "/root" }),
      agent({
        agentId: "child-1",
        parentAgentId: "root",
        agentPath: "/root/child-1",
        createdAt: "2026-09-13T10:00:00.000Z",
        updatedAt: "2026-09-13T10:01:30.000Z",
      }),
      agent({
        agentId: "no-stamps",
        parentAgentId: "root",
        agentPath: "/root/no-stamps",
      }),
    ]);

    const chip = container.querySelector<HTMLElement>(
      '[data-agent-id="child-1"] [data-testid="agent-duration"]',
    );
    expect(chip).not.toBeNull();
    expect(chip!.textContent).toBeTruthy();
    expect(chip!.getAttribute("title")).toContain("2026-09-13T10:00:00.000Z");
    expect(chip!.getAttribute("title")).toContain("2026-09-13T10:01:30.000Z");

    // 缺端点不补零：不渲染 chip。
    expect(
      container.querySelector('[data-agent-id="no-stamps"] [data-testid="agent-duration"]'),
    ).toBeNull();
  });
});
