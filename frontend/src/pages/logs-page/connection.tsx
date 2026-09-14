// 由 pages/logs-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
// P1-8：状态口径与配色下沉到 lib/connection-status.ts；日志页头改用共享
// ConnectionStatusBadge（variant="header"），与会话流/直连 chat 流同一套
// 状态词汇、同一套配色与同一份文案键位。

import type { RuntimeLogsConnectionState } from "@/hooks/use-runtime-logs";
import {
  connectionStatusFromLogsState,
  type ConnectionStatus,
} from "@/lib/connection-status";

/** 日志流状态 → 统一连接状态（供日志页头共享呈现件使用）。 */
export function logsConnectionStatus(
  state: RuntimeLogsConnectionState,
): ConnectionStatus {
  return connectionStatusFromLogsState(state);
}
