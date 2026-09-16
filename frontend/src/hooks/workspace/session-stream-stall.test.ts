import { describe, expect, it, vi } from "vitest";

import { SseIdleTimeoutError } from "@/api/runtime/sse";
import { createStallGuard } from "@/hooks/workspace/session-stream-stall";

describe("session stream stall guard", () => {
  it("holds reconnecting after an idle timeout until real bytes arrive", () => {
    const guard = createStallGuard();
    const fallback = vi.fn(() => "fallback");

    expect(guard.holdReconnecting).toBe(false);
    expect(guard.noteFailure(new SseIdleTimeoutError(45_000), fallback)).toBe(
      "静默超时：45 秒未收到任何数据（含 keepalive），已按游标重连",
    );
    expect(guard.holdReconnecting).toBe(true);
    expect(fallback).not.toHaveBeenCalled();

    guard.markAlive();
    expect(guard.holdReconnecting).toBe(false);
  });

  it("delegates other failures to the caller and clears the hold", () => {
    const guard = createStallGuard();
    guard.noteFailure(new SseIdleTimeoutError(45_000), () => "idle");
    expect(guard.holdReconnecting).toBe(true);

    const fallback = vi.fn((_error: unknown, message: string) => `wrapped:${message}`);
    expect(guard.noteFailure(new Error("boom"), fallback)).toBe(
      "wrapped:failed to connect runtime stream",
    );
    expect(fallback).toHaveBeenCalledTimes(1);
    // 显式失败（服务端还活着）说明连接层没静默，解除重连保持。
    expect(guard.holdReconnecting).toBe(false);
  });
});
