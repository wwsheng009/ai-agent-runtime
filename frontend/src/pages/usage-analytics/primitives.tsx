// 由 pages/usage-analytics-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { buttonVariants } from "@/components/ui/button-variants";
import { Select } from "@/components/ui/select";
import { cn } from "@/lib/utils";
import type { AnalyticsCoverage } from "@/types/runtime";
import { AlertTriangleIcon, ArrowLeftIcon, BarChart3Icon, CheckCircle2Icon, DatabaseIcon, RefreshCwIcon, TerminalSquareIcon } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link, useParams, useSearchParams } from "react-router-dom";

import { formatPercent, partialReasonKey, qualityKey, shortID } from "./format";

export function AnalyticsHeader({ onRefresh, refreshing }: { onRefresh: () => void; refreshing: boolean }) {
  const { t } = useTranslation("usageAnalytics");
  const { sessionId } = useParams();
  const [searchParams] = useSearchParams();
  const listSearch = new URLSearchParams(searchParams);
  listSearch.delete("tab");
  const backTo = sessionId
    ? `/usage${listSearch.size > 0 ? `?${listSearch.toString()}` : ""}`
    : "/workspace/chats/new";
  return (
    <header className="surface-panel relative overflow-hidden rounded-panel-lg px-3 py-3 sm:px-4">
      <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_top_left,rgba(240,199,123,0.18),transparent_28%),radial-gradient(circle_at_right,rgba(103,215,230,0.12),transparent_24%)]" />
      <div className="relative flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <div className="min-w-0 space-y-1.5">
          <div className="flex flex-wrap items-center gap-2">
            <Badge className="border-accent-primary-border bg-accent-primary-soft text-accent-primary">
              <BarChart3Icon size={13} />
              {t("badge")}
            </Badge>
            <Badge>{t("independentPage")}</Badge>
          </div>
          <div>
            <h1 className="text-base font-semibold tracking-[-0.03em] sm:text-[1.1rem]">
              {sessionId ? t("detail.title") : t("title")}
            </h1>
            <p className="mt-1 max-w-3xl truncate text-sm leading-6 text-muted-foreground">
              {sessionId ? shortID(sessionId) : t("description")}
            </p>
          </div>
        </div>

        <nav className="flex flex-wrap items-center gap-2" aria-label={t("navigation")}>
          <Link
            to={backTo}
            className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}
            aria-label={sessionId ? t("actions.backToList") : t("backToWorkspace")}
          >
            <ArrowLeftIcon size={14} />
            <span className="hidden sm:inline">
              {sessionId ? t("actions.backToList") : t("backToWorkspace")}
            </span>
          </Link>
          <Link
            to="/logs"
            className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}
            aria-label={t("logs")}
            title={t("logs")}
          >
            <TerminalSquareIcon size={14} />
            <span className="hidden sm:inline">{t("logs")}</span>
          </Link>
          <Link
            to="/runtime/config"
            className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}
            aria-label={t("runtime")}
            title={t("runtime")}
          >
            <DatabaseIcon size={14} />
            <span className="hidden sm:inline">{t("runtime")}</span>
          </Link>
          <Button
            variant="secondary"
            size="sm"
            onClick={onRefresh}
            disabled={refreshing}
            aria-label={t("actions.refresh")}
            title={t("actions.refresh")}
          >
            <RefreshCwIcon size={14} className={cn(refreshing && "animate-spin")} />
            <span className="hidden sm:inline">{t("actions.refresh")}</span>
          </Button>
        </nav>
      </div>
    </header>
  );
}

export function Metric({ label, value, detail, tone }: {
  label: string;
  value: string;
  detail: string;
  tone?: "default" | "warning" | "danger";
}) {
  return (
    <div className={cn(
      "min-w-0 rounded-panel border border-border bg-surface-softer px-3 py-3 shadow-[0_12px_34px_rgba(0,0,0,0.08)]",
      tone === "warning" && "border-analytics-warning-border bg-analytics-warning-soft",
      tone === "danger" && "border-analytics-danger-border bg-analytics-danger-soft",
    )}>
      <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">{label}</div>
      <div className={cn(
        "mt-1 truncate text-2xl font-semibold tracking-[-0.03em] tabular-nums",
        tone === "warning" && "text-analytics-warning",
        tone === "danger" && "text-analytics-danger",
      )}>
        {value}
      </div>
      <div className="mt-1 text-xs leading-4 text-muted-foreground">{detail}</div>
    </div>
  );
}

export function QualityNotice({ coverage, partial, reasons }: {
  coverage: AnalyticsCoverage;
  partial: boolean;
  reasons: string[];
}) {
  const { t } = useTranslation("usageAnalytics");
  return (
    <div className={cn(
      "flex items-start gap-2 rounded-panel border px-3 py-2.5 text-sm shadow-[0_12px_34px_rgba(0,0,0,0.08)]",
      partial
        ? "border-analytics-warning-border bg-analytics-warning-soft text-analytics-warning"
        : "border-analytics-success-border bg-analytics-success-soft text-analytics-success",
    )}>
      {partial ? <AlertTriangleIcon size={16} className="mt-0.5 shrink-0" /> : <CheckCircle2Icon size={16} className="mt-0.5 shrink-0" />}
      <div className="min-w-0">
        <div className="font-medium">
          {partial ? t("quality.partialTitle") : t("quality.completeTitle")}
        </div>
        <div className="mt-0.5 text-xs text-muted-foreground">
          {t("quality.coverage", {
            sessions: formatPercent(coverage.usage_session_rate),
            requests: formatPercent(coverage.usage_request_rate),
          })}
          {reasons.length > 0 ? ` · ${reasons.map((reason) => t(partialReasonKey(reason))).join(", ")}` : ""}
        </div>
      </div>
    </div>
  );
}

export function FilterInput({ label, value, placeholder, onChange, icon, type = "text" }: {
  label: string;
  value: string;
  placeholder: string;
  onChange: (value: string) => void;
  icon?: ReactNode;
  type?: "text" | "date";
}) {
  return (
    <label className={cn("min-w-0", icon && "2xl:col-span-2")}>
      <span className="mb-1 block text-xs text-muted-foreground">{label}</span>
      <div className="relative">
        {icon ? <span className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground">{icon}</span> : null}
        <input type={type} value={value} onChange={(event) => onChange(event.target.value)} placeholder={placeholder} className={cn("h-9 w-full rounded-field border border-border bg-surface-softer px-3 text-sm outline-none transition focus:border-accent-primary-border focus:ring-2 focus:ring-ring", icon && "pl-8")} />
      </div>
    </label>
  );
}

export function FilterSelect({ label, value, options, onChange }: {
  label: string;
  value: string;
  options: readonly { value: string; label: string }[];
  onChange: (value: string) => void;
}) {
  return (
    <label className="min-w-0">
      <span className="mb-1 block text-xs text-muted-foreground">{label}</span>
      <Select
        ariaLabel={label}
        value={value}
        options={options}
        onChange={onChange}
        className="w-full min-w-0 max-w-full"
        triggerClassName="h-9 w-full min-w-0 max-w-full overflow-hidden rounded-field"
        menuClassName="max-w-[min(92vw,560px)]"
        optionClassName="truncate"
      />
    </label>
  );
}

export function UsageAnalyticsChartsFallback() {
  const { t } = useTranslation("usageAnalytics");
  return (
    <section
      aria-label={t("charts.title")}
      className="grid min-w-0 gap-2 lg:grid-cols-[minmax(0,1.7fr)_minmax(280px,0.8fr)]"
    >
      {[t("charts.trend.title"), t("charts.tokens.title")].map((title) => (
        <div key={title} className="surface-panel min-h-[260px] rounded-panel-lg p-3.5 sm:p-4">
          <h2 className="text-sm font-semibold">{title}</h2>
          <div className="flex min-h-52 items-center justify-center text-sm text-muted-foreground">
            <RefreshCwIcon size={15} className="mr-2 animate-spin" />
            {t("loading")}
          </div>
        </div>
      ))}
    </section>
  );
}

export function QualityBadge({ quality, coverage, partial }: { quality: string; coverage: number; partial: boolean }) {
  const { t } = useTranslation("usageAnalytics");
  return (
    <div>
      <Badge className={partial ? "border-analytics-warning-border bg-analytics-warning-soft text-analytics-warning" : "border-analytics-success-border bg-analytics-success-soft text-analytics-success"}>
        {t(qualityKey(quality))}
      </Badge>
      <div className="mt-1 text-xs tabular-nums text-muted-foreground">{formatPercent(coverage)}</div>
    </div>
  );
}

export function TabButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return <button type="button" role="tab" aria-selected={active} onClick={onClick} className={cn("rounded-[0.6rem] border px-3 py-1.5 text-sm transition", active ? "border-accent-primary-border bg-accent-primary-soft text-foreground" : "border-transparent text-muted-foreground hover:bg-surface-soft hover:text-foreground")}>{children}</button>;
}
