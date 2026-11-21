import { requestSessionTurnInterrupt } from "@/api/runtime/session-turn-control";

import type { SessionTurnRegistry } from "./agent-chat-turn/session-turn-registry";

/**
 * 建议 3（后端 cancel 契约）：abort 只让本地 UI 收尾，服务端 detached 回合仍在跑，
 * 因此带回合身份投递 interrupt（best-effort，失败不阻塞本地停止）。
 * 按**选中会话**定位：停止按钮只作用于当前会话，后台会话的回合不受影响。
 */
export function stopRespondingTurn(
  turnRegistry: SessionTurnRegistry,
  selectedTurnKey: string,
) {
  const entry = turnRegistry.getSnapshot(selectedTurnKey).entry;
  void requestSessionTurnInterrupt(
    entry?.turnState.currentSessionId,
    entry ? (entry.activeTurnId ?? entry.turnId) : null,
  );
  entry?.controller.abort();
}