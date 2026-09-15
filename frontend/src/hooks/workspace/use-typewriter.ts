import { useEffect, useRef, useState, type RefObject } from "react";

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
 * 这里只负责「驱动」：帧回调里读目标（store 副本 + live ref）并按真实经过时间推进
 * 揭示量；渲染期不读任何 ref，也不反过来推进状态机。
 *
 * 性能约定：
 * - `active=false`（历史消息 / 已结束 / 被打断）时零成本退化，不建 rAF 循环；
 * - 流式期间帧循环常驻（追平也不停），但追平帧不 `setState`：循环继续存在的唯一
 *   目的是让「只写 ref 的 live 增量」在下一帧被顺带揭示，而不必自己驱动一次渲染；
 * - 单帧最多推进 `MAX_FRAME_MS`（长任务 / 后台标签页恢复后不会一次性冲刷）。
 */

/** 无 rAF 环境（老浏览器 / 部分测试环境）下的兜底帧间隔。 */
const FRAME_FALLBACK_MS = 16;

/**
 * 揭示状态的**最小提交间隔**（≈30fps）。
 *
 * 状态机按**真实经过时间**推进（`advanceReveal(prev, elapsed)`），揭示速率与提交
 * 频率无关：把提交降到 30fps 只是把每帧的字数从 1 个变成 2–3 个，手感不变；但
 * 每提交一次都会用新的 revealed 文本重渲染整条消息——尾块的 markdown 随之重新
 * 解析一次，这才是流式期间的主要 CPU。
 *
 * 依据（`e2e/zz-mount-audit.mjs`，假 SSE + 真实 Chrome，507 帧 / 16s）：打字机按
 * rAF 提交时 MutationObserver 记录 938 次回调 / charData 937 次（≈59 次/秒，而
 * 服务端只有 32 帧/秒，说明驱动源是打字机而不是 SSE），单条流式消息的
 * ScriptDuration 7.0–7.7s（主线程约 45% 占用）。
 */
const REVEAL_COMMIT_INTERVAL_MS = 32;

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

export function useTypewriter(
  content: string,
  active: boolean,
  liveTextRef?: RefObject<string | null>,
): string {
  const [state, setState] = useState<TypewriterState>(() =>
    createTypewriterState(content),
  );
  // 帧循环里读最新状态：把 state 放进 effect deps 会让循环每帧重建。镜像只在
  // 帧回调里更新（不在渲染期写 ref，见 react-hooks/refs）；状态的全部变更都发生在
  // 帧回调里，因此这个镜像始终与 state 同步。
  const stateRef = useRef(state);
  // store 副本走 ref：结构快照（1 次/秒）变化只改变目标，不额外驱动渲染。
  // effect 在提交后、下一帧回调之前执行，帧回调读到的永远是最新副本。
  const contentRef = useRef(content);
  useEffect(() => {
    contentRef.current = content;
  }, [content]);

  // 揭示推进：只要还在流式就保持帧循环（追平也不停），否则新增量到达时没有渲染
  // 可搭车、也无从「醒来」。追平帧不 setState（`next === prev`），成本只有一次
  // 空转回调。
  useEffect(() => {
    if (!active) {
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
      // 攒够最小提交间隔再推进：揭示量按真实耗时计算，速率不受提交频率影响。
      if (elapsed >= REVEAL_COMMIT_INTERVAL_MS) {
        lastTick = now;
        const prev = stateRef.current;
        const base = contentRef.current;
        const liveNow = liveTextRef?.current ?? null;
        // live 增量只写 ref（见 use-live-stream-text-ref）：目标在**帧回调里**读取，
        // 渲染期不碰 ref（react-hooks/refs）。live 短于 store 副本时回落副本
        // （reload / 重连后 live 从头起算，见 segment-components 同名守卫）。
        const targetNow =
          liveNow !== null && liveNow.length >= base.length ? liveNow : base;
        const next = advanceReveal(
          advanceOnTargetChange(prev, targetNow, true),
          elapsed,
        );
        // 无进展（追平且目标未变）→ 不 setState：同值更新会让组件再渲染一次，
        // 白白重解析一遍 markdown 尾块。
        if (next !== prev) {
          stateRef.current = next;
          setState(next);
        }
      }
      // 追平后 `animating` 翻 false，本 effect 的 cleanup 会取消这一帧。
      frame = scheduleFrame(tick);
    };

    frame = scheduleFrame(tick);
    return () => {
      disposed = true;
      frame?.cancel();
    };
  }, [active, liveTextRef]);

  // 渲染只读 state：目标推进全部发生在帧回调里，渲染于是永远拿到「上一次节拍
  // 看到的目标」。这样既绕开「渲染期读 ref」，又保证追平帧显示的就是 state.target
  // 本身（不会退回更短的 store 副本）；`content` 只在非流式时直接直挂。
  return visibleTypewriterText(state, active ? state.target : content, active);
}
