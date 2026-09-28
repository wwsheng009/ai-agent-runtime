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

  it("P1-2 三帧门：animation-frame 档 120ms 下界后连跨三次绘制机会才提交一次", () => {
    const scheduler = createStreamingFrameScheduler(render);
    scheduler.schedule({ pace: "animation-frame" });
    // 120ms 频率下界仍生效（提交:事件 远小于 1:1 验收）：先等满再排帧。
    expect(rafCallbacks).toHaveLength(0);
    vi.advanceTimersByTime(120);
    expect(rafCallbacks).toHaveLength(1);

    rafCallbacks.shift()!();
    expect(render).not.toHaveBeenCalled(); // 第 1 次绘制机会：再排
    expect(rafCallbacks).toHaveLength(1);

    rafCallbacks.shift()!();
    expect(render).not.toHaveBeenCalled(); // 第 2 次绘制机会：再排
    expect(rafCallbacks).toHaveLength(1);

    rafCallbacks.shift()!();
    expect(render).toHaveBeenCalledTimes(1); // 第 3 次绘制机会：提交
    expect(render.mock.calls[0][0]).toEqual({ force: false });
  });

  it("P1-2 三帧门去重：门内重复调度并入同一次提交", () => {
    const scheduler = createStreamingFrameScheduler(render);
    scheduler.schedule({ pace: "animation-frame" });
    scheduler.schedule({ pace: "animation-frame" });
    scheduler.schedule(); // structural 档挂起期间也并入（脏位合一）
    expect(rafCallbacks).toHaveLength(0);
    vi.advanceTimersByTime(120);
    expect(rafCallbacks).toHaveLength(1);

    rafCallbacks.shift()!();
    rafCallbacks.shift()!();
    rafCallbacks.shift()!();
    expect(render).toHaveBeenCalledTimes(1);
  });

  it("P1-2 immediate：flush 取消挂起帧门并立即提交，迟到帧不再提交", () => {
    const scheduler = createStreamingFrameScheduler(render);
    scheduler.schedule({ pace: "animation-frame" });
    vi.advanceTimersByTime(120);
    rafCallbacks.shift()!(); // 第 1 次绘制机会：挂起帧门
    expect(rafCallbacks).toHaveLength(1);
    expect(render).not.toHaveBeenCalled();

    // 持久事件（immediate）：取消挂起帧，立即提交。
    scheduler.flush();
    expect(render).toHaveBeenCalledTimes(1);
    expect(render.mock.calls[0][0]).toEqual({ force: true });

    // 已取消的帧门回调即使迟到也不再提交（脏位已被 flush 清掉）。
    while (rafCallbacks.length > 0) {
      rafCallbacks.shift()!();
    }
    expect(render).toHaveBeenCalledTimes(1);
  });

  it("P1-2 隐藏标签页：三帧门同样零工作，恢复可见一次冲刷", () => {
    const scheduler = createStreamingFrameScheduler(render);
    scheduler.attachVisibilityListener();
    setVisibility("hidden");
    scheduler.schedule({ pace: "animation-frame" });
    vi.advanceTimersByTime(1000);
    expect(rafMock).not.toHaveBeenCalled();
    expect(render).not.toHaveBeenCalled();

    setVisibility("visible");
    document.dispatchEvent(new Event("visibilitychange"));
    expect(render).toHaveBeenCalledTimes(1);
    expect(render.mock.calls[0][0]).toEqual({ force: true });
    scheduler.detachVisibilityListener();
  });
});
