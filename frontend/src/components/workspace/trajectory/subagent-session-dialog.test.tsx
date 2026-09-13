// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  SubagentSessionDialog,
} from "@/components/workspace/trajectory/subagent-session-dialog";
import {
  subagentSessionTarget,
  type SubagentSessionTarget,
} from "@/components/workspace/trajectory/subagent-session-target";
import { TrajectoryView } from "@/components/workspace/trajectory/trajectory-view";
import { createTrajectoryStore } from "@/hooks/workspace/use-trajectory-snapshot";
import type { TrajectoryItem } from "@/lib/trajectory/types";
import type { SessionRuntimeEvent } from "@/types/runtime";

const fetchSessionRuntimeEvents = vi.fn();
const streamSessionRuntime = vi.fn();
const resolveSessionToolApproval = vi.fn();

vi.mock("@/api/runtime/sessions", () => ({
  fetchSessionRuntimeEvents: (...args: unknown[]) => fetchSessionRuntimeEvents(...args),
  resolveSessionToolApproval: (...args: unknown[]) =>
    resolveSessionToolApproval(...args),
}));

vi.mock("@/api/runtime/sse", () => ({
  streamSessionRuntime: (...args: unknown[]) => streamSessionRuntime(...args),
}));

// jsdom 无 ResizeObserver：stub 并提供容器高度，让 TrajectoryView 的虚拟滚动
// 窗口化生效（否则 clientHeight=0 → 不渲染任何行）。
beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      private callback: ResizeObserverCallback;
      constructor(callback: ResizeObserverCallback) {
        this.callback = callback;
      }
      observe(element: Element) {
        Object.defineProperty(element, "clientHeight", {
          configurable: true,
          value: 600,
        });
        this.callback([], this as unknown as ResizeObserver);
      }
      unobserve() {}
      disconnect() {}
    },
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function subagentItem(payload: Record<string, unknown>): TrajectoryItem {
  return {
    id: "subagent-7",
    seq: 7,
    kind: "subagent",
    causeId: "",
    status: "running",
    head: { kind: "structured", payload },
    createdAt: 7,
    updatedAt: 7,
  };
}

function chatEvent(seq: number, content: string): SessionRuntimeEvent {
  return {
    type: "chat.sse.chunk",
    session_id: "child-1",
    payload: { type: "text", content, seq },
    timestamp: "2026-09-13T00:00:00Z",
  };
}

function approvalEvent(
  seq: number,
  requestId: string,
  extra: Record<string, unknown> = {},
): SessionRuntimeEvent {
  return {
    type: "approval_requested",
    session_id: "child-1",
    timestamp: "2026-09-13T00:00:02Z",
    payload: {
      seq,
      request_id: requestId,
      tool_name: "shell",
      reason: "writes outside workspace",
      risk_level: "high",
      ...extra,
    },
  } as SessionRuntimeEvent;
}

function approvalResolvedEvent(seq: number, requestId: string): SessionRuntimeEvent {
  return {
    type: "approval_resolved",
    session_id: "child-1",
    timestamp: "2026-09-13T00:00:03Z",
    payload: { seq, request_id: requestId, allowed: false, tool_name: "shell" },
  } as SessionRuntimeEvent;
}

function captureStream() {
  let handlers: {
    onEvent?: (event: SessionRuntimeEvent) => void;
    signal?: AbortSignal;
  } = {};
  streamSessionRuntime.mockImplementation(
    (_sessionId: string, next: typeof handlers) => {
      handlers = next;
      return new Promise<void>((resolve) => {
        next.signal?.addEventListener("abort", () => resolve(), { once: true });
      });
    },
  );
  return {
    emit(event: SessionRuntimeEvent) {
      handlers.onEvent?.(event);
    },
  };
}

function flush() {
  return act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

describe("subagentSessionTarget", () => {
  it("从 chat SSE subagent 载荷解析子会话 ID 与 role", () => {
    const target = subagentSessionTarget(
      subagentItem({ session_id: "child-9", role: "researcher" }),
    );
    expect(target).toEqual({
      sessionId: "child-9",
      agentId: undefined,
      role: "researcher",
      status: undefined,
    });
  });

  it("终态镜像用 agent_id 兜底并把 status 带出", () => {
    const target = subagentSessionTarget(
      subagentItem({ agent_id: "child-9", session_id: "child-9", status: "completed" }),
    );
    expect(target?.sessionId).toBe("child-9");
    expect(target?.agentId).toBe("child-9");
    expect(target?.status).toBe("completed");
  });

  it("缺少 session 标识或非 subagent item 时返回 null", () => {
    expect(subagentSessionTarget(subagentItem({ role: "researcher" }))).toBeNull();
    expect(subagentSessionTarget(null)).toBeNull();
    expect(
      subagentSessionTarget({
        ...subagentItem({ session_id: "child-9" }),
        kind: "tool",
      }),
    ).toBeNull();
  });
});

describe("SubagentSessionDialog", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    fetchSessionRuntimeEvents.mockReset();
    streamSessionRuntime.mockReset();
    resolveSessionToolApproval.mockReset();
    resolveSessionToolApproval.mockResolvedValue({ ok: true });
    fetchSessionRuntimeEvents.mockResolvedValue({ events: [], count: 0, latest_seq: 0 });
    // 默认的实时流保持挂起，直到 signal abort。
    streamSessionRuntime.mockImplementation(
      (_sessionId: string, handlers: { signal?: AbortSignal }) =>
        new Promise<void>((resolve) => {
          handlers.signal?.addEventListener("abort", () => resolve(), { once: true });
        }),
    );
  });

  afterEach(() => {
    act(() => {
      root.unmount();
    });
    container.remove();
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = false;
  });

  function renderDialog(target: SubagentSessionTarget | null, onClose = () => {}) {
    act(() => {
      root.render(<SubagentSessionDialog onClose={onClose} target={target} />);
    });
  }

  it("关闭态（target=null）不渲染对话框且不发起请求", () => {
    renderDialog(null);
    expect(container.querySelector("[data-subagent-session-dialog]")).toBeNull();
    expect(fetchSessionRuntimeEvents).not.toHaveBeenCalled();
    expect(streamSessionRuntime).not.toHaveBeenCalled();
  });

  it("回填子会话事件并进入实时跟随（live=1）", async () => {
    fetchSessionRuntimeEvents.mockResolvedValue({
      events: [chatEvent(1, "child says hi")],
      count: 1,
      latest_seq: 1,
    });

    await act(async () => {
      renderDialog({ sessionId: "child-1", role: "researcher", status: "running" });
    });
    await flush();

    expect(fetchSessionRuntimeEvents).toHaveBeenCalledWith(
      "child-1",
      expect.objectContaining({ after: 0 }),
    );
    const rows = container.querySelectorAll("[data-subagent-session-row]");
    expect(rows.length).toBe(1);
    expect(container.textContent).toContain("child says hi");
    expect(container.textContent).toContain("researcher");

    // 实时流以回填后的游标续传，并订阅 live-only 事件。
    expect(streamSessionRuntime).toHaveBeenCalledWith(
      "child-1",
      expect.objectContaining({ after: 1, live: true }),
    );
    expect(
      container.querySelector("[data-subagent-session-status]")?.getAttribute(
        "data-subagent-session-status",
      ),
    ).toBe("live");
  });

  it("实时事件按序追加到子会话列表", async () => {
    let handlers: {
      onEvent?: (event: SessionRuntimeEvent) => void;
      signal?: AbortSignal;
    } = {};
    streamSessionRuntime.mockImplementation(
      (_sessionId: string, next: typeof handlers) => {
        handlers = next;
        return new Promise<void>((resolve) => {
          next.signal?.addEventListener("abort", () => resolve(), { once: true });
        });
      },
    );

    await act(async () => {
      renderDialog({ sessionId: "child-1" });
    });
    await flush();

    await act(async () => {
      // seq 需与回填游标衔接（空回填 → 下一事件即 seq=1），否则会被
      // reducer 当作乱序事件缓存等待前序补齐。
      handlers.onEvent?.(chatEvent(1, "streamed progress"));
      // store 按 rAF 批量冲刷（后台标签页 100ms 兜底），等待一帧后再断言。
      await new Promise((resolve) => setTimeout(resolve, 150));
    });

    expect(container.textContent).toContain("streamed progress");
  });

  it("live-only tool.progress 折叠进工具行（进行中输出可见，不新增行）", async () => {
    let handlers: {
      onEvent?: (event: SessionRuntimeEvent) => void;
      signal?: AbortSignal;
    } = {};
    streamSessionRuntime.mockImplementation(
      (_sessionId: string, next: typeof handlers) => {
        handlers = next;
        return new Promise<void>((resolve) => {
          next.signal?.addEventListener("abort", () => resolve(), { once: true });
        });
      },
    );

    await act(async () => {
      renderDialog({ sessionId: "child-1" });
    });
    await flush();

    await act(async () => {
      // 持久化的工具开始行（回填缺口由后一条事件补齐，见 replay 用例）。
      handlers.onEvent?.({
        type: "chat.sse.tool_start",
        timestamp: "2026-09-13T00:00:00Z",
        payload: {
          type: "tool_call",
          tool_call: { id: "call-7", name: "bash" },
          seq: 1,
        },
      } as SessionRuntimeEvent);
      // live-only 进度：不落库、无 seq，按到达顺序即时应用。
      handlers.onEvent?.({
        type: "tool.progress",
        timestamp: "2026-09-13T00:00:01Z",
        tool_name: "bash",
        payload: { tool_call_id: "call-7", partial: "compiling 3/7", live: true },
      } as SessionRuntimeEvent);
      await new Promise((resolve) => setTimeout(resolve, 150));
    });

    expect(container.textContent).toContain("compiling 3/7");
    const bashRows = [...container.querySelectorAll("[data-subagent-session-row]")].filter(
      (row) => row.textContent?.includes("bash"),
    );
    expect(bashRows).toHaveLength(1);
  });

  it("回填失败时显示错误且不静默", async () => {
    fetchSessionRuntimeEvents.mockRejectedValue(new Error("boom"));

    await act(async () => {
      renderDialog({ sessionId: "child-1" });
    });
    await flush();

    expect(container.querySelector("[data-subagent-session-error]")?.textContent).toContain(
      "boom",
    );
    expect(
      container.querySelector("[data-subagent-session-status]")?.getAttribute(
        "data-subagent-session-status",
      ),
    ).toBe("error");
  });

  it("inline 审批：approval_requested 显示入口，批准后提交 approve_tool 并收掉入口", async () => {
    const stream = captureStream();

    await act(async () => {
      renderDialog({ sessionId: "child-1", role: "researcher" });
    });
    await flush();

    expect(container.querySelector("[data-subagent-session-approval]")).toBeNull();

    await act(async () => {
      stream.emit(approvalEvent(1, "approval-42"));
    });

    const bar = container.querySelector("[data-subagent-session-approval]");
    expect(bar).not.toBeNull();
    // 工具名/风险级别/reason 均来自 approval_requested 载荷。
    expect(bar?.textContent).toContain("shell");
    expect(bar?.textContent).toContain("high");
    expect(bar?.textContent).toContain("writes outside workspace");

    const approve = container.querySelector<HTMLButtonElement>(
      "[data-subagent-session-approval-approve]",
    );
    expect(approve).not.toBeNull();
    await act(async () => {
      approve?.click();
    });
    await flush();

    expect(resolveSessionToolApproval).toHaveBeenCalledTimes(1);
    expect(resolveSessionToolApproval).toHaveBeenCalledWith("child-1", {
      requestId: "approval-42",
      allow: true,
    });
    expect(container.querySelector("[data-subagent-session-approval]")).toBeNull();
    expect(container.querySelector("[data-subagent-session-approval-error]")).toBeNull();
  });

  it("inline 审批：提交失败保留入口并显示原因，approval_resolved 只清除匹配请求", async () => {
    const stream = captureStream();
    resolveSessionToolApproval.mockRejectedValue(new Error("network down"));

    await act(async () => {
      renderDialog({ sessionId: "child-1" });
    });
    await flush();

    await act(async () => {
      stream.emit(approvalEvent(1, "approval-42"));
    });

    const reject = container.querySelector<HTMLButtonElement>(
      "[data-subagent-session-approval-reject]",
    );
    expect(reject).not.toBeNull();
    await act(async () => {
      reject?.click();
    });
    await flush();

    expect(resolveSessionToolApproval).toHaveBeenCalledWith("child-1", {
      requestId: "approval-42",
      allow: false,
    });
    // 失败（网络/网关拒绝）时入口不消失，避免审批被静默丢弃。
    expect(container.querySelector("[data-subagent-session-approval]")).not.toBeNull();
    expect(
      container.querySelector("[data-subagent-session-approval-error]")?.textContent,
    ).toContain("network down");

    // 迟到的其它请求决议不应误清当前入口。
    await act(async () => {
      stream.emit(approvalResolvedEvent(2, "approval-other"));
    });
    expect(container.querySelector("[data-subagent-session-approval]")).not.toBeNull();

    // 匹配的决议（durable 事件）收掉入口与残留错误。
    await act(async () => {
      stream.emit(approvalResolvedEvent(3, "approval-42"));
    });
    expect(container.querySelector("[data-subagent-session-approval]")).toBeNull();
  });

  it("Esc 关闭对话框", async () => {
    const onClose = vi.fn();
    await act(async () => {
      renderDialog({ sessionId: "child-1" }, onClose);
    });
    await flush();

    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });
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
    streamSessionRuntime.mockImplementation(
      (_sessionId: string, handlers: { signal?: AbortSignal }) =>
        new Promise<void>((resolve) => {
          handlers.signal?.addEventListener("abort", () => resolve(), { once: true });
        }),
    );
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
