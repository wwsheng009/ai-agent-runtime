import { RefreshCwIcon, WifiIcon, WifiOffIcon } from "lucide-react";

import {
  canManualRetryConnection,
  getConnectionStatusTone,
  type ConnectionStatus,
  type ConnectionStatusLabels,
} from "@/lib/connection-status";
import { cn } from "@/lib/utils";

type ConnectionStatusBadgeProps = {
  status: ConnectionStatus;
  labels: ConnectionStatusLabels;
  /** 提供时在非在线态渲染手动重试入口（与自动重连共用同一重连逻辑）。 */
  onRetry?: () => void;
  retryLabel?: string;
  /** 呈现变体：pill=工作台状态条；header=日志页头（紧凑大写，沿用既有视觉）。 */
  variant?: "pill" | "header";
  className?: string;
};

/** 变体基类：颜色一律由 `getConnectionStatusTone` 提供，保证两处同色同义。 */
const VARIANT_CLASSNAMES = {
  pill: "gap-1.5 rounded-full px-2 py-0.5 app-text-10",
  header:
    "gap-1 rounded-control px-2 py-0.5 app-text-9 font-semibold uppercase tracking-[0.12em]",
} as const;

const VARIANT_ICON_SIZE = { pill: 12, header: 14 } as const;

const STATUS_ICONS = {
  connecting: RefreshCwIcon,
  idle: WifiOffIcon,
  offline: WifiOffIcon,
  online: WifiIcon,
} as const;

export function ConnectionStatusBadge({
  status,
  labels,
  onRetry,
  retryLabel,
  variant = "pill",
  className,
}: ConnectionStatusBadgeProps) {
  const tone = getConnectionStatusTone(status, labels);
  const Icon = STATUS_ICONS[tone.icon];
  const showRetry = Boolean(onRetry) && canManualRetryConnection(status);
  const isHeader = variant === "header";

  return (
    <span
      role="status"
      aria-live="polite"
      data-connection-status={status}
      className={cn(
        "inline-flex items-center border",
        VARIANT_CLASSNAMES[variant],
        isHeader ? "border-border bg-surface-soft text-muted-foreground" : null,
        tone.badgeClassName,
        className,
      )}
    >
      <Icon
        size={VARIANT_ICON_SIZE[variant]}
        aria-hidden="true"
        className={tone.icon === "connecting" ? "animate-spin" : undefined}
      />
      <span>{tone.label}</span>
      {showRetry ? (
        <button
          type="button"
          onClick={onRetry}
          className="underline underline-offset-2 hover:no-underline"
        >
          {retryLabel ?? tone.label}
        </button>
      ) : null}
    </span>
  );
}
