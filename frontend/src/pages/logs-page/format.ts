// 由 pages/logs-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { formatLogTimestamp as formatLogTimestampWithLocale } from "@/i18n/format";
import type { LogsPageDetailLabels } from "@/pages/logs-page-detail-panel.i18n";
import type { RuntimeLogLevelKey } from "@/pages/logs-page-shared";
import type { RuntimeLogEntry } from "@/types/runtime";

export function formatListTimestamp(
  locale: "zh-CN" | "en-US",
  value?: string,
) {
  const formatted = formatLogTimestampWithLocale(locale, value);
  const parts = formatted.split(" ");
  if (parts.length >= 2) {
    return {
      date: parts[0] ?? "",
      time: parts.slice(1).join(" "),
    };
  }
  return {
    date: "",
    time: formatted,
  };
}

export function formatDetailValue(value: unknown, noneLabel: string) {
  if (value === undefined || value === null || value === "") {
    return noneLabel;
  }
  if (typeof value === "string") {
    return value;
  }
  return JSON.stringify(value, null, 2);
}

export function levelTone(level?: string) {
  switch ((level ?? "").toLowerCase()) {
    case "error":
      return "border-red-500/30 bg-red-500/10 text-red-200";
    case "warn":
      return "border-amber-500/30 bg-amber-500/10 text-amber-200";
    case "debug":
      return "border-sky-500/30 bg-sky-500/10 text-sky-200";
    case "info":
      return "border-emerald-500/30 bg-emerald-500/10 text-emerald-200";
    default:
      return "border-[var(--border)] bg-[var(--surface-soft)] text-[var(--muted-foreground)]";
  }
}

export function levelShortLabel(level?: string) {
  switch ((level ?? "").toLowerCase()) {
    case "error":
      return "ERR";
    case "warn":
      return "WRN";
    case "debug":
      return "DBG";
    case "info":
      return "INF";
    default:
      return "LOG";
  }
}

export function levelStatTone(level: RuntimeLogLevelKey) {
  if (level === "other") {
    return "border-[var(--border)] bg-black/10 text-[var(--muted-foreground)]";
  }
  return levelTone(level);
}

export function buildEntrySubtitle(
  entry: RuntimeLogEntry,
  labels: Pick<
    LogsPageDetailLabels,
    "requestPrefix" | "runtimeLogFallback" | "statusPrefix"
  >,
) {
  const parts = [
    entry.request_id?.trim() ? `${labels.requestPrefix} ${entry.request_id}` : "",
    entry.provider?.trim() ? `${entry.provider}${entry.model ? ` / ${entry.model}` : ""}` : "",
    typeof entry.response_status_code === "number"
      ? `${labels.statusPrefix} ${entry.response_status_code}`
      : "",
  ].filter(Boolean);

  if (parts.length === 0) {
    return entry.module?.trim() || labels.runtimeLogFallback;
  }
  return parts.join("  ·  ");
}

export function buildEntryMeta(entry: RuntimeLogEntry) {
  return [
    entry.request_id?.trim() ? entry.request_id : "",
    entry.provider?.trim() ? entry.provider : "",
    entry.model?.trim() ? entry.model : "",
    typeof entry.response_status_code === "number"
      ? String(entry.response_status_code)
      : "",
  ].filter(Boolean).join("  ·  ");
}

export function buildEntryContext(entry: RuntimeLogEntry) {
  return [
    entry.module?.trim() ? entry.module : "",
    entry.caller?.trim() ? entry.caller : "",
  ].filter(Boolean).join("  ·  ");
}

export function detailRows(entry: RuntimeLogEntry | null, labels: LogsPageDetailLabels) {
  if (!entry) {
    return [];
  }
  return [
    [labels.timestamp, entry.timestamp],
    [labels.level, entry.level],
    [labels.module, entry.module],
    [labels.caller, entry.caller],
    [labels.requestId, entry.request_id],
    [labels.traceId, entry.trace_id],
    [labels.sessionId, entry.session_id],
    [labels.provider, entry.provider],
    [labels.model, entry.model],
    [labels.method, entry.method],
    [labels.url, entry.url],
    [
      labels.responseStatus,
      typeof entry.response_status_code === "number"
        ? String(entry.response_status_code)
        : "",
    ],
    [labels.upstreamError, entry.upstream_error],
  ].filter(([, value]) => String(value ?? "").trim() !== "");
}
