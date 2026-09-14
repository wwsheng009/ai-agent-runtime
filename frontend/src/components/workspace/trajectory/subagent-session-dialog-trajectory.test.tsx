// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  flush,
  fetchSessionRuntimeEvents,
  hangStreamUntilAbort,
  resolveSessionToolApproval,
  streamSessionRuntime,
  stubResizeObserver,
  type ReactActEnvironmentGlobal,
} from "@/components/workspace/trajectory/subagent-session-dialog.test-helpers";
import { TrajectoryView } from "@/components/workspace/trajectory/trajectory-view";
import { createTrajectoryStore } from "@/hooks/workspace/use-trajectory-snapshot";

vi.mock("@/api/runtime/sessions", () => ({
  fetchSessionRuntimeEvents: (...args: unknown[]) => fetchSessionRuntimeEvents(...args),
  resolveSessionToolApproval: (...args: unknown[]) =>
    resolveSessionToolApproval(...args),
}));

vi.mock("@/api/runtime/sse", () => ({
  streamSessionRuntime: (...args: unknown[]) => streamSessionRuntime(...args),
}));

beforeEach(() => {
  stubResizeObserver();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("TrajectoryView 子会话下钻入口", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    fetchSessionRuntimeEvents.mockReset();
    streamSessionRuntime.mockReset();
    fetchSessionRuntimeEvents.mockResolvedValue({ events: [], count: 0, latest_seq: 0 });
    streamSessionRuntime.mockImplementation(hangStreamUntilAbort);
  });

  afterEach(() => {
    act(() => {
      root.unmount();
    });
    container.remove();
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = false;
  });

  it("从 subagent item 打开子会话且不污染父轨迹 store", async () => {
    const parentStore = createTrajectoryStore();
    await act(async () => {
      root.render(<TrajectoryView store={parentStore} sessionId="parent-1" />);
    });
    await act(async () => {
      parentStore.push("subagent", {
        session_id: "child-42",
        role: "researcher",
        status: "running",
        _event: { sequence: 1 },
      });
      parentStore.flush();
    });

    const parentItemsBefore = parentStore.getSnapshot().items.length;
    const row = container.querySelector('[data-trajectory-row="true"]');
    expect(row).toBeInstanceOf(HTMLElement);
    act(() => {
      row?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    const openButton = container.querySelector("[data-open-subagent-session]");
    expect(openButton?.getAttribute("data-open-subagent-session")).toBe("child-42");
    act(() => {
      openButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await flush();

    expect(container.querySelector("[data-subagent-session-dialog]")).toBeInstanceOf(
      HTMLElement,
    );
    expect(fetchSessionRuntimeEvents).toHaveBeenCalledWith(
      "child-42",
      expect.objectContaining({ after: 0 }),
    );
    // 父 store 未被子的工具/审批事件写入（P1-5 验收②）。
    expect(parentStore.getSnapshot().items.length).toBe(parentItemsBefore);
    act(() => {
      parentStore.dispose();
    });
  });
});
