// @vitest-environment jsdom

// P2-1A：会话统计状态机单测（拉取口径 / 刷新 / 降级分类 / 过期响应与取消）。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { RuntimeApiError } from "@/api/runtime/shared";
import type { RuntimeSessionStats, RuntimeSessionStatsResponse } from "@/types/runtime";

const { fetchRuntimeSessionStatsMock } = vi.hoisted(() => ({
  fetchRuntimeSessionStatsMock: vi.fn(),
}));

vi.mock("@/api/runtime/session-stats", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("@/api/runtime/session-stats")>();
  return { ...actual, fetchRuntimeSessionStats: fetchRuntimeSessionStatsMock };
});

import { useSessionStats, type UseSessionStatsResult } from "./use-session-stats";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function stats(overrides: Partial<RuntimeSessionStats> = {}): RuntimeSessionStats {
  return {
    total: 3,
    active: 1,
    idle: 1,
    closed: 1,
    archived: 0,
    totalMessages: 9,
    tags: {},
    ...overrides,
  };
}

function response(
  userId: string,
  value: RuntimeSessionStats,
): RuntimeSessionStatsResponse {
  return { user_id: userId, stats: value };
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
  userId,
  onSnapshot,
}: {
  userId?: string;
  onSnapshot: (snapshot: UseSessionStatsResult) => void;
}) {
  const snapshot = useSessionStats(userId);
  onSnapshot(snapshot);
  return null;
}

describe("useSessionStats", () => {
  let container: HTMLDivElement;
  let root: Root;
  let signals: Array<AbortSignal | undefined>;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    signals = [];
    fetchRuntimeSessionStatsMock.mockReset();
    fetchRuntimeSessionStatsMock.mockImplementation(
      async (_userId?: string, options?: { signal?: AbortSignal }) => {
        signals.push(options?.signal);
        return response("default", stats());
      },
    );
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  function renderHook(userId?: string) {
    let latest: UseSessionStatsResult | null = null;
    act(() => {
      root.render(
        <Harness
          userId={userId}
          onSnapshot={(next) => {
            latest = next;
          }}
        />,
      );
    });
    return {
      get current(): UseSessionStatsResult {
        if (!latest) {
          throw new Error("hook not rendered");
        }
        return latest;
      },
      update(nextUserId?: string) {
        act(() => {
          root.render(
            <Harness
              userId={nextUserId}
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

  it("挂载即拉取：loading → ready，userId 去空白后透传", async () => {
    fetchRuntimeSessionStatsMock.mockResolvedValue(response("alice", stats()));

    const hook = renderHook("  alice  ");

    expect(hook.current.status).toBe("loading");
    expect(hook.current.stats).toBeNull();

    await flush();

    expect(fetchRuntimeSessionStatsMock).toHaveBeenCalledTimes(1);
    expect(fetchRuntimeSessionStatsMock.mock.calls[0][0]).toBe("alice");
    expect(hook.current).toMatchObject({
      status: "ready",
      unavailable: false,
      error: null,
    });
    expect(hook.current.stats?.totalMessages).toBe(9);
  });

  it("userId 为空串时不传 user（交给服务端默认用户）", async () => {
    renderHook("   ");
    await flush();

    expect(fetchRuntimeSessionStatsMock.mock.calls[0][0]).toBeUndefined();
  });

  it("refresh() 重新拉取并覆盖统计", async () => {
    fetchRuntimeSessionStatsMock
      .mockResolvedValueOnce(response("alice", stats({ total: 1 })))
      .mockResolvedValueOnce(response("alice", stats({ total: 5 })));

    const hook = renderHook("alice");
    await flush();
    expect(hook.current.stats?.total).toBe(1);

    act(() => hook.current.refresh());
    await flush();

    expect(fetchRuntimeSessionStatsMock).toHaveBeenCalledTimes(2);
    expect(hook.current).toMatchObject({
      status: "ready",
      error: null,
      unavailable: false,
    });
    expect(hook.current.stats?.total).toBe(5);
  });

  it("HTTP 503 归『统计不可用』，并保留上一次成功统计", async () => {
    const unavailableError = new RuntimeApiError(503, null);
    fetchRuntimeSessionStatsMock
      .mockResolvedValueOnce(response("alice", stats({ total: 2 })))
      .mockRejectedValueOnce(unavailableError);

    const hook = renderHook("alice");
    await flush();

    act(() => hook.current.refresh());
    await flush();

    expect(hook.current).toMatchObject({ status: "error", unavailable: true });
    expect(hook.current.error).toBe(unavailableError);
    expect(hook.current.stats?.total).toBe(2);
  });

  it("真实失败（500）按错误呈现，unavailable 保持 false", async () => {
    const failure = new RuntimeApiError(500, null);
    fetchRuntimeSessionStatsMock.mockRejectedValue(failure);

    const hook = renderHook("alice");
    await flush();

    expect(hook.current).toMatchObject({ status: "error", unavailable: false });
    expect(hook.current.error).toBe(failure);
    expect(hook.current.stats).toBeNull();
  });

  it("userId 变化触发重取：中止旧请求并丢弃过期响应", async () => {
    const stale = deferred<RuntimeSessionStatsResponse>();
    fetchRuntimeSessionStatsMock
      .mockImplementationOnce(
        async (_userId?: string, options?: { signal?: AbortSignal }) => {
          signals.push(options?.signal);
          return stale.promise;
        },
      )
      .mockResolvedValueOnce(response("bob", stats({ total: 7 })));

    const hook = renderHook("alice");
    await flush();

    hook.update("bob");
    await flush();

    expect(signals[0]?.aborted).toBe(true);
    expect(fetchRuntimeSessionStatsMock.mock.calls[1][0]).toBe("bob");
    expect(hook.current.stats?.total).toBe(7);

    stale.resolve(response("alice", stats({ total: 99 })));
    await flush();

    expect(hook.current.stats?.total).toBe(7);
    expect(hook.current.status).toBe("ready");
  });

  it("卸载时中止在途请求", async () => {
    const pending = deferred<RuntimeSessionStatsResponse>();
    fetchRuntimeSessionStatsMock.mockImplementationOnce(
      async (_userId?: string, options?: { signal?: AbortSignal }) => {
        signals.push(options?.signal);
        return pending.promise;
      },
    );

    renderHook("alice");
    await flush();

    act(() => root.unmount());

    expect(signals[0]?.aborted).toBe(true);
  });

  it("主动取消（AbortError）不落错误态", async () => {
    fetchRuntimeSessionStatsMock.mockRejectedValue(
      new DOMException("The operation was aborted.", "AbortError"),
    );

    const hook = renderHook("alice");
    await flush();

    expect(hook.current).toMatchObject({
      status: "loading",
      error: null,
      unavailable: false,
    });
  });
});
