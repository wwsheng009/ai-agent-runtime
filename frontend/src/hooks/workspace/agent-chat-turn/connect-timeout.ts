import { RUNTIME_CONNECT_TIMEOUT_MS } from "./shared";
import { type ChatTurnRuntimeState } from "./turn-state";

/**
 * 连接超时保护：后端可能因共享 SQLite 被其他进程锁住而无法在有限时间内建立
 * SSE 流（表现为 "Connecting to runtime…" 无限旋转）。若 N 秒内既没有业务
 * 活动（onMeta 等会置 turnState.receivedRuntimeActivity）也没有读到任何字节
 * （turnState.receivedStreamBytes，含 `: open`/`: keepalive` 注释帧），主动
 * 中止请求并进入错误状态，而不是让前端永远卡在 connecting 阶段。
 *
 * 「连接成功」以任意字节为准：服务端在请求入口就写 `: open` 首帧，注释帧
 * 与 sse.ts 读侧 45s 静默看门狗同一口径。
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
        const { receivedRuntimeActivity, receivedStreamBytes, turnFinalized } =
          options.turnState;
        if (
          receivedRuntimeActivity ||
          receivedStreamBytes ||
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
