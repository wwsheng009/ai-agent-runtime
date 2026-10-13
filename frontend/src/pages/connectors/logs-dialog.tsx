// 连接器运行日志抽屉：stats 汇总头部 + entries 列表（时间/级别色标/方向/摘要，等宽字体）。
//
// 端点：GET /api/connectors/{id}/logs?limit=50（@/api/runtime/connectors）。
// 交互：打开时加载 + 手动刷新 + 可选 5s 自动刷新（打开时启用、关闭即停）；Escape 关闭。
// 分支：加载中 / 加载失败（保留已加载条目）/ 空态 / 无 runtime 快照
// （ConnectorView.runtime 是 additive 字段，缺失时头部降级提示而不是显示零计数）。

import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  CONNECTOR_LOGS_DEFAULT_LIMIT,
  readConnectorLogs,
} from "@/api/runtime/connectors";
import { Button } from "@/components/ui/button";
import { CheckboxInput } from "@/components/ui/checkbox";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { readAdminToken } from "@/lib/admin-token";
import { cn } from "@/lib/utils";
import type {
  ConnectorLogEntry,
  ConnectorStats,
  ConnectorView,
} from "@/types/runtime";

const AUTO_REFRESH_MS = 5000;

const statChipClass =
  "rounded-chip border border-border bg-surface-soft px-2 py-0.5 text-muted-foreground";

/** 级别色标（语义 token；debug 与未知级别降级为中性色）。 */
function levelTone(level: string): string {
  switch (level.trim().toLowerCase()) {
    case "error":
      return "border-analytics-danger-border bg-analytics-danger-soft text-analytics-danger";
    case "warn":
      return "border-analytics-warning-border bg-analytics-warning-soft text-analytics-warning";
    case "info":
      return "border-analytics-info-border bg-analytics-info-soft text-analytics-info";
    default:
      return "border-border bg-surface-soft text-muted-foreground";
  }
}

interface ConnectorLogsDialogProps {
  open: boolean;
  connector: ConnectorView | null;
  onClose: () => void;
}

export function ConnectorLogsDialog({
  open,
  connector,
  onClose,
}: ConnectorLogsDialogProps) {
  const { t } = useTranslation("connectors");
  const connectorId = connector?.id ?? "";

  const [entries, setEntries] = useState<ConnectorLogEntry[]>([]);
  const [stats, setStats] = useState<ConnectorStats | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [autoRefresh, setAutoRefresh] = useState(false);

  const load = useCallback(async () => {
    if (!connectorId) {
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const payload = await readConnectorLogs(
        connectorId,
        CONNECTOR_LOGS_DEFAULT_LIMIT,
        { adminToken: readAdminToken() },
      );
      setEntries(payload.entries);
      setStats(payload.stats);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setLoading(false);
    }
  }, [connectorId]);

  // 打开（或切换连接器）时重置状态并立即加载。
  useEffect(() => {
    if (!open) {
      return;
    }
    setEntries([]);
    setStats(null);
    setError(null);
    setAutoRefresh(false);
    void load();
  }, [open, connectorId, load]);

  // 可选自动刷新：仅在打开且勾选时生效，关闭/卸载即清除定时器。
  useEffect(() => {
    if (!open || !autoRefresh) {
      return;
    }
    const timer = window.setInterval(() => {
      void load();
    }, AUTO_REFRESH_MS);
    return () => window.clearInterval(timer);
  }, [open, autoRefresh, load]);

  // Escape 关闭（读取中不拦截：日志查看可随时退出）。
  useEffect(() => {
    if (!open) {
      return;
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        onClose();
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [open, onClose]);

  if (!open || !connector) {
    return null;
  }

  const hasRuntimeView = connector.runtime !== undefined;
  const runtimeStats = stats ?? connector.runtime?.stats ?? null;

  const levelLabel = (level: string) => {
    switch (level.trim().toLowerCase()) {
      case "debug":
        return t("logs.levelDebug");
      case "warn":
        return t("logs.levelWarn");
      case "error":
        return t("logs.levelError");
      case "info":
        return t("logs.levelInfo");
      default:
        return level || t("logs.levelInfo");
    }
  };

  const directionLabel = (direction: string) => {
    switch (direction.trim().toLowerCase()) {
      case "in":
        return t("logs.directionIn");
      case "out":
        return t("logs.directionOut");
      case "system":
        return t("logs.directionSystem");
      default:
        return direction;
    }
  };

  return (
    <DialogOverlay onDismiss={onClose}>
      <DialogPanel
        role="dialog"
        aria-modal="true"
        aria-label={t("logs.title")}
        className="max-w-3xl"
      >
        <header className="border-b border-border px-4 py-3">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <h2 className="text-sm font-semibold text-foreground">
                {t("logs.title")}
              </h2>
              <p className="mt-0.5 truncate text-xs text-muted-foreground">
                {connector.name} · {connector.id}
              </p>
            </div>
            <Button variant="ghost" size="sm" onClick={onClose}>
              {t("logs.close")}
            </Button>
          </div>

          <div className="mt-2 flex flex-wrap items-center gap-1.5 text-xs">
            {hasRuntimeView ? (
              runtimeStats ? (
                <>
                  <span className={statChipClass}>
                    {t("stats.inbound", {
                      value: String(runtimeStats.inbound),
                    })}
                  </span>
                  <span className={statChipClass}>
                    {t("stats.outbound", {
                      value: String(runtimeStats.outbound),
                    })}
                  </span>
                  <span
                    className={cn(
                      statChipClass,
                      runtimeStats.errors > 0
                        ? "border-analytics-danger-border bg-analytics-danger-soft text-analytics-danger"
                        : undefined,
                    )}
                  >
                    {t("stats.errors", { value: String(runtimeStats.errors) })}
                  </span>
                  <span className={statChipClass}>
                    {t("stats.dedupSkipped", {
                      value: String(runtimeStats.dedup_skipped),
                    })}
                  </span>
                  <span className={statChipClass}>
                    {t("stats.queueFull", {
                      value: String(runtimeStats.queue_full),
                    })}
                  </span>
                </>
              ) : null
            ) : (
              <span className="text-muted-foreground">{t("logs.noRuntime")}</span>
            )}
          </div>
          {hasRuntimeView && runtimeStats?.last_activity_at ? (
            <p className="mt-1 font-mono text-[11px] text-muted-foreground">
              {t("logs.lastActivity", { time: runtimeStats.last_activity_at })}
            </p>
          ) : null}
          {hasRuntimeView && runtimeStats?.last_error ? (
            <p className="mt-1 break-words font-mono text-[11px] text-analytics-danger">
              {t("logs.lastError", { message: runtimeStats.last_error })}
            </p>
          ) : null}
        </header>

        <div className="flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto px-4 py-3">
          {error ? (
            <p className="rounded-card border border-accent-orange/25 bg-accent-orange/8 px-3 py-2 text-sm text-muted-foreground">
              {t("logs.loadFailed", { message: error })}
            </p>
          ) : null}
          {loading && entries.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("logs.loading")}</p>
          ) : entries.length === 0 ? (
            error ? null : (
              <p className="text-sm text-muted-foreground">{t("logs.empty")}</p>
            )
          ) : (
            <ul className="flex flex-col gap-1.5">
              {entries.map((entry, index) => (
                <li
                  key={`${entry.time}-${index}`}
                  className="flex flex-col gap-1 rounded-card border border-border bg-surface-soft px-2.5 py-2"
                >
                  <div className="flex flex-wrap items-center gap-2 font-mono text-[11px]">
                    <span className="text-muted-foreground">{entry.time}</span>
                    <span
                      className={cn(
                        "rounded-chip border px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-[0.08em]",
                        levelTone(entry.level),
                      )}
                    >
                      {levelLabel(entry.level)}
                    </span>
                    <span className="text-muted-foreground">
                      {directionLabel(entry.direction)}
                    </span>
                    {entry.chat ? (
                      <span className="text-muted-foreground">
                        {t("logs.chat", { value: entry.chat })}
                      </span>
                    ) : null}
                  </div>
                  <p className="whitespace-pre-wrap break-words font-mono text-[11px] leading-5 text-foreground">
                    {entry.summary}
                  </p>
                </li>
              ))}
            </ul>
          )}
        </div>

        <footer className="flex flex-wrap items-center justify-between gap-2 border-t border-border px-4 py-3">
          <label className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
            <CheckboxInput
              checked={autoRefresh}
              aria-label={t("logs.autoRefresh")}
              onChange={(event) => setAutoRefresh(event.target.checked)}
            />
            {t("logs.autoRefresh")}
          </label>
          <div className="flex items-center gap-2">
            <Button variant="ghost" size="sm" onClick={onClose}>
              {t("logs.close")}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void load()}
              disabled={loading}
            >
              {loading ? t("logs.refreshing") : t("logs.refresh")}
            </Button>
          </div>
        </footer>
      </DialogPanel>
    </DialogOverlay>
  );
}
