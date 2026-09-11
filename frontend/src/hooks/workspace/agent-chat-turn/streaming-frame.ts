// 由 hooks/workspace/use-workspace-agent-chat-turn.ts 机械拆分而来（P0-2），仅搬迁不改语义。

/**
 * rAF 节流的流式渲染调度器：把高频 SSE delta 合并为每帧一次渲染；后台标签页不触发
 * rAF，用 setTimeout 兜底（G3），标签页恢复可见时立即冲刷。
 */
export function createStreamingFrameScheduler(
  renderStreamingMessage: () => void,
) {
  let pendingStreamingFrame: number | null = null;
  let pendingStreamingTimeout: number | null = null;

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

  const cancelStreamingFrame = () => {
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

  const flushStreamingMessage = () => {
    cancelStreamingFrame();
    renderStreamingMessage();
  };

  const scheduleStreamingMessage = () => {
    if (pendingStreamingFrame !== null) {
      return;
    }

    if (
      typeof window === "undefined" ||
      typeof window.requestAnimationFrame !== "function"
    ) {
      flushStreamingMessage();
      return;
    }

    pendingStreamingFrame = window.requestAnimationFrame(() => {
      pendingStreamingFrame = null;
      clearPendingStreamingTimeout();
      renderStreamingMessage();
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
      renderStreamingMessage();
    }, 100);
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
    cancel: cancelStreamingFrame,
    flush: flushStreamingMessage,
    schedule: scheduleStreamingMessage,
    attachVisibilityListener,
    detachVisibilityListener,
  };
}
