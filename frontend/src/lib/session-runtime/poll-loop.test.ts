// sleepWithSignal 的监听器生命周期测试：长寿命 AbortSignal 上不得累积 abort 闭包。
import { describe, expect, it, vi } from "vitest";

import { sleepWithSignal } from "./poll-loop";

describe("sleepWithSignal", () => {
  it("正常超时后移除 abort 监听器（每次 sleep 不留闭包）", async () => {
    vi.useFakeTimers();
    try {
      const listeners = new Set<() => void>();
      const signal = {
        aborted: false,
        addEventListener: (_type: string, listener: () => void) => {
          listeners.add(listener);
        },
        removeEventListener: (_type: string, listener: () => void) => {
          listeners.delete(listener);
        },
      } as unknown as AbortSignal;

      const pending = sleepWithSignal(10, signal);
      expect(listeners.size).toBe(1);
      await vi.advanceTimersByTimeAsync(10);
      await pending;
      expect(listeners.size).toBe(0);
    } finally {
      vi.useRealTimers();
    }
  });

  it("abort 后立即 resolve 并清掉定时器", async () => {
    vi.useFakeTimers();
    try {
      const controller = new AbortController();
      const pending = sleepWithSignal(60_000, controller.signal);
      controller.abort();
      await pending;
      expect(vi.getTimerCount()).toBe(0);
    } finally {
      vi.useRealTimers();
    }
  });

  it("已 aborted 的 signal 直接返回，不注册监听器", async () => {
    let added = false;
    const signal = {
      aborted: true,
      addEventListener: () => {
        added = true;
      },
      removeEventListener: () => {},
    } as unknown as AbortSignal;
    await sleepWithSignal(10, signal);
    expect(added).toBe(false);
  });
});
