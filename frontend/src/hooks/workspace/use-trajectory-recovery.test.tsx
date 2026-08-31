// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { fetchSessionRuntimeEvents } from "@/api/runtime/sessions";
import {
  createTrajectoryStore,
  type TrajectoryStore,
} from "@/hooks/workspace/use-trajectory-snapshot";

import { useTrajectoryRecovery } from "./use-trajectory-recovery";

vi.mock("@/api/runtime/sessions", () => ({
  fetchSessionRuntimeEvents: vi.fn(),
}));

const mockFetch = vi.mocked(fetchSessionRuntimeEvents);

function chatSseEvent(kind: string, seq: number, extra: Record<string, unknown> = {}) {
  return {
    type: `chat.sse.${kind}`,
    timestamp: "2026-08-16T00:00:00Z",
    payload: { ...extra, seq },
  };
}

const TOOL_START_PAYLOAD = {
  type: "tool_start",
  index: 0,
  status: "started",
  tool: { id: "tool-1", name: "web_search", args: { query: "capital" } },
  tool_call: { id: "tool-1", name: "web_search", args: { query: "capital" } },
  delta: { id: "tool-1" },
  metadata: { name: "web_search" },
};

function Harness({
  store,
  sessionId,
}: {
  store: TrajectoryStore;
  sessionId: string | undefined;
}) {
  useTrajectoryRecovery({ store, sessionId });
  return null;
}

describe("useTrajectoryRecovery", () => {
  let container: HTMLDivElement;
  let root: Root;
  let store: TrajectoryStore;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    store = createTrajectoryStore();
    mockFetch.mockReset();
  });

  afterEach(() => {
    act(() => {
      root.unmount();
      store.dispose();
    });
    container.remove();
  });

  function render(sessionId: string | undefined) {
    act(() => {
      root.render(<Harness store={store} sessionId={sessionId} />);
    });
  }

  it("恢复事件按序重放进 store（chat.sse.* 转换）", async () => {
    mockFetch.mockResolvedValue({
      events: [
        chatSseEvent("meta", 1, { kind: "chat", status: "started" }),
        chatSseEvent("tool_start", 2, TOOL_START_PAYLOAD),
        chatSseEvent("done", 3, { content: "done" }),
      ],
      count: 3,
      latest_seq: 3,
    });

    render("session-1");

    await vi.waitFor(() => {
      expect(store.getSnapshot().items.length).toBeGreaterThan(0);
    });
    expect(mockFetch).toHaveBeenCalledWith("session-1", { after: 0, limit: 500 });
    const items = store.getSnapshot().items;
    expect(items.some((item) => item.head.kind === "tool")).toBe(true);
  });

  it("同一会话成功恢复后不重复拉取（防重）", async () => {
    mockFetch.mockResolvedValue({
      events: [
        chatSseEvent("meta", 1, { kind: "chat", status: "started" }),
        chatSseEvent("tool_start", 2, TOOL_START_PAYLOAD),
      ],
      count: 2,
      latest_seq: 1,
    });

    render("session-1");
    await vi.waitFor(() => {
      expect(store.getSnapshot().items.length).toBeGreaterThan(0);
    });

    // selectedThread 经 undefined 往返（reload 竞态）：防重命中，不重复拉取。
    render(undefined);
    render("session-1");
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("取消的恢复不标记：sessionId 往返后重新拉取", async () => {
    // 永挂起的 fetch（既不 resolve 也不 reject）；后续 mockResolvedValue
    // 替换实现后，旧 pending promise 随取消的恢复被丢弃。
    mockFetch.mockImplementation(() => new Promise(() => {}));

    render("session-1"); // fetch pending
    render(undefined); // cleanup -> cancelled

    mockFetch.mockResolvedValue({
      events: [
        chatSseEvent("meta", 1, { kind: "chat", status: "started" }),
        chatSseEvent("tool_start", 2, TOOL_START_PAYLOAD),
      ],
      count: 2,
      latest_seq: 2,
    });
    render("session-1"); // 重新恢复

    await vi.waitFor(() => {
      expect(store.getSnapshot().items.length).toBeGreaterThan(0);
    });
    console.log("DBG calls:", mockFetch.mock.calls.length, "sessionIds:", mockFetch.mock.calls.map(c=>c[0])); expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it("无 sessionId 时不拉取", () => {
    render(undefined);
    expect(mockFetch).not.toHaveBeenCalled();
  });

  it("拉取失败时通过 onError 上报，不再静默", async () => {
    const errors: string[] = [];
    mockFetch.mockRejectedValue(new Error("runtime request failed with status 404"));

    function HarnessWithError({
      store,
      sessionId,
    }: {
      store: TrajectoryStore;
      sessionId: string | undefined;
    }) {
      useTrajectoryRecovery({
        store,
        sessionId,
        onError: (failedSessionId, m) => errors.push(`${failedSessionId}:${m}`),
      });
      return null;
    }
    const harness = (
      <HarnessWithError store={store} sessionId="broken-session" />
    );
    act(() => {
      root.render(harness);
    });

    await vi.waitFor(() => {
      expect(errors.length).toBeGreaterThan(0);
    });
    // 回调携带发起恢复的会话 id 与错误文案。
    expect(errors[0]).toBe("broken-session:runtime request failed with status 404");
    // 失败后不固化：切到新会话仍应尝试恢复（不污染防重游标）。
    mockFetch.mockResolvedValue({
      events: [
        chatSseEvent("meta", 1, { kind: "chat", status: "started" }),
        chatSseEvent("tool_start", 2, TOOL_START_PAYLOAD),
      ],
      count: 2,
      latest_seq: 2,
    });
    act(() => {
      root.render(<HarnessWithError store={store} sessionId="broken-session-2" />);
    });
    await vi.waitFor(() => {
      expect(store.getSnapshot().items.length).toBeGreaterThan(0);
    });
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it("已取消的恢复（会话切换）失败时不再上报 onError", async () => {
    const rejectors: Array<(e: Error) => void> = [];
    mockFetch.mockImplementation(
      () =>
        new Promise((_, reject) => {
          rejectors.push(reject);
        }),
    );

    const errors: string[] = [];
    function HarnessSwitch({
      store: s,
      sessionId,
    }: {
      store: TrajectoryStore;
      sessionId: string | undefined;
    }) {
      useTrajectoryRecovery({
        store: s,
        sessionId,
        onError: (sid, m) => errors.push(`${sid}:${m}`),
      });
      return null;
    }

    act(() => {
      root.render(<HarnessSwitch store={store} sessionId="session-a" />);
    });
    // 等 fetch 挂起后切换会话 → 旧恢复被取消。
    await vi.waitFor(() => {
      expect(mockFetch).toHaveBeenCalledTimes(1);
    });
    act(() => {
      root.render(<HarnessSwitch store={store} sessionId="session-b" />);
    });
    await vi.waitFor(() => {
      expect(rejectors.length).toBe(2);
    });
    // 迟到的失败（session-a 的请求现在才 reject）不得触发 onError。
    rejectors[0](new Error("runtime request failed with status 500"));

    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(errors).toHaveLength(0);
  });
});
