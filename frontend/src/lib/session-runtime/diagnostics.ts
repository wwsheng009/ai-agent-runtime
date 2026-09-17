// Batch 4（多会话并发运行时 §4.6）：注册表观测投影（纯函数，供诊断面板消费）。
//
// 面板要回答的运维问题：当前打开了多少条会话订阅、其中多少条是 live（真实 SSE
// 连接）、多少条已降级轮询、页面隐藏时有没有生效降采样。这些都能从注册表条目
// 快照推导，因此不新增状态来源，也不依赖 React。
//
// 与后端 `active_connections` 指标对账时以 `live` 为准（= 前端实际持有的长连接数，
// 含前台 1 条）。

import {
  DEFAULT_BACKGROUND_LIVE_BUDGET,
  DEFAULT_FOREGROUND_LIVE_BUDGET,
} from "./flags";
import type { SessionRuntimeEntrySnapshot, SubscriptionMode } from "./types";

export type SessionRuntimeDiagnosticsSummary = {
  /** 注册表条目总数（= 被订阅的会话数）。 */
  total: number;
  /** 实际持有长连接的条目数（live 模式）。 */
  live: number;
  /** 降级轮询的条目数。 */
  poll: number;
  /** 已释放但仍有记录的条目数（正常为 0，便于发现收敛问题）。 */
  idle: number;
  /** 指定会话的订阅模式；该会话不在订阅集合时为 null。 */
  sessionMode: SubscriptionMode | null;
  /** 页面当前是否隐藏（隐藏时后台 live 应降为 poll）。 */
  pageHidden: boolean;
  /** live 预算（前台 + 后台），用于对照「live 是否超预算」。 */
  foregroundBudget: number;
  backgroundBudget: number;
};

export function summarizeSessionRuntimeEntries(
  entries: readonly SessionRuntimeEntrySnapshot[],
  options: {
    sessionId?: string | null;
    pageHidden?: boolean;
    foregroundBudget?: number;
    backgroundBudget?: number;
  } = {},
): SessionRuntimeDiagnosticsSummary {
  const {
    sessionId,
    pageHidden = false,
    foregroundBudget = DEFAULT_FOREGROUND_LIVE_BUDGET,
    backgroundBudget = DEFAULT_BACKGROUND_LIVE_BUDGET,
  } = options;

  let live = 0;
  let poll = 0;
  let idle = 0;
  let sessionMode: SubscriptionMode | null = null;
  const normalizedSessionId = sessionId?.trim() ?? "";

  for (const entry of entries) {
    if (entry.mode === "live") {
      live += 1;
    } else if (entry.mode === "poll") {
      poll += 1;
    } else {
      idle += 1;
    }
    if (normalizedSessionId && entry.sessionId === normalizedSessionId) {
      sessionMode = entry.mode;
    }
  }

  return {
    total: entries.length,
    live,
    poll,
    idle,
    sessionMode,
    pageHidden,
    foregroundBudget,
    backgroundBudget,
  };
}
