import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createStreamingFrameScheduler } from "./streaming-frame";

/** 隐藏标签页判定依赖 document.visibilityState（jsdom 可覆写）。 */
function setVisibility(state: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => state,
  });
}

describe("createStreamingFrameScheduler（P0-2 惰性通知语义）", () => {
  let rafCallbacks: Array<() => void>;
  let rafMock: ReturnType<typeof vi.fn>;
  let cafMock: ReturnType<typeof vi.fn>;
  let render: ReturnType<typeof vi.fn<(options?: { force?: boolean }) => void>>;

  beforeEach(() => {
    vi.useFakeTimers();
    setVisibility("visible");
    rafCallbacks = [];
    rafMock = vi.fn((callback: () => void) => {
      rafCallbacks.push(callback);
      return rafCallbacks.length;
    });
    cafMock = vi.fn(() => {});
    window.requestAnimationFrame = rafMock as unknown as typeof window.requestAnimationFrame;
    window.cancelAnimationFrame = cafMock as unknown as typeof window.cancelAnimationFrame;
    render = vi.fn<(options?: { force?: boolean }) => void>();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    setVisibility("visible");
  });

  it("可见标签页：schedule 经最小间隔对齐后按帧提交一次", () => {
    const scheduler = createStreamingFrameScheduler(render);
    scheduler.schedule();
    vi.advanceTimersByTime(120);
    expect(rafCallbacks).toHaveLength(1);
    rafCallbacks.shift()!();
    expect(render).toHaveBeenCalledTimes(1);
    expect(render.mock.calls[0][0]).toEqual({ force: false });
  });

  it("隐藏标签页零工作：不排帧/定时器，恢复可见一次冲刷（P0-2）", () => {
    const scheduler = createStreamingFrameScheduler(render);
    scheduler.attachVisibilityListener();
    setVisibility("hidden");
    scheduler.schedule();
    scheduler.schedule();
    vi.advanceTimersByTime(1000);
    expect(rafMock).not.toHaveBeenCalled();
    expect(render).not.toHaveBeenCalled();

    setVisibility("visible");
    document.dispatchEvent(new Event("visibilitychange"));
    expect(render).toHaveBeenCalledTimes(1);
    expect(render.mock.calls[0][0]).toEqual({ force: true });
    scheduler.detachVisibilityListener();
  });

  it("隐藏期间 flush 只记脏，恢复可见时兑现（P0-2）", () => {
    const scheduler = createStreamingFrameScheduler(render);
    scheduler.attachVisibilityListener();
    setVisibility("hidden");
    scheduler.flush();
    expect(render).not.toHaveBeenCalled();

    setVisibility("visible");
    document.dispatchEvent(new Event("visibilitychange"));
    expect(render).toHaveBeenCalledTimes(1);
    expect(render.mock.calls[0][0]).toEqual({ force: true });
    scheduler.detachVisibilityListener();
  });

  it("无脏位不空提交：flush 后迟到的帧回调不再提交", () => {
    const scheduler = createStreamingFrameScheduler(render);
    scheduler.schedule();
    vi.advanceTimersByTime(120);
    const frameCallback = rafCallbacks.shift()!;

    scheduler.flush();
    expect(render).toHaveBeenCalledTimes(1);

    frameCallback();
    expect(render).toHaveBeenCalledTimes(1);
  });
});
