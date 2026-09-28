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
  /** 应用批次后的发布回调（P0-2：读路径兜底时发布被推迟，故与 apply 分离）。 */
  onFlushed?: () => void;
  /**
   * 惰性闸门（P0-2「无订阅者不 rebuild」）：返回 false 时非强制冲刷保留挂起
   * 批次，等读路径 `ensureFresh` 兜底（参考 notifier：`listeners.size === 0` 早退）。
   */
  shouldFlush?: () => boolean;
  /** 后台标签页兜底延时（rAF 不触发时）。默认 100ms。 */
  fallbackDelayMs?: number;
}

/** 隐藏标签页判定（P0-2）：无 document 环境（SSR / 单测）视为可见。 */
function isDocumentHidden(): boolean {
  return typeof document !== "undefined" && document.visibilityState === "hidden";
}

/**
 * rAF 帧内合并调度器：
 * - push 的事件在下一帧统一 flush 给 reducer（批量 → ChangeSet 去重合并）；
 * - 后台标签页零工作（P0-2）：不排 rAF、不排兜底定时器，挂起批次由
 *   visibilitychange 恢复时一次冲刷（读路径 ensureFresh 同样兜底）。
 */
export class TrajectoryBatcher {
  private readonly flush: TrajectoryFlushHandler;
  private readonly onFlushed: (() => void) | undefined;
  private readonly shouldFlush: (() => boolean) | undefined;
  private readonly fallbackDelayMs: number;
  private pending: TrajectoryEvent[] = [];
  private frame: number | null = null;
  private timeout: number | null = null;
  /** 已应用但尚未发布（读路径 ensureFresh 兜底）：下一次 flush 补发一次通知。 */
  private notifyPending = false;
  private readonly handleVisibilityChange = () => {
    if (typeof document === "undefined") {
      return;
    }
    if (document.visibilityState === "visible") {
      this.flushNow();
    }
  };

  constructor(options: TrajectoryBatcherOptions) {
    this.flush = options.flush;
    this.onFlushed = options.onFlushed;
    this.shouldFlush = options.shouldFlush;
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
    // P0-2：隐藏标签页零工作——不排帧也不排兜底定时器，事件留在挂起批次里，
    // 恢复可见时由 visibilitychange 一次冲刷（读路径 ensureFresh 同样兜底）。
    if (isDocumentHidden()) {
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

  /**
   * 立即冲刷（页面恢复可见 / 流结束时调用）。
   * - `force=false`：受惰性闸门约束——无订阅者时保留挂起批次（读路径兜底）；
   * - `force=true`：显式同步点（store flush / 游标推进 / 重建 / dispose）一律兑现。
   */
  flushNow(force = false) {
    const hadPending = this.pending.length > 0;
    if (hadPending && !force && this.shouldFlush?.() === false) {
      return; // 惰性：无订阅者不 rebuild，挂起批次留给读路径 ensureFresh
    }
    if (hadPending) {
      const batch = this.pending;
      this.pending = [];
      this.flush(batch);
    }
    if (hadPending || this.notifyPending) {
      this.notifyPending = false;
      this.onFlushed?.();
    }
  }

  /**
   * 读路径兜底（P0-2，参考 notifier.ensureFresh）：有挂起批次时同步应用，
   * 但**不在此刻发布**——发布由下一次 flushNow（帧回调 / 可见性恢复 / dispose）
   * 补发一次，避免在 React 渲染期同步通知订阅方。
   */
  ensureFresh() {
    if (this.pending.length === 0) {
      return;
    }
    const batch = this.pending;
    this.pending = [];
    this.flush(batch);
    this.notifyPending = true;
  }

  /** 隐藏标签页零工作（P0-2）：恢复可见时冲刷挂起批次。 */
  attachVisibilityListener() {
    if (typeof document === "undefined") {
      return;
    }
    document.addEventListener("visibilitychange", this.handleVisibilityChange);
  }

  detachVisibilityListener() {
    if (typeof document === "undefined") {
      return;
    }
    document.removeEventListener("visibilitychange", this.handleVisibilityChange);
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
    this.flushNow(true);
  }

  private clearFallback() {
    if (this.timeout !== null && typeof window !== "undefined") {
      window.clearTimeout(this.timeout);
    }
    this.timeout = null;
  }
}
