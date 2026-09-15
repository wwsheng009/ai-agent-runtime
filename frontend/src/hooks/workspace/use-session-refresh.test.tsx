// @vitest-environment jsdom

// 顶栏「刷新当前会话」控制器单测：三份数据一次重拉 / 回合进行中跳过历史 /
// 在途守卫（连点只跑一次）与在途标记。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  useSessionRefresh,
  type SessionRefreshController,
  type SessionRefreshOptions,
} from "./use-session-refresh";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

function Harness({
  onSnapshot,
  options,
}: {
  onSnapshot: (snapshot: SessionRefreshController) => void;
  options: SessionRefreshOptions;
}) {
  const snapshot = useSessionRefresh(options);
  onSnapshot(snapshot);
  return null;
}

describe("useSessionRefresh", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  function renderHook(options: SessionRefreshOptions) {
    let latest: SessionRefreshController | null = null;
    act(() => {
      root.render(
        <Harness
          options={options}
          onSnapshot={(next) => {
            latest = next;
          }}
        />,
      );
    });
    return {
      get current(): SessionRefreshController {
        if (!latest) {
          throw new Error("hook not rendered");
        }
        return latest;
      },
    };
  }

  it("reloads runtime state, the session list projection and the authoritative history", async () => {
    const refreshRuntimeState = vi.fn();
    const refreshRuntimeSessions = vi.fn();
    const recoverHistory = vi.fn(async () => true);
    const hook = renderHook({
      recoverHistory,
      refreshRuntimeSessions,
      refreshRuntimeState,
      responding: false,
    });

    await act(async () => {
      await hook.current.refreshSession();
    });

    expect(refreshRuntimeState).toHaveBeenCalledTimes(1);
    expect(refreshRuntimeSessions).toHaveBeenCalledTimes(1);
    expect(recoverHistory).toHaveBeenCalledTimes(1);
    expect(hook.current.sessionRefreshing).toBe(false);
  });

  it("skips the history reload while this session is streaming a reply", async () => {
    const recoverHistory = vi.fn(async () => true);
    const hook = renderHook({ recoverHistory, responding: true });

    await act(async () => {
      await hook.current.refreshSession();
    });

    // 回合进行中：历史快照会覆盖在途流式消息，只保留与回合无关的刷新。
    expect(recoverHistory).not.toHaveBeenCalled();
  });

  it("ignores re-entrant clicks while a refresh is in flight", async () => {
    const pending = deferred<boolean>();
    const recoverHistory = vi.fn(() => pending.promise);
    const hook = renderHook({ recoverHistory, responding: false });

    let inFlight!: Promise<void>;
    act(() => {
      inFlight = hook.current.refreshSession();
    });
    expect(hook.current.sessionRefreshing).toBe(true);

    // 第二次点击（在途标记未清）不应再发起一次权威历史拉取。
    await act(async () => {
      await hook.current.refreshSession();
    });
    expect(recoverHistory).toHaveBeenCalledTimes(1);

    await act(async () => {
      pending.resolve(true);
      await inFlight;
    });
    expect(hook.current.sessionRefreshing).toBe(false);
  });

  it("clears the pending marker when the history reload fails", async () => {
    const recoverHistory = vi.fn(async () => false);
    const hook = renderHook({ recoverHistory, responding: false });

    await act(async () => {
      await hook.current.refreshSession();
    });

    expect(hook.current.sessionRefreshing).toBe(false);
  });

  it("works without optional entries (preview thread without a session)", async () => {
    const hook = renderHook({ responding: false });

    await act(async () => {
      await hook.current.refreshSession();
    });

    expect(hook.current.sessionRefreshing).toBe(false);
  });
});
