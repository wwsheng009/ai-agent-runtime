// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { RuntimeSessionSnapshot } from "@/lib/runtime-api";

vi.mock("@/lib/runtime-api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/runtime-api")>();
  return {
    ...actual,
    getSessionRuntimeState: vi.fn(),
  };
});

import { getSessionRuntimeState } from "@/lib/runtime-api";

import { useSessionRuntimeState } from "./use-session-runtime-state";

const mockGetState = vi.mocked(getSessionRuntimeState);

type HookSnapshot = ReturnType<typeof useSessionRuntimeState>;
type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function snapshot(sessionId: string, status = "running"): RuntimeSessionSnapshot {
  return {
    state: {
      sessionId,
      status,
      pendingApproval: null,
      pendingQuestion: null,
      headOffset: 0,
      activeJobIds: [],
    },
    activeTurn: null,
  };
}

function notFoundError(): Error {
  return Object.assign(new Error("session not found"), { status: 404 });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function Harness({
  sessionId,
  onSnapshot,
}: {
  sessionId?: string;
  onSnapshot: (snapshot: HookSnapshot) => void;
}) {
  const snapshot = useSessionRuntimeState(sessionId);
  onSnapshot(snapshot);
  return null;
}

describe("useSessionRuntimeState", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    mockGetState.mockReset();
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderHook(sessionId: string | undefined) {
    let latest: HookSnapshot | null = null;
    act(() => {
      root.render(
        <Harness
          sessionId={sessionId}
          onSnapshot={(next) => {
            latest = next;
          }}
        />,
      );
    });
    return {
      get current(): HookSnapshot {
        if (!latest) {
          throw new Error("hook not rendered");
        }
        return latest;
      },
      update(next: { sessionId?: string }) {
        act(() => {
          root.render(
            <Harness
              sessionId={next.sessionId}
              onSnapshot={(value) => {
                latest = value;
              }}
            />,
          );
        });
      },
    };
  }

  async function flush() {
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  it("空 sessionId 不发请求且保持空态", async () => {
    const hook = renderHook("   ");

    await flush();

    expect(mockGetState).not.toHaveBeenCalled();
    expect(hook.current.state).toBeNull();
    expect(hook.current.snapshot).toBeNull();
    expect(hook.current.error).toBeNull();
  });

  it("拉取成功后暴露 snapshot 与 state", async () => {
    mockGetState.mockResolvedValue(snapshot("sess-1"));
    const hook = renderHook("sess-1");

    await flush();

    expect(mockGetState).toHaveBeenCalledWith("sess-1", {
      signal: expect.any(AbortSignal),
    });
    expect(hook.current.snapshot?.state?.sessionId).toBe("sess-1");
    expect(hook.current.state?.status).toBe("running");
    expect(hook.current.error).toBeNull();
  });

  it("404 视为无 runtime state：按空态处理且不报错", async () => {
    mockGetState.mockRejectedValue(notFoundError());
    const hook = renderHook("sess-404");

    await flush();

    expect(hook.current.state).toBeNull();
    expect(hook.current.error).toBeNull();
  });

  it("显式空快照（200 + state: null）按空态处理且不报错", async () => {
    mockGetState.mockResolvedValue(null);
    const hook = renderHook("sess-empty");

    await flush();

    expect(hook.current.snapshot).toBeNull();
    expect(hook.current.state).toBeNull();
    expect(hook.current.error).toBeNull();
  });

  it("刷新后返回显式空快照：清掉陈旧快照而不是沿用上一轮状态", async () => {
    mockGetState.mockResolvedValueOnce(snapshot("sess-1", "waiting_approval"));
    const hook = renderHook("sess-1");

    await flush();
    expect(hook.current.state?.status).toBe("waiting_approval");

    mockGetState.mockResolvedValueOnce(null);
    act(() => {
      hook.current.refresh();
    });
    await flush();

    expect(hook.current.snapshot).toBeNull();
    expect(hook.current.state).toBeNull();
    expect(hook.current.error).toBeNull();
  });

  it("其他错误暴露 error 且 state 归空", async () => {
    const failure = new Error("boom");
    mockGetState.mockRejectedValue(failure);
    const hook = renderHook("sess-1");

    await flush();

    expect(hook.current.error).toBe(failure);
    expect(hook.current.state).toBeNull();
  });

  it("会话切换：陈旧会话的迟到结果不串台", async () => {
    const stale = deferred<RuntimeSessionSnapshot>();
    mockGetState.mockImplementationOnce(() => stale.promise);
    mockGetState.mockResolvedValueOnce(snapshot("sess-2", "idle"));
    const hook = renderHook("sess-1");

    await flush();
    hook.update({ sessionId: "sess-2" });
    await flush();

    expect(hook.current.state?.sessionId).toBe("sess-2");

    await act(async () => {
      stale.resolve(snapshot("sess-1"));
      await stale.promise;
      await Promise.resolve();
    });

    expect(hook.current.state?.sessionId).toBe("sess-2");
    expect(hook.current.error).toBeNull();
  });

  it("refresh 重新拉取（重连后可主动刷新）", async () => {
    mockGetState.mockResolvedValue(snapshot("sess-1", "running"));
    const hook = renderHook("sess-1");

    await flush();
    expect(mockGetState).toHaveBeenCalledTimes(1);

    mockGetState.mockResolvedValueOnce(snapshot("sess-1", "waiting_approval"));
    act(() => {
      hook.current.refresh();
    });
    await flush();

    expect(mockGetState).toHaveBeenCalledTimes(2);
    expect(hook.current.state?.status).toBe("waiting_approval");
  });
});
