import { act } from "react";
import { vi } from "vitest";

import type { TrajectoryItem } from "@/lib/trajectory/types";
import type { SessionRuntimeEvent } from "@/types/runtime";

export const fetchSessionRuntimeEvents = vi.fn();
export const streamSessionRuntime = vi.fn();
export const resolveSessionToolApproval = vi.fn();

export type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

// jsdom 无 ResizeObserver：stub 并提供容器高度，让 TrajectoryView 的虚拟滚动
// 窗口化生效（否则 clientHeight=0 → 不渲染任何行）。
export function stubResizeObserver() {
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
}

// 默认的实时流保持挂起，直到 signal abort。
export function hangStreamUntilAbort(
  _sessionId: string,
  handlers: { signal?: AbortSignal },
): Promise<void> {
  return new Promise<void>((resolve) => {
    handlers.signal?.addEventListener("abort", () => resolve(), { once: true });
  });
}

export function resetDialogRuntimeMocks() {
  fetchSessionRuntimeEvents.mockReset();
  streamSessionRuntime.mockReset();
  resolveSessionToolApproval.mockReset();
  resolveSessionToolApproval.mockResolvedValue({ ok: true });
  fetchSessionRuntimeEvents.mockResolvedValue({ events: [], count: 0, latest_seq: 0 });
  streamSessionRuntime.mockImplementation(hangStreamUntilAbort);
}

export function subagentItem(payload: Record<string, unknown>): TrajectoryItem {
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

export function chatEvent(seq: number, content: string): SessionRuntimeEvent {
  return {
    type: "chat.sse.chunk",
    session_id: "child-1",
    payload: { type: "text", content, seq },
    timestamp: "2026-09-13T00:00:00Z",
  };
}

export function approvalEvent(
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

export function approvalResolvedEvent(seq: number, requestId: string): SessionRuntimeEvent {
  return {
    type: "approval_resolved",
    session_id: "child-1",
    timestamp: "2026-09-13T00:00:03Z",
    payload: { seq, request_id: requestId, allowed: false, tool_name: "shell" },
  } as SessionRuntimeEvent;
}

export function captureStream() {
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

export function flush() {
  return act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}
