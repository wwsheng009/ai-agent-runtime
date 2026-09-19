import { useCallback } from "react";

import { useEscapeStopResponding } from "@/hooks/workspace/use-escape-stop-responding";
import type { PendingInteractionConvergeReason } from "@/lib/pending-interaction";

export type UseStopRespondingOptions = {
  /** 本地回合身份；缺席 = 刷新后的页面（没有本地请求可 abort）。 */
  activeTurnId: string | null | undefined;
  /** 未决审批 / 提问的立即收敛入口（不等可能缺席的中断事件）。 */
  convergePendingInteractions: (reason: PendingInteractionConvergeReason) => void;
  /** 选中会话是否在途（本地回合或刷新续传），驱动 Esc 快捷键。 */
  currentSessionResponding: boolean;
  /** 本地回合停止入口（abort 本地请求 + 标记停止）。 */
  stopResponding: () => void;
  /** 刷新续传：向服务端显式投递 interrupt。 */
  stopResumedTurn: () => void;
};

/**
 * 「停止应答」的完整语义（P1-7 + ESC 中断方案阶段 A）：Stop 按钮与 Esc 等价。
 *
 * 停止时先让未决审批 / 提问立即收敛（避免停止后卡片仍挂在 composer 上沿），
 * 再停本地回合；刷新后的页面没有本地请求可 abort，必须显式把 interrupt 投给服务端
 * （本地回合在跑时不重复投递——chat-turn hook 已带本地回合身份投递）。
 */
export function useStopResponding({
  activeTurnId,
  convergePendingInteractions,
  currentSessionResponding,
  stopResponding,
  stopResumedTurn,
}: UseStopRespondingOptions) {
  const stop = useCallback(() => {
    convergePendingInteractions("session_interrupted");
    if (!activeTurnId) {
      void stopResumedTurn();
    }
    stopResponding();
  }, [activeTurnId, convergePendingInteractions, stopResponding, stopResumedTurn]);

  useEscapeStopResponding({ onStop: stop, responding: currentSessionResponding });

  return stop;
}
