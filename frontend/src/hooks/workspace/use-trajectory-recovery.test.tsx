// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  fetchSessionRuntimeEvents,
  getSessionHistory,
} from "@/api/runtime/sessions";
import {
  createTrajectoryStore,
  type TrajectoryStore,
} from "@/hooks/workspace/use-trajectory-snapshot";

import { useTrajectoryRecovery } from "./use-trajectory-recovery";

vi.mock("@/api/runtime/sessions", () => ({
  fetchSessionRuntimeEvents: vi.fn(),
  getSessionHistory: vi.fn(),
}));

const mockFetch = vi.mocked(fetchSessionRuntimeEvents);
const mockHistory = vi.mocked(getSessionHistory);

function runtimeEvent(
  type: string,
  seq: number,
  extra: Record<string, unknown> = {},
) {
  return {
    type,
    timestamp: "2026-09-14T00:00:00Z",
    payload: { seq, ...extra },
  };
}

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
    mockHistory.mockReset();
    // 默认：会话历史为空（健康会话不走兜底；需要兜底的用例各自注入历史）。
    mockHistory.mockResolvedValue({
      session_id: "session-1",
      count: 0,
      history: [],
    });
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

  it("无内容帧的会话回退到会话历史：用户/助手/工具消息都能看到", async () => {
    // 这类会话（aicli 进程内 chat 运行时）只落生命周期事件，消息本体在持久化历史里。
    mockFetch.mockResolvedValue({
      events: [
        runtimeEvent("session_start", 1, { turn_id: "turn-1" }),
        runtimeEvent("session_end", 2, { turn_id: "turn-1" }),
      ],
      count: 2,
      latest_seq: 2,
    });
    mockHistory.mockResolvedValue({
      session_id: "session-history-only",
      count: 3,
      history: [
        {
          role: "user",
          content: "用户问题",
          metadata: { turn_id: "turn-1", message_id: "u1" },
        },
        {
          role: "assistant",
          content: "助手回答",
          metadata: { turn_id: "turn-1", message_id: "a1" },
          tool_calls: [{ id: "call-1", name: "web_search" }],
        },
        {
          role: "tool",
          content: "工具结果",
          tool_call_id: "call-1",
          metadata: { turn_id: "turn-1", tool_name: "web_search" },
        },
      ],
    });

    render("session-history-only");

    await vi.waitFor(() => {
      expect(store.getSnapshot().items.some((item) => item.kind === "user")).toBe(
        true,
      );
    });
    const items = store.getSnapshot().items;
    expect(
      items.some(
        (item) => item.kind === "user" && item.head.kind === "text" && item.head.content === "用户问题",
      ),
    ).toBe(true);
    expect(
      items.some(
        (item) =>
          item.kind === "assistant" &&
          item.head.kind === "text" &&
          item.head.content === "助手回答",
      ),
    ).toBe(true);
    expect(
      items.some((item) => item.kind === "tool" && item.id.includes("call-1")),
    ).toBe(true);
    expect(mockHistory).toHaveBeenCalledTimes(1);
  });

  it("重启后仍能再次查看：消息来自持久化历史，不依赖内存态", async () => {
    mockFetch.mockResolvedValue({
      events: [runtimeEvent("session_start", 1, { turn_id: "turn-1" })],
      count: 1,
      latest_seq: 1,
    });
    mockHistory.mockResolvedValue({
      session_id: "session-restart",
      count: 2,
      history: [
        {
          role: "user",
          content: "重启前的问题",
          metadata: { turn_id: "turn-1", message_id: "u1" },
        },
        {
          role: "assistant",
          content: "重启前的回答",
          metadata: { turn_id: "turn-1", message_id: "a1" },
        },
      ],
    });

    render("session-restart");
    await vi.waitFor(() => {
      expect(store.getSnapshot().items.some((item) => item.kind === "user")).toBe(
        true,
      );
    });

    // 模拟重启：丢弃内存 store 与页面，仅保留持久化数据（mock 仍返回同一份历史）。
    act(() => {
      root.unmount();
      store.dispose();
    });
    store = createTrajectoryStore();
    root = createRoot(container);
    render("session-restart");

    await vi.waitFor(() => {
      expect(store.getSnapshot().items.some((item) => item.kind === "user")).toBe(
        true,
      );
    });
    const contents = store
      .getSnapshot()
      .items.filter((item) => item.head.kind === "text")
      .map((item) => (item.head.kind === "text" ? item.head.content : ""));
    expect(contents).toContain("重启前的问题");
    expect(contents).toContain("重启前的回答");
  });

  it("有内容帧的会话不回退到历史（单一事实源仍是 EventStore）", async () => {
    mockFetch.mockResolvedValue({
      events: [
        chatSseEvent("meta", 1, { kind: "chat", status: "started" }),
        chatSseEvent("chunk", 2, { type: "text", content: "实时回答" }),
      ],
      count: 2,
      latest_seq: 2,
    });

    render("session-healthy");

    await vi.waitFor(() => {
      expect(store.getSnapshot().items.length).toBeGreaterThan(0);
    });
    expect(mockHistory).not.toHaveBeenCalled();
  });
});
