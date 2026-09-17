/**
 * Batch 2（多会话并发运行时）：会话运行时注册表的纯类型层（无 React / 无 IO）。
 *
 * 口径见 `docs/plan/workspace-multi-session-concurrent-runtime-plan.md` §4.2：
 * - `SubscriptionMode` 决定该会话的订阅强度：`live` = 常驻 SSE（`live=1`）、
 *   `poll` = 低频快照轮询（`GET /runtime`）、`idle` = 不订阅；
 * - `SessionRuntimeEntrySnapshot` 是注册表向 React 与侧栏暴露的**唯一**投影：
 *   后台会话只维护这份轻量状态，不写 `threads`、不渲染消息（D1-B，§4.3）。
 */

import type { ConnectionStatus } from "@/lib/connection-status";
import type {
  RuntimeSessionActiveTurn,
  SessionRuntimeEvent,
} from "@/types/runtime";

export type SubscriptionMode =
  | "live" // 常驻 SSE（/runtime/stream?live=1）：前台会话或后台活跃回合
  | "poll" // 低频快照轮询（GET /runtime）：后台非活跃 / 超预算会话
  | "idle"; // 不订阅（归档 / 关闭 / 长时间空闲）

/** 待交互计数（呈现层仍按选中会话过滤，注册表层只统计）。 */
export type SessionRuntimePendingCounts = {
  approvals: number;
  questions: number;
  /** 计划评审挂起（plan_review 条目已注册、尚未裁决）。 */
  planPending: boolean;
};

export type SessionRuntimeEntrySnapshot = {
  sessionId: string;
  mode: SubscriptionMode;
  /**
   * 传输健康度。`poll` 模式下同样使用这套枚举：首次快照成功前 `connecting`，
   * 成功后 `online`，连续失败 `reconnecting` / `offline`。
   */
  status: ConnectionStatus;
  /** 已消费的最大 seq（续传游标，跨切换 / 跨重连保持）。 */
  lastSeq: number;
  activeTurn: RuntimeSessionActiveTurn | null;
  /** 后台续传中（服务端回合与本地请求解耦）。 */
  detached: boolean;
  pending: SessionRuntimePendingCounts;
  runningAgents: number;
  lastEventAt: number | null;
  lastError: string | null;
};

export type SessionRuntimeEntryObserver = (
  snapshot: SessionRuntimeEntrySnapshot,
) => void;

/** 事件出口：注册表把「谁的事件」一并交给页面层（§4.2 `onEvent`）。 */
export type SessionRuntimeEventOutlet = (
  sessionId: string,
  event: SessionRuntimeEvent,
) => void;
