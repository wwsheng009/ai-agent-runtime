import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  coalesceTrajectoryEvents,
  TrajectoryBatcher,
} from "./stream-batch";
import { makeTrajectoryEvent } from "./trajectory-reducer";
import type { TrajectoryEvent } from "./types";

function event(
  kind: "chunk" | "reasoning" | "tool_start" | "tool_end",
  seq: number,
  content = "",
) {
  return makeTrajectoryEvent(kind, seq, { type: "text", content });
}

/** 隐藏标签页判定依赖 document.visibilityState（jsdom 可覆写）。 */
function setVisibility(state: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => state,
  });
}

describe("coalesceTrajectoryEvents 帧内合并", () => {
  it("同 kind 连续事件合并为一段（保留首尾 seq）", () => {
    const segments = coalesceTrajectoryEvents([
      event("chunk", 1, "A"),
      event("chunk", 2, "B"),
      event("chunk", 3, "C"),
    ]);
    expect(segments).toHaveLength(1);
    expect(segments[0].kind).toBe("chunk");
    expect(segments[0].seqs).toEqual([1, 3]);
    expect(segments[0].payloads).toHaveLength(3);
  });

  it("不同 kind 边界不合并（reasoning→text 边界语义保持）", () => {
    const segments = coalesceTrajectoryEvents([
      event("reasoning", 1, "R"),
      event("chunk", 2, "T"),
      event("reasoning", 3, "R2"),
    ]);
    expect(segments.map((segment) => segment.kind)).toEqual([
      "reasoning",
      "chunk",
      "reasoning",
    ]);
  });

  it("空 delta 事件保留在段内（live reasoning 完成边界）", () => {
    const segments = coalesceTrajectoryEvents([
      event("reasoning", 1, "R1"),
      event("reasoning", 2, ""),
      event("reasoning", 3, "R3"),
    ]);
    expect(segments).toHaveLength(1);
    expect(segments[0].payloads.map((payload) => payload["content"])).toEqual([
      "R1",
      "",
      "R3",
    ]);
  });

  it("空数组返回空段列表", () => {
    expect(coalesceTrajectoryEvents([])).toEqual([]);
  });
});

describe("TrajectoryBatcher rAF 帧内批量 + 后台兜底", () => {
  let rafCallbacks: Array<() => void>;
  let rafMock: ReturnType<typeof vi.fn>;
  let cafMock: ReturnType<typeof vi.fn>;
  let flush: ReturnType<typeof vi.fn<(events: TrajectoryEvent[]) => void>>;

  beforeEach(() => {
    vi.useFakeTimers();
    rafCallbacks = [];
    rafMock = vi.fn((callback: () => void) => {
      rafCallbacks.push(callback);
      return rafCallbacks.length;
    });
    cafMock = vi.fn(() => {});
    window.requestAnimationFrame = rafMock as unknown as typeof window.requestAnimationFrame;
    window.cancelAnimationFrame = cafMock as unknown as typeof window.cancelAnimationFrame;
    flush = vi.fn<(events: TrajectoryEvent[]) => void>();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("push 的事件在下一 rAF 统一批量 flush", () => {
    const batcher = new TrajectoryBatcher({ flush });
    batcher.push(event("chunk", 1, "A"));
    batcher.push(event("chunk", 2, "B"));
    expect(flush).not.toHaveBeenCalled();
    expect(rafCallbacks).toHaveLength(1);

    rafCallbacks.shift()!();
    expect(flush).toHaveBeenCalledTimes(1);
    const batch = flush.mock.calls[0][0] as unknown[];
    expect(batch).toHaveLength(2);
    expect(batch[0]).toMatchObject({ kind: "chunk", seq: 1 });
    expect(batch[1]).toMatchObject({ kind: "chunk", seq: 2 });
    batcher.dispose();
  });

  it("后台标签页 rAF 不触发时 setTimeout 兜底（100ms）", () => {
    const batcher = new TrajectoryBatcher({ flush });
    batcher.push(event("chunk", 1, "A"));
    expect(flush).not.toHaveBeenCalled();

    vi.advanceTimersByTime(99);
    expect(flush).not.toHaveBeenCalled();

    vi.advanceTimersByTime(1);
    expect(flush).toHaveBeenCalledTimes(1);
    const batch = flush.mock.calls[0][0] as unknown[];
    expect(batch).toHaveLength(1);
    // 兜底触发后帧被取消。
    expect(cafMock).toHaveBeenCalled();
    batcher.dispose();
  });

  it("页面恢复可见：flushNow 立即冲刷并清空挂起", () => {
    const batcher = new TrajectoryBatcher({ flush });
    batcher.push(event("chunk", 1, "A"));
    batcher.flushNow();
    expect(flush).toHaveBeenCalledTimes(1);

    // 清空后再 flush 不重复调用。
    batcher.flushNow();
    expect(flush).toHaveBeenCalledTimes(1);
    batcher.dispose();
  });

  it("dispose 冲刷残余并取消挂起的帧/定时器", () => {
    const batcher = new TrajectoryBatcher({ flush });
    batcher.push(event("chunk", 1, "A"));
    batcher.dispose();
    expect(flush).toHaveBeenCalledTimes(1);
    expect(cafMock).toHaveBeenCalled();
    const batch = flush.mock.calls[0][0] as unknown[];
    expect(batch).toHaveLength(1);
  });

  it("隐藏标签页零工作：不排帧/定时器，恢复可见一次冲刷（P0-2）", () => {
    const onFlushed = vi.fn();
    const batcher = new TrajectoryBatcher({ flush, onFlushed });
    batcher.attachVisibilityListener();
    setVisibility("hidden");
    try {
      batcher.push(event("chunk", 1, "A"));
      batcher.push(event("chunk", 2, "B"));
      expect(flush).not.toHaveBeenCalled();
      expect(rafMock).not.toHaveBeenCalled();
      vi.advanceTimersByTime(500);
      expect(flush).not.toHaveBeenCalled();

      setVisibility("visible");
      document.dispatchEvent(new Event("visibilitychange"));
      expect(flush).toHaveBeenCalledTimes(1);
      expect(flush.mock.calls[0][0]).toHaveLength(2);
      expect(onFlushed).toHaveBeenCalledTimes(1);
    } finally {
      batcher.detachVisibilityListener();
      batcher.dispose();
      setVisibility("visible");
    }
  });

  it("ensureFresh：读路径同步兑现但不发布，下一次 flushNow 补发（P0-2）", () => {
    const onFlushed = vi.fn();
    const batcher = new TrajectoryBatcher({ flush, onFlushed });
    batcher.push(event("chunk", 1, "A"));

    batcher.ensureFresh();
    expect(flush).toHaveBeenCalledTimes(1);
    expect(onFlushed).not.toHaveBeenCalled();

    // 无挂起批次的 flushNow 只补发一次通知，不重复应用。
    batcher.flushNow();
    expect(flush).toHaveBeenCalledTimes(1);
    expect(onFlushed).toHaveBeenCalledTimes(1);
    batcher.flushNow();
    expect(onFlushed).toHaveBeenCalledTimes(1);
    batcher.dispose();
  });

  it("空批次 flushNow 不触发 onFlushed", () => {
    const onFlushed = vi.fn();
    const batcher = new TrajectoryBatcher({ flush, onFlushed });
    batcher.flushNow();
    expect(flush).not.toHaveBeenCalled();
    expect(onFlushed).not.toHaveBeenCalled();
    batcher.dispose();
  });

  it("惰性闸门：无订阅者时不 rebuild，读路径 ensureFresh / force 兑现（P0-2）", () => {
    const onFlushed = vi.fn();
    const batcher = new TrajectoryBatcher({
      flush,
      onFlushed,
      shouldFlush: () => false,
    });
    batcher.push(event("chunk", 1, "A"));

    batcher.flushNow(); // 非强制：闸门拦截，保留挂起批次
    expect(flush).not.toHaveBeenCalled();
    expect(onFlushed).not.toHaveBeenCalled();

    batcher.ensureFresh(); // 读路径兜底：同步兑现，不发布
    expect(flush).toHaveBeenCalledTimes(1);
    expect(onFlushed).not.toHaveBeenCalled();

    batcher.flushNow(); // 下一次冲刷补发一次通知
    expect(onFlushed).toHaveBeenCalledTimes(1);

    // 强制冲刷（显式同步点）不受闸门约束。
    batcher.push(event("chunk", 2, "B"));
    batcher.flushNow(true);
    expect(flush).toHaveBeenCalledTimes(2);
    batcher.dispose();
  });
});
