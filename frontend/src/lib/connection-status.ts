/**
 * P1-8：连接状态统一口径（会话运行时流 / 日志流 / 直连 chat 流共用）。
 * 纯函数层：只做状态收敛与文案/样式选择，不依赖 React 与具体传输实现。
 */

export type ConnectionStatus =
  | "idle"
  | "connecting"
  | "online"
  | "reconnecting"
  | "offline";

export type ConnectionStatusLabels = {
  connecting: string;
  idle: string;
  offline: string;
  online: string;
  reconnecting: string;
};

export type ConnectionStatusIcon = "online" | "connecting" | "offline" | "idle";

export type ConnectionStatusTone = {
  badgeClassName: string;
  icon: ConnectionStatusIcon;
  label: string;
};

/** 日志流既有状态机（`use-runtime-logs`）的等价口径，用于跨域映射。 */
export type LogsLikeConnectionState =
  | "idle"
  | "connecting"
  | "open"
  | "reconnecting"
  | "error";

const CONNECTION_TONES: Record<
  ConnectionStatus,
  { badgeClassName: string; icon: ConnectionStatusIcon }
> = {
  online: {
    badgeClassName: "border-emerald-500/30 bg-emerald-500/12 text-emerald-200",
    icon: "online",
  },
  connecting: {
    badgeClassName: "border-sky-500/30 bg-sky-500/12 text-sky-200",
    icon: "connecting",
  },
  reconnecting: {
    badgeClassName: "border-amber-500/30 bg-amber-500/12 text-amber-200",
    icon: "connecting",
  },
  offline: {
    badgeClassName: "border-red-500/30 bg-red-500/12 text-red-200",
    icon: "offline",
  },
  idle: {
    badgeClassName: "border-border bg-surface-soft text-muted-foreground",
    icon: "idle",
  },
};

export function getConnectionStatusTone(
  status: ConnectionStatus,
  labels: ConnectionStatusLabels,
): ConnectionStatusTone {
  const tone = CONNECTION_TONES[status];
  return { ...tone, label: labels[status] };
}

/** 日志流状态 → 统一状态（P1-8 起日志页与工作台共用同一呈现件）。 */
export function connectionStatusFromLogsState(
  state: LogsLikeConnectionState,
): ConnectionStatus {
  switch (state) {
    case "open":
      return "online";
    case "connecting":
      return "connecting";
    case "reconnecting":
      return "reconnecting";
    case "error":
      return "offline";
    default:
      return "idle";
  }
}

/** 非在线态都允许手动重试（与自动重连共用入口）。 */
export function canManualRetryConnection(status: ConnectionStatus) {
  return status !== "online";
}

/**
 * P1-8：直连 `/api/agent/chat` 流失败会把线程 `transport` 标记为 `error`
 * （见 `use-workspace-agent-chat-turn`），此时会话运行时流可能仍在线，但用户
 * 必须看到「断线 + 可手动重试」。该降级不写入会话流自身状态，只在呈现层收敛。
 */
export function withTransportDegradation(
  status: ConnectionStatus,
  transport: string | null | undefined,
): ConnectionStatus {
  if (status === "offline" || transport === "error") {
    return "offline";
  }

  return status;
}
