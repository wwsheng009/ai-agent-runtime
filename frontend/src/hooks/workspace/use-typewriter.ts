import { useEffect, useState } from "react";

import {
  advanceOnTargetChange,
  advanceReveal,
  createTypewriterState,
  visibleTypewriterText,
  type TypewriterState,
} from "@/lib/typewriter";

/**
 * useTypewriter —— 流式文本的打字机（渐进揭示）。
 *
 * 纯状态机在 `@/lib/typewriter`（单调前缀揭示 / 只对增长中的文本打字 / 自适应追赶），
 * 这里只负责「驱动」：渲染期推进目标态，rAF 里按真实经过时间推进揭示量。
 *
 * 性能约定：
 * - `active=false`（历史消息 / 已结束 / 被打断）时零成本退化，不建 rAF 循环；
 * - 追平后 `animating` 翻 false，effect 卸载并取消帧回调，不常驻 rAF；
 * - 单帧最多推进 `MAX_FRAME_MS`（长任务 / 后台标签页恢复后不会一次性冲刷）。
 */

/** 无 rAF 环境（老浏览器 / 部分测试环境）下的兜底帧间隔。 */
const FRAME_FALLBACK_MS = 16;

function nowMs(): number {
  return typeof performance !== "undefined" &&
    typeof performance.now === "function"
    ? performance.now()
    : Date.now();
}

function scheduleFrame(callback: () => void): { cancel: () => void } {
  if (
    typeof window !== "undefined" &&
    typeof window.requestAnimationFrame === "function"
  ) {
    const handle = window.requestAnimationFrame(callback);
    return { cancel: () => window.cancelAnimationFrame(handle) };
  }
  const handle = setTimeout(callback, FRAME_FALLBACK_MS);
  return { cancel: () => clearTimeout(handle) };
}

export function useTypewriter(content: string, active: boolean): string {
  const [state, setState] = useState<TypewriterState>(() =>
    createTypewriterState(content),
  );

  // 渲染期状态调整（React 官方 "storing information from previous renders"
  // 模式，与 message-markdown 的冻结代同源）：目标文本变化时推进状态机。
  // 用函数式更新 + 守卫，收敛后不再触发。
  if (state.target !== content) {
    setState((prev) => advanceOnTargetChange(prev, content, active));
  }

  // 揭示推进一律走函数式更新，帧回调不需要读渲染闭包里的旧状态。
  const animating = state.animating;
  useEffect(() => {
    if (!animating || !active) {
      return;
    }
    let disposed = false;
    let lastTick = nowMs();
    let frame: { cancel: () => void } | null = null;

    const tick = () => {
      if (disposed) {
        return;
      }
      const now = nowMs();
      // 时长截断由状态机负责（`MAX_FRAME_MS`），这里只提供真实经过时间。
      const elapsed = Math.max(0, now - lastTick);
      lastTick = now;
      setState((prev) => (prev.animating ? advanceReveal(prev, elapsed) : prev));
      // 追平后 `animating` 翻 false，本 effect 的 cleanup 会取消这一帧。
      frame = scheduleFrame(tick);
    };

    frame = scheduleFrame(tick);
    return () => {
      disposed = true;
      frame?.cancel();
    };
  }, [active, animating]);

  return visibleTypewriterText(state, content, active);
}
