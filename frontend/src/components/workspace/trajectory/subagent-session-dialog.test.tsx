// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  SubagentSessionDialog,
} from "@/components/workspace/trajectory/subagent-session-dialog";
import {
  approvalEvent,
  approvalResolvedEvent,
  captureStream,
  chatEvent,
  flush,
  fetchSessionRuntimeEvents,
  resetDialogRuntimeMocks,
  resolveSessionToolApproval,
  streamSessionRuntime,
  stubResizeObserver,
  type ReactActEnvironmentGlobal,
} from "@/components/workspace/trajectory/subagent-session-dialog.test-helpers";
import type { SubagentSessionTarget } from "@/components/workspace/trajectory/subagent-session-target";
import type { SessionRuntimeEvent } from "@/types/runtime";

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

describe("SubagentSessionDialog", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    resetDialogRuntimeMocks();
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
