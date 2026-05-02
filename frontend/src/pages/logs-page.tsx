import {
  ArrowLeftIcon,
  CheckIcon,
  CopyIcon,
  RefreshCwIcon,
  SearchIcon,
  ShieldIcon,
  TerminalSquareIcon,
  WifiIcon,
  WifiOffIcon,
} from "lucide-react";
import { lazy, startTransition, Suspense, useState, type ComponentProps } from "react";
import { Link, useSearchParams } from "react-router-dom";

import { Button } from "@/components/ui/button";
import { buttonVariants } from "@/components/ui/button-variants";
import { Select } from "@/components/ui/select";
import { useAppSettings } from "@/core/settings";
import {
  type RuntimeLogsConnectionState,
  useRuntimeLogs,
} from "@/hooks/use-runtime-logs";
import { formatLogTimestamp as formatLogTimestampWithLocale } from "@/i18n/format";
import { useTranslation } from "react-i18next";
import {
  buildRuntimeLogsActiveChips,
  buildRuntimeLogIdentifierRows,
  buildRuntimeLogLevelStats,
  buildRuntimeLogsShareState,
  readRuntimeLogsUrlState,
  type RuntimeLogLevelKey,
  type RuntimeLogLevelFilter,
  type RuntimeLogsActiveChip,
  type RuntimeLogsUrlState,
  writeRuntimeLogsUrlState,
} from "@/pages/logs-page-shared";
import type { LogsPageDetailLabels } from "@/pages/logs-page-detail-panel.i18n";
import type { RuntimeLogEntry } from "@/types/runtime";
import { cn } from "@/lib/utils";

const LogsPageDetailPanel = lazy(() =>
  import("@/pages/logs-page-detail-panel").then((module) => ({
    default: module.LogsPageDetailPanel,
  })),
);

function LogHeaderBadge({ className, children }: ComponentProps<"span">) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-soft)] px-2 py-0.5 app-text-9 font-semibold uppercase tracking-[0.12em] text-[var(--muted-foreground)]",
        className,
      )}
    >
      {children}
    </span>
  );
}

function formatListTimestamp(
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

function formatDetailValue(value: unknown, noneLabel: string) {
  if (value === undefined || value === null || value === "") {
    return noneLabel;
  }
  if (typeof value === "string") {
    return value;
  }
  return JSON.stringify(value, null, 2);
}

function levelTone(level?: string) {
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

function levelShortLabel(level?: string) {
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

function levelStatTone(level: RuntimeLogLevelKey) {
  if (level === "other") {
    return "border-[var(--border)] bg-black/10 text-[var(--muted-foreground)]";
  }
  return levelTone(level);
}

function connectionTone(
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

function buildEntrySubtitle(
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

function buildEntryMeta(entry: RuntimeLogEntry) {
  return [
    entry.request_id?.trim() ? entry.request_id : "",
    entry.provider?.trim() ? entry.provider : "",
    entry.model?.trim() ? entry.model : "",
    typeof entry.response_status_code === "number"
      ? String(entry.response_status_code)
      : "",
  ].filter(Boolean).join("  ·  ");
}

function buildEntryContext(entry: RuntimeLogEntry) {
  return [
    entry.module?.trim() ? entry.module : "",
    entry.caller?.trim() ? entry.caller : "",
  ].filter(Boolean).join("  ·  ");
}

function detailRows(entry: RuntimeLogEntry | null, labels: LogsPageDetailLabels) {
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

type CopyActionButtonProps = {
  copied: boolean;
  copiedLabel: string;
  label: string;
  onClick: () => void;
};

function CopyActionButton({
  copied,
  copiedLabel,
  label,
  onClick,
}: CopyActionButtonProps) {
  return (
    <Button
      variant="ghost"
      size="sm"
      className="h-7 rounded-[0.65rem] border border-[var(--border)] bg-black/10 px-2.5 text-[var(--muted-foreground)] hover:bg-black/20 hover:text-[var(--foreground)]"
      onClick={onClick}
    >
      {copied ? <CheckIcon size={13} /> : <CopyIcon size={13} />}
      {copied ? copiedLabel : label}
    </Button>
  );
}

function LogsPageDetailPanelFallback({ message }: { message: string }) {
  return (
    <div className="flex flex-1 items-center justify-center px-6 text-center text-sm text-[var(--muted-foreground)]">
      {message}
    </div>
  );
}

export function LogsPage() {
  const { resolvedLocale } = useAppSettings();
  const { t } = useTranslation("logs");
  const { t: tCommon } = useTranslation("common");
  const [searchParams, setSearchParams] = useSearchParams();
  const urlState = readRuntimeLogsUrlState(searchParams);
  const {
    adminToken,
    connectionState,
    entries,
    error,
    filePath,
    loading,
    logFileExists,
    refreshing,
    refresh,
    selectedEntry,
    setAdminToken,
    streamError,
  } = useRuntimeLogs({
    follow: urlState.follow,
    level: urlState.level,
    onSelectedCursorChange: setSelectedCursor,
    query: urlState.query,
    selectedCursor: urlState.cursor,
  });
  const [copiedSection, setCopiedSection] = useState<string | null>(null);

  const connection = connectionTone(connectionState, {
    connecting: t("connectionConnecting"),
    error: t("connectionError"),
    idle: t("connectionIdle"),
    live: t("connectionLive"),
    reconnecting: t("connectionReconnecting"),
  });
  const follow = urlState.follow;
  const level = urlState.level;
  const query = urlState.query;
  const selectedCursor = urlState.cursor;
  const subtitleLabels = {
    requestPrefix: t("requestPrefix"),
    runtimeLogFallback: t("runtimeLogFallback"),
    statusPrefix: t("statusPrefix"),
  } as const;
  const detailLabels: LogsPageDetailLabels = {
    copied: tCommon("actions.copied"),
    summary: t("copiedLog"),
    insights: t("insights"),
    insightsHelp: t("insightsHelp"),
    identifiers: t("identifiers"),
    metadata: t("metadata"),
    responsePreview: t("responsePreview"),
    extraFields: t("extraFields"),
    rawJson: t("rawJson"),
    clearSearch: tCommon("actions.clearSearch"),
    copyValue: tCommon("actions.copyValue"),
    copyMetadata: tCommon("actions.copyMetadata"),
    copyPreview: tCommon("actions.copyPreview"),
    copyFields: tCommon("actions.copyFields"),
    copyJson: tCommon("actions.copyJson"),
    cancelFilter: t("cancelFilter"),
    filterSameValue: t("filterSameValue"),
    copiedLog: t("copiedLog"),
    identifiersHelp: t("identifiersHelp"),
    cursorLabel: t("cursorLabel"),
    levelFallback: t("levelFallback"),
    requestPrefix: t("requestPrefix"),
    runtimeLogFallback: t("runtimeLogFallback"),
    statusPrefix: t("statusPrefix"),
    timestamp: t("timestamp"),
    level: t("level"),
    module: t("module"),
    caller: t("caller"),
    requestId: t("requestId"),
    traceId: t("traceId"),
    sessionId: t("sessionId"),
    provider: t("provider"),
    model: t("model"),
    method: t("method"),
    url: t("url"),
    responseStatus: t("responseStatus"),
    upstreamError: t("upstreamError"),
    cacheHit: t("cacheHit"),
    cacheHitValueHit: t("cacheHitValueHit"),
    cacheHitValueMiss: t("cacheHitValueMiss"),
    skillExposureMode: t("skillExposureMode"),
    finalFunctionCount: t("finalFunctionCount"),
    routedSkillCount: t("routedSkillCount"),
    candidateCount: t("candidateCount"),
    exposedFunctionCount: t("exposedFunctionCount"),
  };
  const metadataRows = detailRows(selectedEntry, detailLabels);
  const formattedMetadataRows = metadataRows.map(([label, value]) => ({
    label: label ?? "",
    value: formatDetailValue(value, tCommon("states.none")),
  }));
  const identifierRows = buildRuntimeLogIdentifierRows(selectedEntry, {
    identifiers: {
      request_id: t("identifierRequest"),
      trace_id: t("identifierTrace"),
      session_id: t("identifierSession"),
    },
  });
  const levelStats = buildRuntimeLogLevelStats(entries, {
    levelStats: {
      error: { label: t("levelError"), shortLabel: t("levelShortError") },
      warn: { label: t("levelWarn"), shortLabel: t("levelShortWarn") },
      info: { label: t("levelInfo"), shortLabel: t("levelShortInfo") },
      debug: { label: t("levelDebug"), shortLabel: t("levelShortDebug") },
      other: { label: t("levelOther"), shortLabel: t("levelShortOther") },
    },
  });
  const newestCursor = entries[0]?.cursor ?? null;
  const activeChips = buildRuntimeLogsActiveChips(urlState, newestCursor, {
    activeChips: {
      query: t("chipQuery"),
      level: t("chipLevel"),
      follow: t("chipFollow"),
      cursor: t("chipCursor"),
    },
    activeChipValues: {
      follow: t("activeChipOff"),
    },
  });
  const shareState = buildRuntimeLogsShareState(urlState, newestCursor);
  const rawJsonText = selectedEntry?.raw
    ? JSON.stringify(selectedEntry.raw, null, 2)
    : (selectedEntry?.raw_text ?? "");
  const metadataText = metadataRows
    .map(([label, value]) => `${label}: ${formatDetailValue(value, tCommon("states.none"))}`)
    .join("\n");
  const responsePreviewText = selectedEntry?.response_body_preview ?? "";
  const extraFieldsText = selectedEntry?.fields
    ? JSON.stringify(selectedEntry.fields, null, 2)
    : "";
  const selectedEntrySubtitle = selectedEntry
    ? buildEntrySubtitle(selectedEntry, subtitleLabels)
    : "";
  const selectedLevelTone = selectedEntry ? levelTone(selectedEntry.level) : "";
  const levelFilterOptions = [
    { value: "", label: t("allLevels") },
    { value: "error", label: t("levelError") },
    { value: "warn", label: t("levelWarn") },
    { value: "info", label: t("levelInfo") },
    { value: "debug", label: t("levelDebug") },
  ] as const;
  const shareSearchParams = writeRuntimeLogsUrlState(
    searchParams,
    shareState,
  );
  const sharePath = shareSearchParams.toString()
    ? `/logs?${shareSearchParams.toString()}`
    : "/logs";
  const shareUrl =
    typeof window === "undefined"
      ? sharePath
      : `${window.location.origin}${sharePath}`;

  function updateUrlState(
    updater: (currentState: RuntimeLogsUrlState) => RuntimeLogsUrlState,
  ) {
    startTransition(() => {
      setSearchParams(
        (currentSearchParams) =>
          writeRuntimeLogsUrlState(
            currentSearchParams,
            updater(readRuntimeLogsUrlState(currentSearchParams)),
          ),
        { replace: true },
      );
    });
  }

  function setQuery(
    value: string | ((currentValue: string) => string),
  ) {
    updateUrlState((currentState) => ({
      ...currentState,
      cursor: null,
      query:
        typeof value === "function" ? value(currentState.query) : value,
    }));
  }

  function setLevel(value: RuntimeLogLevelFilter) {
    updateUrlState((currentState) => ({
      ...currentState,
      cursor: null,
      level: value,
    }));
  }

  function setFollow(
    value: boolean | ((currentValue: boolean) => boolean),
  ) {
    updateUrlState((currentState) => {
      const nextFollow =
        typeof value === "function" ? value(currentState.follow) : value;
      return {
        ...currentState,
        cursor: nextFollow ? newestCursor : currentState.cursor,
        follow: nextFollow,
      };
    });
  }

  function setSelectedCursor(
    value: number | null | ((currentValue: number | null) => number | null),
  ) {
    updateUrlState((currentState) => ({
      ...currentState,
      cursor:
        typeof value === "function" ? value(currentState.cursor) : value,
    }));
  }

  function toggleIdentifierQuery(value: string) {
    const normalizedValue = value.trim();
    if (!normalizedValue) {
      return;
    }

    setQuery((currentQuery) =>
      currentQuery.trim() === normalizedValue ? "" : normalizedValue,
    );
  }

  async function handleCopy(sectionKey: string, value: string) {
    if (!value.trim()) {
      return;
    }
    try {
      await navigator.clipboard.writeText(value);
      setCopiedSection(sectionKey);
      window.setTimeout(() => {
        setCopiedSection((currentSection) =>
          currentSection === sectionKey ? null : currentSection,
        );
      }, 1500);
    } catch {
      setCopiedSection(null);
    }
  }

  function clearActiveChip(chipKey: RuntimeLogsActiveChip["key"]) {
    switch (chipKey) {
      case "query":
        setQuery("");
        return;
      case "level":
        setLevel("");
        return;
      case "follow":
        setFollow(true);
        return;
      case "cursor":
        setSelectedCursor(newestCursor);
        return;
    }
  }

  function clearAllActiveState() {
    updateUrlState(() => ({
      query: "",
      level: "",
      follow: true,
      cursor: null,
    }));
  }

  return (
    <div className="min-h-screen [background:var(--workspace-shell-bg)] text-[var(--foreground)] lg:h-dvh lg:overflow-hidden">
      <div className="mx-auto flex min-h-screen w-full max-w-[1760px] flex-col gap-2 px-2.5 py-2.5 sm:px-3 lg:h-full lg:min-h-0 lg:px-3">
        <header className="surface-panel relative overflow-hidden rounded-[0.95rem] px-3 py-2 sm:px-3.5">
          <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_top_left,rgba(240,199,123,0.18),transparent_28%),radial-gradient(circle_at_right,rgba(103,215,230,0.12),transparent_22%)]" />
          <div className="relative flex flex-col gap-2">
            <div className="flex flex-col gap-2 lg:flex-row lg:items-center lg:justify-between">
              <div className="min-w-0 space-y-1">
                <div className="flex flex-wrap items-center gap-2">
                  <LogHeaderBadge className="border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)] text-[var(--accent-primary)]">
                    <TerminalSquareIcon size={13} />
                    {t("title")}
                  </LogHeaderBadge>
                  <LogHeaderBadge className={connection.badgeClassName}>
                    {connection.icon}
                    {connection.label}
                  </LogHeaderBadge>
                  <LogHeaderBadge
                    className={cn(
                      logFileExists
                        ? "border-emerald-500/25 bg-emerald-500/10 text-emerald-200"
                        : "border-amber-500/25 bg-amber-500/10 text-amber-200",
                    )}
                  >
                  {logFileExists
                    ? tCommon("states.fileDetected")
                    : tCommon("states.waitingForLogFile")}
                  </LogHeaderBadge>
                  <div className="hidden h-4 w-px bg-[var(--border)] sm:block" />
                  <h1 className="app-text-12 font-semibold tracking-[-0.03em]">
                    {t("title")}
                  </h1>
                  <span className="app-text-9 uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
                    {t("subtitle")}
                  </span>
                </div>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <Link
                  to="/workspace/chats/new"
                  className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}
                >
                  <ArrowLeftIcon size={14} />
                  {t("backToWorkspace")}
                </Link>
                <Link
                  to="/"
                  className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}
                >
                  {t("home")}
                </Link>
              </div>
            </div>

            <div className="flex flex-wrap items-center gap-2 rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-softer)] p-2">
              <label className="relative min-w-[16rem] flex-1">
                <SearchIcon
                  size={14}
                  className="pointer-events-none absolute left-3.5 top-1/2 -translate-y-1/2 text-[var(--muted-foreground)]"
                />
                <input
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                  placeholder={t("searchPlaceholder")}
                  className="h-8 w-full rounded-[0.7rem] border border-[var(--border)] bg-black/15 pl-10 pr-4 app-text-11 text-[var(--foreground)] outline-none transition focus:border-[var(--accent-primary-border)] focus:ring-2 focus:ring-[var(--ring)]"
                />
              </label>

              <label className="flex h-8 min-w-[8.5rem] items-center gap-2 rounded-[0.7rem] border border-[var(--border)] bg-black/10 px-3">
                <span className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                  {t("levelLabel")}
                </span>
                <Select
                  ariaLabel={t("levelLabel")}
                  value={level}
                  onChange={(value) => setLevel(value as RuntimeLogLevelFilter)}
                  options={levelFilterOptions}
                  className="min-w-0 flex-1"
                  triggerClassName="h-full w-full border-0 bg-transparent px-0 py-0 app-text-11 shadow-none hover:border-transparent hover:bg-transparent focus-visible:ring-0"
                  optionClassName="font-mono app-text-11"
                />
              </label>

              <label className="flex h-8 min-w-[11rem] flex-1 items-center gap-2 rounded-[0.7rem] border border-[var(--border)] bg-black/10 px-3 sm:flex-none">
                <span className="flex items-center gap-1.5 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                  <ShieldIcon size={12} />
                  {t("tokenLabel")}
                </span>
                <input
                  type="password"
                  value={adminToken}
                  onChange={(event) => setAdminToken(event.target.value)}
                  placeholder={t("tokenPlaceholder")}
                  className="min-w-0 flex-1 bg-transparent app-text-11 text-[var(--foreground)] outline-none"
                />
              </label>

              <div className="flex items-center gap-2">
                <Button
                  variant="secondary"
                  size="sm"
                  className="h-8 px-3"
                  onClick={refresh}
                >
                  <RefreshCwIcon
                    size={15}
                    className={refreshing ? "animate-spin" : undefined}
                  />
                  {t("refresh")}
                </Button>
                <CopyActionButton
                  copied={copiedSection === "view_link"}
                  copiedLabel={tCommon("actions.copied")}
                  label={t("copyLink")}
                  onClick={() => handleCopy("view_link", shareUrl)}
                />
              </div>

              <label className="flex h-8 items-center gap-2.5 rounded-[0.7rem] border border-[var(--border)] bg-black/10 px-3 whitespace-nowrap">
                <span className="app-text-11 font-medium">{t("followLatest")}</span>
                <input
                  type="checkbox"
                  checked={follow}
                  onChange={(event) => setFollow(event.target.checked)}
                  className="h-4 w-4 accent-[var(--accent-primary)]"
                />
              </label>
            </div>

            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 app-text-11 text-[var(--muted-foreground)]">
              <span className="min-w-0 flex-1 truncate">
                {t("fileLabel")}
                {" "}
                {filePath || t("fileFallback")}
              </span>
              {streamError ? (
                <span className="text-amber-200">{t("streamHint")} {streamError}</span>
              ) : null}
              {error ? <span className="text-red-200">{t("loadError")} {error}</span> : null}
            </div>

            {activeChips.length > 0 ? (
              <div className="flex flex-wrap items-center gap-2 rounded-[0.8rem] border border-[var(--border)]/70 bg-black/8 px-2.5 py-1.5">
                <span className="app-text-9 uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
                  {t("currentView")}
                </span>
                {activeChips.map((chip) => (
                  <button
                    key={`${chip.key}:${chip.value}`}
                    type="button"
                    onClick={() => clearActiveChip(chip.key)}
                    className="inline-flex items-center gap-2 rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-soft)] px-2.5 py-1 app-text-10 text-[var(--foreground)] transition hover:border-[var(--accent-primary-border)] hover:bg-[var(--accent-primary-soft)]"
                    title={`${t("clearSearch")} ${chip.label}`}
                  >
                    <span className="uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                      {chip.label}
                    </span>
                    <span className="font-mono">{chip.value}</span>
                    <span className="text-[var(--muted-foreground)]">×</span>
                  </button>
                ))}
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-7 rounded-[0.65rem] border border-[var(--border)] bg-black/10 px-2.5 text-[var(--muted-foreground)] hover:bg-black/20 hover:text-[var(--foreground)]"
                  onClick={clearAllActiveState}
                >
                  {t("clearAll")}
                </Button>
              </div>
            ) : null}
          </div>
        </header>

        <section className="grid min-h-0 flex-1 gap-2 lg:min-h-0 lg:flex-1 xl:grid-cols-[21rem_minmax(0,1fr)] 2xl:grid-cols-[22rem_minmax(0,1fr)]">
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
                          title={
                            canFilter
                              ? `${active ? t("clearSearch") : t("showOnly")} ${stat.label}`
                              : stat.label
                          }
                          onClick={() => {
                            if (!canFilter) {
                              return;
                            }
                            setLevel(active ? "" : (stat.key as RuntimeLogLevelFilter));
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
                        onClick={() => setSelectedCursor(entry.cursor)}
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

          <div className="surface-panel flex min-h-[22rem] flex-col overflow-hidden rounded-[0.95rem] lg:min-h-0">
            <div className="border-b border-[var(--border)] px-3.5 py-2">
              <div className="app-text-13 font-semibold tracking-[-0.02em]">{t("detailsTitle")}</div>
              <div className="mt-1 app-text-11 text-[var(--muted-foreground)]">
                {t("detailsDescription")}
              </div>
            </div>

            {!selectedEntry ? (
              <div className="flex flex-1 items-center justify-center px-6 text-center text-sm text-[var(--muted-foreground)]">
                {t("selectPrompt")}
              </div>
            ) : (
              <Suspense fallback={<LogsPageDetailPanelFallback message={t("detailsLoading")} />}>
                <LogsPageDetailPanel
                  copiedSection={copiedSection}
                  extraFieldsText={extraFieldsText}
                  identifierRows={identifierRows}
                  metadataRows={formattedMetadataRows}
                  metadataText={metadataText}
                  onClearQuery={() => setQuery("")}
                  onCopy={handleCopy}
                  onToggleIdentifierQuery={toggleIdentifierQuery}
                  query={query}
                  rawJsonText={rawJsonText}
                  responsePreviewText={responsePreviewText}
                  selectedEntry={selectedEntry}
                  selectedEntrySubtitle={selectedEntrySubtitle}
                  selectedLevelTone={selectedLevelTone}
                  labels={detailLabels}
                />
              </Suspense>
            )}
          </div>
        </section>
      </div>
    </div>
  );
}
