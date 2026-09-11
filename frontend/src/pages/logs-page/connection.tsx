// 由 pages/logs-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { RefreshCwIcon, WifiIcon, WifiOffIcon } from "lucide-react";

import type { RuntimeLogsConnectionState } from "@/hooks/use-runtime-logs";

export function connectionTone(
  state: RuntimeLogsConnectionState,
  labels: {
    connecting: string;
    error: string;
    idle: string;
    live: string;
    reconnecting: string;
  },
) {
  switch (state) {
    case "open":
      return {
        badgeClassName: "border-emerald-500/30 bg-emerald-500/12 text-emerald-200",
        icon: <WifiIcon size={14} />,
        label: labels.live,
      };
    case "connecting":
      return {
        badgeClassName: "border-sky-500/30 bg-sky-500/12 text-sky-200",
        icon: <RefreshCwIcon size={14} className="animate-spin" />,
        label: labels.connecting,
      };
    case "reconnecting":
      return {
        badgeClassName: "border-amber-500/30 bg-amber-500/12 text-amber-200",
        icon: <RefreshCwIcon size={14} className="animate-spin" />,
        label: labels.reconnecting,
      };
    case "error":
      return {
        badgeClassName: "border-red-500/30 bg-red-500/12 text-red-200",
        icon: <WifiOffIcon size={14} />,
        label: labels.error,
      };
    default:
      return {
        badgeClassName: "border-[var(--border)] bg-[var(--surface-soft)] text-[var(--muted-foreground)]",
        icon: <WifiOffIcon size={14} />,
        label: labels.idle,
      };
  }
}
