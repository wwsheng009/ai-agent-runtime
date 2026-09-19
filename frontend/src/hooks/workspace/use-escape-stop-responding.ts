import { useEffect, useRef } from "react";

export type UseEscapeStopRespondingOptions = {
  /** 选中会话是否在途（本地回合或刷新续传）。 */
  responding: boolean;
  /** 与点击 Stop 按钮等价的停止入口。 */
  onStop: () => void;
};

function hasOpenModal(): boolean {
  if (typeof document === "undefined") {
    return false;
  }
  return Boolean(document.querySelector('[aria-modal="true"]'));
}

/**
 * 运行中按 Esc 与 Stop 按钮等价（ESC 中断方案阶段 A）。
 *
 * 约束：
 * - 只在选中会话在途时生效；
 * - 有模态（aria-modal）打开时让位给模态自身的 Esc 语义；
 * - 单次在途回合只投递一次（服务端本身幂等，这里避免连按产生重复请求）。
 */
export function useEscapeStopResponding({
  responding,
  onStop,
}: UseEscapeStopRespondingOptions) {
  const onStopRef = useRef(onStop);
  const stopSentRef = useRef(false);

  useEffect(() => {
    onStopRef.current = onStop;
  }, [onStop]);

  useEffect(() => {
    if (!responding) {
      stopSentRef.current = false;
      return;
    }
    if (typeof window === "undefined") {
      return;
    }

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.isComposing) {
        return;
      }
      if (event.key !== "Escape") {
        return;
      }
      if (event.metaKey || event.ctrlKey || event.altKey) {
        return;
      }
      // 原生下拉/选择器拥有自己的 Esc 语义。
      const target = event.target as HTMLElement | null;
      if (target && target.tagName === "SELECT") {
        return;
      }
      if (hasOpenModal()) {
        return;
      }
      if (stopSentRef.current) {
        return;
      }
      stopSentRef.current = true;
      event.preventDefault();
      onStopRef.current();
    };

    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [responding]);
}
