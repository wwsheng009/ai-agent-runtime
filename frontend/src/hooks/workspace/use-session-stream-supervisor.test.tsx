// @vitest-environment jsdom
//
// 回归：dev 下 React StrictMode 的「挂载 → 卸载 → 挂载」不得把注册表单例打死。
//
// 缺陷现场（live 实测复现）：`use-session-stream-supervisor` 曾在卸载清理里直接
// `disposeSessionRuntimeRegistry()`；StrictMode 的模拟卸载让单例被 dispose，而第二次
// 挂载复用的仍是同一个已 disposed 实例（`useState` 惰性初始化只执行一次），
// `registry.ensure()` 在 disposed 后静默 no-op —— 后台订阅从此不再建立：侧栏没有
// 「运行中」投影，也不会有任何 `/runtime` 轮询。

import { StrictMode, act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/runtime-api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/runtime-api")>();
  return {
    ...actual,
    getSessionRuntimeState: vi.fn(),
    streamSessionRuntime: vi.fn(),
  };
});

import { getSessionRuntimeState, streamSessionRuntime } from "@/lib/runtime-api";

import {
  disposeSessionRuntimeRegistry,
  getSessionRuntimeRegistry,
} from "./use-session-runtime-registry";
import { useSessionStreamSupervisor } from "./use-session-stream-supervisor";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const mockGetState = vi.mocked(getSessionRuntimeState);
const mockStream = vi.mocked(streamSessionRuntime);

/** 传输层永不结算：本用例只验证注册表生命周期，不发真实请求。 */
function neverSettles(): Promise<never> {
  return new Promise<never>(() => {});
}

function Harness() {
  useSessionStreamSupervisor({
    enabled: true,
    selectedSessionId: null,
    candidates: [
      { sessionId: "session-a", updatedAt: new Date().toISOString() },
    ],
  });
  return null;
}

describe("useSessionStreamSupervisor（StrictMode 下的注册表生命周期）", () => {
  let container: HTMLDivElement;
  let root: Root;
  let unmounted: boolean;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    mockGetState.mockReset();
    mockStream.mockReset();
    mockGetState.mockImplementation(neverSettles);
    mockStream.mockImplementation(neverSettles);
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    unmounted = false;
  });

  afterEach(() => {
    if (!unmounted) {
      act(() => root.unmount());
    }
    container.remove();
    disposeSessionRuntimeRegistry();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderStrict() {
    act(() => {
      root.render(
        <StrictMode>
          <Harness />
        </StrictMode>,
      );
    });
  }

  it("StrictMode 双挂载后订阅仍然成立（单例未被模拟卸载打死）", () => {
    renderStrict();

    const registry = getSessionRuntimeRegistry();
    // ensure() 在 disposed 实例上静默 no-op → 旧实现下这里是 undefined / 空表。
    expect(registry.snapshot("session-a")).toBeDefined();
    expect(registry.entriesSnapshot().size).toBe(1);
  });

  it("真正卸载（使用者归零）后释放单例，重挂载得到新实例", async () => {
    renderStrict();
    const mounted = getSessionRuntimeRegistry();
    expect(mounted.snapshot("session-a")).toBeDefined();

    act(() => root.unmount());
    unmounted = true;
    // 释放延迟一个宏任务：StrictMode 的同步重挂载会在回调前重新 acquire。
    await new Promise<void>((resolve) => {
      setTimeout(resolve, 0);
    });

    const next = getSessionRuntimeRegistry();
    expect(next).not.toBe(mounted);
    expect(next.entriesSnapshot().size).toBe(0);
  });
});
