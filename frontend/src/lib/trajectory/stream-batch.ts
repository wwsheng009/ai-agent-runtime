/**
 * Trajectory 流批处理（rAF 帧内合并 + 后台标签页兜底；承接 G3）
 *
 * 对齐 TUI `coalesceStreamDeltas` 思路：
 * - 一帧内到达的多个事件批量交给 reducer（applyEvents 负责 ChangeSet 去重合并）；
 * - 同 kind 连续事件可合并为「段」供渲染层消费；空 delta 保留以完成 live
 *   reasoning 边界；reasoning→text 等不同 kind 边界不合并。
 */
import type { TrajectoryEvent } from "./types";

/** 帧内同 kind 连续事件合并结果（渲染用段）。 */
export interface TrajectorySegment {
  kind: TrajectoryEvent["kind"];
  /** [firstSeq, lastSeq]（段内事件 seq 范围）。 */
  seqs: [number, number];
  payloads: Record<string, unknown>[];
}

/**
 * 同 kind 相邻事件合并为段。
 * - 合并仅对「相邻同 kind」发生；kind 边界（如 reasoning→text）保持独立段；
 * - 空 delta 事件保留在段内（live reasoning 完成边界由渲染层消费空段判定）。
 */
export function coalesceTrajectoryEvents(
  events: TrajectoryEvent[],
): TrajectorySegment[] {
  const segments: TrajectorySegment[] = [];
  for (const event of events) {
    const last = segments[segments.length - 1];
    if (last && last.kind === event.kind) {
      last.seqs[1] = event.seq;
      last.payloads.push(event.payload);
    } else {
      segments.push({
        kind: event.kind,
        seqs: [event.seq, event.seq],
        payloads: [event.payload],
      });
    }
  }
  return segments;
}

export type TrajectoryFlushHandler = (events: TrajectoryEvent[]) => void;

export interface TrajectoryBatcherOptions {
  flush: TrajectoryFlushHandler;
  /** 后台标签页兜底延时（rAF 不触发时）。默认 100ms。 */
  fallbackDelayMs?: number;
}

/**
 * rAF 帧内合并调度器：
 * - push 的事件在下一帧统一 flush 给 reducer（批量 → ChangeSet 去重合并）；
 * - 后台标签页不触发 rAF，setTimeout 兜底保持流推进（G3），页面恢复可见时立即冲刷。
 */
export class TrajectoryBatcher {
  private readonly flush: TrajectoryFlushHandler;
  private readonly fallbackDelayMs: number;
  private pending: TrajectoryEvent[] = [];
  private frame: number | null = null;
  private timeout: number | null = null;

  constructor(options: TrajectoryBatcherOptions) {
    this.flush = options.flush;
    this.fallbackDelayMs = options.fallbackDelayMs ?? 100;
  }

  push(event: TrajectoryEvent) {
    this.pending.push(event);
    if (this.frame !== null) {
      return;
    }
    if (
      typeof window === "undefined" ||
      typeof window.requestAnimationFrame !== "function"
    ) {
      this.flushNow();
      return;
    }
    this.frame = window.requestAnimationFrame(() => {
      this.frame = null;
      this.clearFallback();
      this.flushNow();
    });
    this.timeout = window.setTimeout(() => {
      if (
        this.frame !== null &&
        typeof window.cancelAnimationFrame === "function"
      ) {
        window.cancelAnimationFrame(this.frame);
        this.frame = null;
      }
      this.timeout = null;
      this.flushNow();
    }, this.fallbackDelayMs);
  }

  /** 立即冲刷（页面恢复可见 / 流结束时调用）。 */
  flushNow() {
    if (this.pending.length === 0) {
      return;
    }
    const batch = this.pending;
    this.pending = [];
    this.flush(batch);
  }

  /** 丢弃全部挂起事件（会话/线程切换，reset 用；已 flush 的快照由 store 重建）。 */
  clear() {
    this.pending = [];
    if (
      this.frame !== null &&
      typeof window !== "undefined" &&
      typeof window.cancelAnimationFrame === "function"
    ) {
      window.cancelAnimationFrame(this.frame);
    }
    this.frame = null;
    this.clearFallback();
  }

  /** 流结束清理（取消挂起的帧/定时器，冲刷残余）。 */
  dispose() {
    if (
      this.frame !== null &&
      typeof window !== "undefined" &&
      typeof window.cancelAnimationFrame === "function"
    ) {
      window.cancelAnimationFrame(this.frame);
    }
    this.frame = null;
    this.clearFallback();
    this.flushNow();
  }

  private clearFallback() {
    if (this.timeout !== null && typeof window !== "undefined") {
      window.clearTimeout(this.timeout);
    }
    this.timeout = null;
  }
}
