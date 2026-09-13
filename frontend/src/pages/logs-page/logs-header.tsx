// 由 pages/logs-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  ArrowLeftIcon,
  BarChart3Icon,
  DatabaseIcon,
  RefreshCwIcon,
  SearchIcon,
  ShieldIcon,
  TerminalSquareIcon,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { Button } from "@/components/ui/button";
import { buttonVariants } from "@/components/ui/button-variants";
import { Select } from "@/components/ui/select";
import type { RuntimeLogsConnectionState } from "@/hooks/use-runtime-logs";
import { cn } from "@/lib/utils";
import type {
  RuntimeLogLevelFilter,
  RuntimeLogsActiveChip,
} from "@/pages/logs-page-shared";
import { connectionTone } from "@/pages/logs-page/connection";
import { CopyActionButton, LogHeaderBadge } from "@/pages/logs-page/primitives";

type LogsHeaderSectionProps = {
  activeChips: RuntimeLogsActiveChip[];
  adminToken: string;
  connectionState: RuntimeLogsConnectionState;
  copiedViewLink: boolean;
  error: string | null;
  filePath: string | null;
  follow: boolean;
  level: RuntimeLogLevelFilter;
  logFileExists: boolean;
  onAdminTokenChange: (value: string) => void;
  onClearAll: () => void;
  onClearChip: (key: RuntimeLogsActiveChip["key"]) => void;
  onCopyViewLink: () => void;
  onFollowChange: (value: boolean) => void;
  onLevelChange: (value: RuntimeLogLevelFilter) => void;
  onQueryChange: (value: string) => void;
  onRefresh: () => void;
  query: string;
  refreshing: boolean;
  streamError: string | null;
};

export function LogsHeaderSection({
  activeChips,
  adminToken,
  connectionState,
  copiedViewLink,
  error,
  filePath,
  follow,
  level,
  logFileExists,
  onAdminTokenChange,
  onClearAll,
  onClearChip,
  onCopyViewLink,
  onFollowChange,
  onLevelChange,
  onQueryChange,
  onRefresh,
  query,
  refreshing,
  streamError,
}: LogsHeaderSectionProps) {
  const { t } = useTranslation("logs");
  const { t: tCommon } = useTranslation("common");

  const connection = connectionTone(connectionState, {
    connecting: t("connectionConnecting"),
    error: t("connectionError"),
    idle: t("connectionIdle"),
    live: t("connectionLive"),
    reconnecting: t("connectionReconnecting"),
  });
  const levelFilterOptions = [
    { value: "", label: t("allLevels") },
    { value: "error", label: t("levelError") },
    { value: "warn", label: t("levelWarn") },
    { value: "info", label: t("levelInfo") },
    { value: "debug", label: t("levelDebug") },
  ] as const;

  return (
    <header className="surface-panel relative overflow-hidden rounded-panel-lg px-3 py-2 sm:px-3.5">
      <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_top_left,rgba(240,199,123,0.18),transparent_28%),radial-gradient(circle_at_right,rgba(103,215,230,0.12),transparent_22%)]" />
      <div className="relative flex flex-col gap-2">
        <div className="flex flex-col gap-2 lg:flex-row lg:items-center lg:justify-between">
          <div className="min-w-0 space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <LogHeaderBadge className="border-accent-primary-border bg-accent-primary-soft text-accent-primary">
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
              <div className="hidden h-4 w-px bg-border sm:block" />
              <h1 className="app-text-12 font-semibold tracking-[-0.03em]">
                {t("title")}
              </h1>
              <span className="app-text-9 uppercase tracking-[0.16em] text-muted-foreground">
                {t("subtitle")}
              </span>
            </div>
          </div>
          <nav className="flex flex-wrap items-center gap-2" aria-label={t("title")}>
            <Link
              to="/workspace/chats/new"
              className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}
            >
              <ArrowLeftIcon size={14} />
              {t("backToWorkspace")}
            </Link>
            <Link
              to="/usage"
              className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}
            >
              <BarChart3Icon size={14} />
              {t("usage")}
            </Link>
            <Link
              to="/runtime/config"
              className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}
            >
              <DatabaseIcon size={14} />
              Runtime
            </Link>
            <Link
              to="/"
              className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}
            >
              {t("home")}
            </Link>
          </nav>
        </div>

        <div className="flex flex-wrap items-center gap-2 rounded-card-lg border border-border bg-surface-softer p-2">
          <label className="relative min-w-[16rem] flex-1">
            <SearchIcon
              size={14}
              className="pointer-events-none absolute left-3.5 top-1/2 -translate-y-1/2 text-muted-foreground"
            />
            <input
              value={query}
              onChange={(event) => onQueryChange(event.target.value)}
              placeholder={t("searchPlaceholder")}
              aria-label={t("searchPlaceholder")}
              className="h-8 w-full rounded-field border border-border bg-black/15 pl-10 pr-4 app-text-11 text-foreground outline-none transition focus:border-accent-primary-border focus:ring-2 focus:ring-ring"
            />
          </label>

          <label className="flex h-8 min-w-[8.5rem] items-center gap-2 rounded-field border border-border bg-black/10 px-3">
            <span className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
              {t("levelLabel")}
            </span>
            <Select
              ariaLabel={t("levelLabel")}
              value={level}
              onChange={(value) => onLevelChange(value as RuntimeLogLevelFilter)}
              options={levelFilterOptions}
              className="min-w-0 flex-1"
              triggerClassName="h-full w-full border-0 bg-transparent px-0 py-0 app-text-11 shadow-none hover:border-transparent hover:bg-transparent focus-visible:ring-0"
              optionClassName="font-mono app-text-11"
            />
          </label>

          <label className="flex h-8 min-w-[11rem] flex-1 items-center gap-2 rounded-field border border-border bg-black/10 px-3 sm:flex-none">
            <span className="flex items-center gap-1.5 app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
              <ShieldIcon size={12} />
              {t("tokenLabel")}
            </span>
            <input
              type="password"
              value={adminToken}
              onChange={(event) => onAdminTokenChange(event.target.value)}
              placeholder={t("tokenPlaceholder")}
              className="min-w-0 flex-1 bg-transparent app-text-11 text-foreground outline-none"
            />
          </label>

          <div className="flex items-center gap-2">
            <Button
              variant="secondary"
              size="sm"
              className="h-8 px-3"
              onClick={onRefresh}
            >
              <RefreshCwIcon
                size={15}
                className={refreshing ? "animate-spin" : undefined}
              />
              {t("refresh")}
            </Button>
            <CopyActionButton
              copied={copiedViewLink}
              copiedLabel={tCommon("actions.copied")}
              label={t("copyLink")}
              onClick={onCopyViewLink}
            />
          </div>

          <label className="flex h-8 items-center gap-2.5 rounded-field border border-border bg-black/10 px-3 whitespace-nowrap">
            <span className="app-text-11 font-medium">{t("followLatest")}</span>
            <input
              type="checkbox"
              checked={follow}
              onChange={(event) => onFollowChange(event.target.checked)}
              className="h-4 w-4 accent-accent-primary"
            />
          </label>
        </div>

        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 app-text-11 text-muted-foreground">
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
          <div className="flex flex-wrap items-center gap-2 rounded-card border border-border/70 bg-black/8 px-2.5 py-1.5">
            <span className="app-text-9 uppercase tracking-[0.16em] text-muted-foreground">
              {t("currentView")}
            </span>
            {activeChips.map((chip) => (
              <button
                key={`${chip.key}:${chip.value}`}
                type="button"
                onClick={() => onClearChip(chip.key)}
                className="inline-flex items-center gap-2 rounded-control border border-border bg-surface-soft px-2.5 py-1 app-text-10 text-foreground transition hover:border-accent-primary-border hover:bg-accent-primary-soft"
                title={`${t("clearSearch")} ${chip.label}`}
              >
                <span className="uppercase tracking-[0.14em] text-muted-foreground">
                  {chip.label}
                </span>
                <span className="font-mono">{chip.value}</span>
                <span className="text-muted-foreground">×</span>
              </button>
            ))}
            <Button
              variant="ghost"
              size="sm"
              className="h-7 rounded-control border border-border bg-black/10 px-2.5 text-muted-foreground hover:bg-black/20 hover:text-foreground"
              onClick={onClearAll}
            >
              {t("clearAll")}
            </Button>
          </div>
        ) : null}
      </div>
    </header>
  );
}
