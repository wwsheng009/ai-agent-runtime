// 由 pages/logs-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { TerminalSquareIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { cn } from "@/lib/utils";
import type {
  RuntimeLogLevelFilter,
  RuntimeLogLevelStat,
} from "@/pages/logs-page-shared";
import {
  buildEntryContext,
  buildEntryMeta,
  buildEntrySubtitle,
  formatListTimestamp,
  levelShortLabel,
  levelStatTone,
  levelTone,
} from "@/pages/logs-page/format";
import { LogHeaderBadge } from "@/pages/logs-page/primitives";
import type { RuntimeLogEntry } from "@/types/runtime";

type LogsListPanelProps = {
  entries: RuntimeLogEntry[];
  error: string | null;
  level: RuntimeLogLevelFilter;
  levelStats: RuntimeLogLevelStat[];
  loading: boolean;
  onSelectCursor: (cursor: number) => void;
  onSelectLevel: (level: RuntimeLogLevelFilter) => void;
  resolvedLocale: "zh-CN" | "en-US";
  selectedCursor: number | null;
};

export function LogsListPanel({
  entries,
  error,
  level,
  levelStats,
  loading,
  onSelectCursor,
  onSelectLevel,
  resolvedLocale,
  selectedCursor,
}: LogsListPanelProps) {
  const { t } = useTranslation("logs");
  const subtitleLabels = {
    requestPrefix: t("requestPrefix"),
    runtimeLogFallback: t("runtimeLogFallback"),
    statusPrefix: t("statusPrefix"),
  } as const;

  return (
    <div className="surface-panel flex min-h-[22rem] flex-col overflow-hidden rounded-[0.95rem] lg:min-h-0">
      <div className="flex items-center justify-between border-b border-[var(--border)] px-3 py-2">
        <div>
          <div className="app-text-12 font-semibold tracking-[-0.02em]">
            {t("listTitle")}
          </div>
          <div className="app-text-10 text-[var(--muted-foreground)]">
            {t("listSubtitle")}
          </div>
          <div className="mt-2 flex flex-wrap items-center gap-1.5">
            {levelStats
              .filter((stat) => stat.count > 0 || stat.key !== "other")
              .map((stat) => {
                const canFilter = stat.key !== "other";
                const active = canFilter && level === stat.key;
                return (
                  <button
                    key={stat.key}
                    type="button"
                    disabled={!canFilter}
                    aria-pressed={canFilter ? active : undefined}
                    title={
                      canFilter
                        ? `${active ? t("clearSearch") : t("showOnly")} ${stat.label}`
                        : stat.label
                    }
                    onClick={() => {
                      if (!canFilter) {
                        return;
                      }
                      onSelectLevel(active ? "" : (stat.key as RuntimeLogLevelFilter));
                    }}
                    className={cn(
                      "inline-flex items-center gap-1 rounded-[0.65rem] border px-2 py-1 font-mono app-text-9 font-medium uppercase tracking-[0.09em] transition",
                      levelStatTone(stat.key),
                      stat.count === 0 ? "opacity-45" : "",
                      canFilter ? "hover:-translate-y-px" : "cursor-default",
                      active ? "ring-1 ring-[var(--accent-primary-border)]" : "",
                    )}
                  >
                    <span>{stat.shortLabel}</span>
                    <span>{stat.count}</span>
                  </button>
                );
              })}
          </div>
        </div>
        <LogHeaderBadge>{loading ? t("loading") : `${entries.length} ${t("entries")}`}</LogHeaderBadge>
      </div>
      <div className="grid grid-cols-[4.1rem_2.15rem_minmax(0,1fr)] gap-2 border-b border-[var(--border)]/70 px-3 py-2 font-mono app-text-10 uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
        <div className="text-right">{t("time")}</div>
        <div className="text-center">{t("levelShort")}</div>
        <div>{t("event")}</div>
      </div>

      <div className="flex-1 overflow-y-auto px-1.5 py-1.5">
        {loading ? (
          <div className="flex h-full items-center justify-center text-sm text-[var(--muted-foreground)]">
            {t("readingLogs")}
          </div>
        ) : error ? (
          <div className="mx-2 rounded-[0.9rem] border border-red-500/20 bg-red-500/8 p-4 text-sm text-red-100">
            <div className="font-medium">{t("logLoadFailed")}</div>
            <div className="mt-2 break-words text-red-100/80">{error}</div>
          </div>
        ) : entries.length === 0 ? (
          <div className="flex h-full flex-col items-center justify-center gap-3 px-4 text-center text-sm text-[var(--muted-foreground)]">
            <TerminalSquareIcon size={22} />
            <div>{t("noLogs")}</div>
          </div>
        ) : (
          <div className="space-y-2">
            {entries.map((entry) => {
              const active = entry.cursor === selectedCursor;
              const timestamp = formatListTimestamp(
                resolvedLocale,
                entry.timestamp,
              );
              return (
                <button
                  key={entry.cursor}
                  type="button"
                  onClick={() => onSelectCursor(entry.cursor)}
                  className={cn(
                    "group grid w-full grid-cols-[4.1rem_2.15rem_minmax(0,1fr)] gap-2 rounded-[0.65rem] border border-transparent px-2 py-1.5 text-left transition",
                    active
                      ? "border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)] shadow-[0_6px_16px_rgba(0,0,0,0.1)]"
                      : "bg-transparent hover:bg-[var(--surface-soft)]",
                  )}
                >
                  <div className="pt-0.5 text-right font-mono app-text-10 leading-4 text-[var(--muted-foreground)]">
                    {timestamp.date ? <div>{timestamp.date}</div> : null}
                    <div>{timestamp.time}</div>
                  </div>
                  <div className="flex justify-center pt-0.5">
                    <span
                      className={cn(
                        "inline-flex min-w-[1.75rem] items-center justify-center rounded-md border px-1.5 py-0.5 font-mono app-text-9 uppercase tracking-[0.14em]",
                        levelTone(entry.level),
                      )}
                    >
                      {levelShortLabel(entry.level)}
                    </span>
                  </div>
                  <div className="min-w-0">
                    <div className="truncate app-text-12-5 font-medium leading-5 tracking-[-0.01em]">
                      {entry.message || entry.raw_text}
                    </div>
                    <div className="mt-0.5 truncate font-mono app-text-10-5 leading-4 text-[var(--muted-foreground)]">
                      {buildEntryMeta(entry) || buildEntrySubtitle(entry, subtitleLabels)}
                    </div>
                    {buildEntryContext(entry) ? (
                      <div className="mt-0.5 truncate app-text-10 leading-4 text-[var(--muted-foreground)]/75">
                        {buildEntryContext(entry)}
                      </div>
                    ) : null}
                  </div>
                </button>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}
