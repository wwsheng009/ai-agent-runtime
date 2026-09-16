import { RUNTIME_CONNECT_TIMEOUT_MS } from "./shared";
import { type ChatTurnRuntimeState } from "./turn-state";

/**
 * 连接超时保护：后端可能因共享 SQLite 被其他进程锁住而无法在有限时间内建立
 * SSE 流（表现为 "Connecting to runtime…" 无限旋转）。若 N 秒内未收到任何
 * runtime 事件（onMeta 等会置 turnState.receivedRuntimeActivity），主动中止请求
 * 并进入错误状态，而不是让前端永远卡在 connecting 阶段。
 */
export function createConnectTimeoutGuard(options: {
  controller: AbortController;
  onTimeout: () => void;
  turnState: ChatTurnRuntimeState;
}) {
  let timeoutId: number | undefined;
  return {
    start() {
      timeoutId = window.setTimeout(() => {
        const { receivedRuntimeActivity, turnFinalized } = options.turnState;
        if (
          receivedRuntimeActivity ||
          turnFinalized ||
          options.controller.signal.aborted
        ) {
          return;
        }
        options.turnState.connectTimedOut = true;
        options.controller.abort();
        options.onTimeout();
      }, RUNTIME_CONNECT_TIMEOUT_MS);
    },
    clear() {
      if (timeoutId !== undefined && typeof window !== "undefined") {
        window.clearTimeout(timeoutId);
      }
      timeoutId = undefined;
    },
  };
}
