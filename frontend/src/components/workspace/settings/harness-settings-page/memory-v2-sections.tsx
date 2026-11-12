import { type TFunction } from "i18next";

import type {
  RuntimeMemoryJob,
  RuntimeMemoryRecallTrace,
  RuntimeMemoryStatus,
  RuntimeMemorySummary,
  RuntimeMemoryUsage,
} from "@/lib/runtime-api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

function formatCounts(values?: Record<string, number>) {
  const entries = Object.entries(values ?? {});
  if (entries.length === 0) return "-";
  return entries.map(([key, value]) => `${key}=${value}`).join(" · ");
}

export function MemoryV2Alerts({ error, notice }: { error: string | null; notice: string | null }) {
  return (
    <>
      {error ? (
        <p role="alert" className="mt-3 rounded-card border border-danger/30 bg-danger/5 px-3 py-2 text-xs text-danger">
          {error}
        </p>
      ) : null}
      {notice ? (
        <p role="status" className="mt-3 rounded-card border border-accent/30 bg-accent/5 px-3 py-2 text-xs text-foreground">
          {notice}
        </p>
      ) : null}
    </>
  );
}

export function MemoryV2StatusGrid({
  t,
  status,
}: {
  t: TFunction<"settings">;
  status: RuntimeMemoryStatus;
}) {
  return (
    <div className="mt-3 grid gap-2 text-xs sm:grid-cols-2 lg:grid-cols-4">
      <div className="rounded-card border border-border bg-surface-softer px-3 py-2">
        <div className="text-muted-foreground">{t("harness.memoryMode")}</div>
        <div className="mt-1 font-medium text-foreground">{status.mode}</div>
      </div>
      <div className="rounded-card border border-border bg-surface-softer px-3 py-2">
        <div className="text-muted-foreground">{t("harness.memoryScope")}</div>
        <div className="mt-1 font-medium text-foreground">{status.scope}</div>
      </div>
      <div className="rounded-card border border-border bg-surface-softer px-3 py-2">
        <div className="text-muted-foreground">{t("harness.memoryRecordCount")}</div>
        <div className="mt-1 font-medium text-foreground">{status.total ?? 0}</div>
      </div>
      <div className="rounded-card border border-border bg-surface-softer px-3 py-2">
        <div className="text-muted-foreground">{t("harness.memoryInjectionMode")}</div>
        <div className="mt-1 font-medium text-foreground">{status.injection_mode || "-"}</div>
      </div>
      <div className="rounded-card border border-border bg-surface-softer px-3 py-2">
        <div className="text-muted-foreground">{t("harness.memoryAutoLearning")}</div>
        <div className="mt-1 font-medium text-foreground">
          {status.generation_enabled
            ? t("harness.memoryAutoLearningEnabled", { trigger: status.generation_trigger || "-" })
            : t("harness.memoryDisabled")}
        </div>
      </div>
      <div className="rounded-card border border-border bg-surface-softer px-3 py-2">
        <div className="text-muted-foreground">{t("harness.memoryManualMode")}</div>
        <div className="mt-1 font-medium text-foreground">
          {status.manual_enabled ? t("harness.memoryManualExplicit") : t("harness.memoryDisabled")}
        </div>
      </div>
      <div className="rounded-card border border-border bg-surface-softer px-3 py-2">
        <div className="text-muted-foreground">{t("harness.memorySummary")}</div>
        <div className="mt-1 font-medium text-foreground">
          {t("harness.memoryRevision", { revision: String(status.summary_revision ?? 0) })} ·{" "}
          {status.summary_status || "-"} · {status.summary_freshness || "-"}
        </div>
      </div>
      <div className="rounded-card border border-border bg-surface-softer px-3 py-2">
        <div className="text-muted-foreground">{t("harness.memoryPinnedBudget")}</div>
        <div className="mt-1 font-medium text-foreground">
          {status.pinned_count ?? 0} · {Math.round((status.pinned_budget_ratio ?? 0) * 100)}%
        </div>
      </div>
    </div>
  );
}

export function MemoryV2Diagnostics({
  t,
  status,
  jobs,
}: {
  t: TFunction<"settings">;
  status: RuntimeMemoryStatus;
  jobs: RuntimeMemoryJob[];
}) {
  const failedJobs = jobs.filter((job) => job.status === "failed" || job.last_error);
  return (
    <>
      <div className="mt-3 grid gap-2 text-xs sm:grid-cols-2">
        <div className="rounded-card border border-border bg-surface-softer px-3 py-2">
          <div className="text-muted-foreground">{t("harness.memoryTurnDigests")}</div>
          <div className="mt-1 font-medium text-foreground">{formatCounts(status.turn_digests)}</div>
        </div>
        <div className="rounded-card border border-border bg-surface-softer px-3 py-2">
          <div className="text-muted-foreground">{t("harness.memoryTurnWindows")}</div>
          <div className="mt-1 font-medium text-foreground">{formatCounts(status.turn_windows)}</div>
        </div>
      </div>
      {status.oldest_pending_digest_age_seconds ||
      status.pending_digest_sessions ||
      status.pending_digest_backlog_batches ||
      status.recovered_digest_sessions ? (
        <div className="mt-2 flex flex-wrap gap-2 text-xs text-muted-foreground">
          {status.oldest_pending_digest_age_seconds ? (
            <span>
              {t("harness.memoryPendingDigestAge", {
                seconds: String(status.oldest_pending_digest_age_seconds),
              })}
            </span>
          ) : null}
          {status.pending_digest_sessions ? (
            <span>
              {t("harness.memoryPendingDigestSessions", {
                count: status.pending_digest_sessions ?? 0,
              })}
            </span>
          ) : null}
          {status.pending_digest_backlog_batches ? (
            <span>
              {t("harness.memoryBacklogBatches", {
                count: status.pending_digest_backlog_batches ?? 0,
              })}
            </span>
          ) : null}
          {status.recovered_digest_sessions ? (
            <span>
              {t("harness.memoryRecoveredSessions", {
                count: status.recovered_digest_sessions ?? 0,
              })}
            </span>
          ) : null}
        </div>
      ) : null}
      {status.jobs && Object.keys(status.jobs).length > 0 ? (
        <p className="mt-2 text-xs text-muted-foreground">
          {t("harness.memoryJobs")}:{" "}
          {Object.entries(status.jobs)
            .map(([key, value]) => `${key}=${value}`)
            .join(" · ")}
        </p>
      ) : null}
      {failedJobs.length > 0 ? (
        <div className="mt-2 space-y-1 text-xs">
          <div className="font-medium text-foreground">{t("harness.memoryJobErrors")}</div>
          {failedJobs.slice(0, 5).map((job) => (
            <div key={job.id} className="text-danger">
              {job.kind} · {job.status} · {job.last_error || "-"}
            </div>
          ))}
        </div>
      ) : null}
    </>
  );
}

export function MemoryV2SessionGovernance({
  t,
  sessionID,
  sessionMode,
  disabled,
  onSessionIDChange,
  onSessionModeChange,
  onApplySessionMode,
  onPurgeSession,
  onPurgeAll,
}: {
  t: TFunction<"settings">;
  sessionID: string;
  sessionMode: "enabled" | "disabled" | "polluted";
  disabled: boolean;
  onSessionIDChange: (value: string) => void;
  onSessionModeChange: (value: "enabled" | "disabled" | "polluted") => void;
  onApplySessionMode: () => void;
  onPurgeSession: () => void;
  onPurgeAll: () => void;
}) {
  return (
    <div className="mt-3 rounded-card border border-border px-3 py-2.5">
      <div className="text-xs font-medium text-foreground">
        {t("harness.memorySessionGovernance")}
      </div>
      <div className="mt-2 flex flex-wrap gap-2">
        <input
          aria-label={t("harness.memorySessionIDPlaceholder")}
          className="min-w-0 flex-1 rounded-card border border-border bg-background px-2.5 py-1.5 text-xs text-foreground outline-none focus:border-accent"
          placeholder={t("harness.memorySessionIDPlaceholder")}
          value={sessionID}
          onChange={(event) => onSessionIDChange(event.target.value)}
        />
        <select
          aria-label={t("harness.memorySessionMode")}
          className="rounded-card border border-border bg-background px-2.5 py-1.5 text-xs text-foreground outline-none focus:border-accent"
          value={sessionMode}
          onChange={(event) =>
            onSessionModeChange(event.target.value as "enabled" | "disabled" | "polluted")
          }
        >
          <option value="enabled">{t("harness.memorySessionModeEnabled")}</option>
          <option value="disabled">{t("harness.memorySessionModeDisabled")}</option>
          <option value="polluted">{t("harness.memorySessionModePolluted")}</option>
        </select>
        <Button
          variant="secondary"
          size="sm"
          disabled={disabled || !sessionID.trim()}
          onClick={onApplySessionMode}
        >
          {t("harness.memorySessionModeApply")}
        </Button>
      </div>
      <div className="mt-2 flex flex-wrap gap-2">
        <Button
          variant="secondary"
          size="sm"
          disabled={disabled || !sessionID.trim()}
          onClick={onPurgeSession}
        >
          {t("harness.memoryPurgeSessionWindows")}
        </Button>
        <Button variant="secondary" size="sm" disabled={disabled} onClick={onPurgeAll}>
          {t("harness.memoryPurgeAllWindows")}
        </Button>
      </div>
      <p className="mt-2 text-xs text-muted-foreground">
        {t("harness.memorySessionGovernanceHint")}
      </p>
    </div>
  );
}

export function MemoryV2Summaries({
  t,
  summaries,
  disabled,
  onActivate,
}: {
  t: TFunction<"settings">;
  summaries: RuntimeMemorySummary[];
  disabled: boolean;
  onActivate: (summary: RuntimeMemorySummary) => void;
}) {
  if (summaries.length === 0) return null;
  return (
    <div className="mt-3 space-y-2">
      {summaries.slice(0, 3).map((summary) => (
        <div key={summary.id} className="rounded-card border border-border px-3 py-2 text-xs">
          <div className="flex flex-wrap items-center gap-2">
            <Badge>{summary.status}</Badge>
            <Badge>{summary.freshness_status}</Badge>
            {summary.model ? (
              <span className="text-muted-foreground">{summary.model}</span>
            ) : null}
            <span className="text-muted-foreground">
              {t("harness.memorySummaryMeta", {
                revision: String(summary.revision),
                tokens: String(summary.token_estimate),
                items: String(summary.source_count),
              })}
            </span>
          </div>
          <p className="mt-2 line-clamp-3 whitespace-pre-wrap text-foreground">{summary.text}</p>
          {summary.status !== "active" ? (
            <div className="mt-2">
              <Button variant="secondary" size="sm" disabled={disabled} onClick={() => onActivate(summary)}>
                {t("harness.memoryActivateSummary")}
              </Button>
            </div>
          ) : null}
        </div>
      ))}
    </div>
  );
}

export function MemoryV2UsageAndTraces({
  t,
  usage,
  traces,
}: {
  t: TFunction<"settings">;
  usage: RuntimeMemoryUsage[];
  traces: RuntimeMemoryRecallTrace[];
}) {
  return (
    <>
      {usage.length > 0 ? (
        <div className="mt-3 space-y-1 text-xs text-muted-foreground">
          <div className="font-medium text-foreground">{t("harness.memoryUsageTitle")}</div>
          {usage.slice(0, 8).map((item) => (
            <div key={item.id} className="flex flex-wrap gap-2">
              <Badge>{item.use_kind}</Badge>
              <span>{item.memory_id}</span>
              <span>{item.used_at}</span>
            </div>
          ))}
        </div>
      ) : null}
      {traces.length > 0 ? (
        <div className="mt-3 space-y-1 text-xs text-muted-foreground">
          <div className="font-medium text-foreground">{t("harness.memoryRecallTraceTitle")}</div>
          {traces.slice(0, 5).map((trace) => (
            <div key={trace.id} className="rounded-card border border-border px-2 py-1.5">
              <div className="flex flex-wrap gap-2">
                <span className="text-foreground">{trace.query_preview || trace.query_hash.slice(0, 12)}</span>
                <span>
                  {t("harness.memoryRecallTraceMeta", {
                    candidates: String(trace.candidate_count),
                    filtered: String(trace.filtered_count),
                    tokens: String(trace.token_estimate),
                    budget: String(trace.token_budget),
                  })}
                </span>
              </div>
              {trace.dropped && trace.dropped.length > 0 ? (
                <div>
                  {t("harness.memoryRecallDropped", {
                    reason: trace.dropped
                      .slice(0, 4)
                      .map((item) => `${item.id}:${item.reason}`)
                      .join(", "),
                  })}
                </div>
              ) : null}
            </div>
          ))}
        </div>
      ) : null}
    </>
  );
}
