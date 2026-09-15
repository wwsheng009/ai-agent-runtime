// 由 hooks/workspace/use-workspace-agent-chat-turn.ts 机械拆分而来（P0-2），仅搬迁不改语义。

/**
 * rAF 节流的流式渲染调度器：把高频 SSE delta 合并为每帧一次渲染；后台标签页不触发
 * rAF，用 setTimeout 兜底（G3），标签页恢复可见时立即冲刷。
 *
 * 2026-09-15 追加**最小提交间隔**（`MIN_COMMIT_INTERVAL_MS`）：文本 / 推理 delta 的每次
 * 提交都会改写页面级 thread state，因此 `WorkspacePage` → `WorkspaceShell` → 侧栏 /
 * 面板 / 消息列整棵树都要重渲染一次。CDP CPU profile 实测（`e2e/zz-perf-probe.manual.ts`）：
 * 按 rAF（≈60 次/秒）提交时，流式期间主线程约 330ms/s 花在这条链上——i18next 每帧几十次
 * `t()`、React 全页协调、样式重算（另有 ~80ms/s RecalcStyle + ~30ms/s Layout）；长会话下
 * 还会打出 150-200ms 的长任务，即用户看到的「整页卡住」。
 *
 * 因此把**页面级提交**压到最小间隔（默认 ≈8 次/秒）。平滑度不受影响：
 * - 行内打字机（`use-typewriter`）仍按 rAF 60fps 逐字揭示，它消费的是目标文本，
 *   目标文本按最小间隔跳跃、揭示过程连续；
 * - 贴底跟随由滚动宿主的 `ResizeObserver` 持续钉底线，不依赖提交频率；
 * - 工具结果、可见性恢复、回合收尾走 `flush()`，立即提交（不受最小间隔约束）。
 */
const MIN_COMMIT_INTERVAL_MS = 120;

export function createStreamingFrameScheduler(
  renderStreamingMessage: () => void,
) {
  let pendingStreamingFrame: number | null = null;
  let pendingStreamingTimeout: number | null = null;
  let lastCommitAt = 0;

  const now = () =>
    typeof performance !== "undefined" && typeof performance.now === "function"
      ? performance.now()
      : Date.now();

  const clearPendingStreamingTimeout = () => {
    if (
      pendingStreamingTimeout !== null &&
      typeof window !== "undefined" &&
      typeof window.clearTimeout === "function"
    ) {
      window.clearTimeout(pendingStreamingTimeout);
    }
    pendingStreamingTimeout = null;
  };

  const cancelPending = () => {
    if (
      pendingStreamingFrame !== null &&
      typeof window !== "undefined" &&
      typeof window.cancelAnimationFrame === "function"
    ) {
      window.cancelAnimationFrame(pendingStreamingFrame);
    }
    pendingStreamingFrame = null;
    clearPendingStreamingTimeout();
  };

  const commitStreamingMessage = () => {
    cancelPending();
    lastCommitAt = now();
    renderStreamingMessage();
  };

  /** 立即冲刷（页面恢复可见 / 流结束时调用）：不受最小间隔约束。 */
  const flushStreamingMessage = () => {
    commitStreamingMessage();
  };

  /** 帧对齐提交（rAF + 后台标签页 setTimeout 兜底，G3）。 */
  const scheduleStreamingFrame = () => {
    if (pendingStreamingFrame !== null) {
      return;
    }

    pendingStreamingFrame = window.requestAnimationFrame(() => {
      pendingStreamingFrame = null;
      clearPendingStreamingTimeout();
      commitStreamingMessage();
    });

    // Background tabs do not fire rAF; a setTimeout fallback keeps the
    // stream rendering alive (G3) and flushes as soon as the tab returns.
    pendingStreamingTimeout = window.setTimeout(() => {
      if (
        pendingStreamingFrame !== null &&
        typeof window.cancelAnimationFrame === "function"
      ) {
        window.cancelAnimationFrame(pendingStreamingFrame);
        pendingStreamingFrame = null;
      }
      pendingStreamingTimeout = null;
      commitStreamingMessage();
    }, 100);
  };

  const scheduleStreamingMessage = () => {
    if (pendingStreamingFrame !== null || pendingStreamingTimeout !== null) {
      return;
    }

    if (
      typeof window === "undefined" ||
      typeof window.requestAnimationFrame !== "function"
    ) {
      flushStreamingMessage();
      return;
    }

    // 距上次页面级提交不足最小间隔：先等满，再对齐到下一帧提交；
    // 等待期间到达的 delta 已累积在 turnState 里，提交时一次性兑现。
    const waitMs = MIN_COMMIT_INTERVAL_MS - (now() - lastCommitAt);
    if (waitMs > 0) {
      pendingStreamingTimeout = window.setTimeout(() => {
        pendingStreamingTimeout = null;
        scheduleStreamingFrame();
      }, waitMs);
      return;
    }

    scheduleStreamingFrame();
  };

  const handleVisibilityChange = () => {
    if (document.visibilityState === "visible") {
      flushStreamingMessage();
    }
  };

  const attachVisibilityListener = () => {
    if (typeof document === "undefined") {
      return;
    }
    document.addEventListener("visibilitychange", handleVisibilityChange);
  };

  const detachVisibilityListener = () => {
    if (typeof document === "undefined") {
      return;
    }
    document.removeEventListener("visibilitychange", handleVisibilityChange);
  };

  return {
    cancel: cancelPending,
    flush: flushStreamingMessage,
    schedule: scheduleStreamingMessage,
    attachVisibilityListener,
    detachVisibilityListener,
  };
}
