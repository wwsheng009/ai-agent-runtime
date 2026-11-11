import { type TFunction } from "i18next";
import { DownloadIcon, RefreshCwIcon } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

import {
  correctMemoryRecord,
  deleteMemoryRecord,
  downloadMemoryExport,
  getMemoryJobs,
  getMemoryRecallTraces,
  getMemoryRecords,
  getMemoryStatus,
  getMemorySummaries,
  getMemoryUsage,
  rebuildMemorySummaries,
  resetMemory,
  revalidateMemoryRecord,
  restoreMemoryRecord,
  RuntimeApiError,
  setMemoryRecordPinned,
  type RuntimeMemoryRecord,
  type RuntimeMemoryJob,
  type RuntimeMemoryRecallTrace,
  type RuntimeMemoryStatus,
  type RuntimeMemorySummary,
  type RuntimeMemoryUsage,
} from "@/lib/runtime-api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

import { SettingsEmptyState } from "../settings-empty-state";
import { SettingsPanelCard } from "../settings-panel-card";
import { MemoryV2WhyNot } from "./memory-v2-why-not";

function errorMessage(error: unknown) {
  return error instanceof Error && error.message.trim()
    ? error.message
    : String(error);
}

export function MemoryV2Panel({
  t,
  workspacePath,
}: {
  t: TFunction<"settings">;
  workspacePath: string;
}) {
  const [status, setStatus] = useState<RuntimeMemoryStatus | null>(null);
  const [records, setRecords] = useState<RuntimeMemoryRecord[]>([]);
  const [summaries, setSummaries] = useState<RuntimeMemorySummary[]>([]);
  const [usage, setUsage] = useState<RuntimeMemoryUsage[]>([]);
  const [jobs, setJobs] = useState<RuntimeMemoryJob[]>([]);
  const [traces, setTraces] = useState<RuntimeMemoryRecallTrace[]>([]);
  const [deletedRecords, setDeletedRecords] = useState<RuntimeMemoryRecord[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const requestSeq = useRef(0);

  const reload = useCallback(async () => {
    if (!workspacePath) {
      setStatus(null);
      setRecords([]);
      setSummaries([]);
      setUsage([]);
      setJobs([]);
      setTraces([]);
      setDeletedRecords([]);
      return;
    }
    const requestId = ++requestSeq.current;
    setLoading(true);
    setError(null);
    try {
      const [statusResult, recordsResult, deletedResult, summariesResult, usageResult, jobsResult, tracesResult] =
        await Promise.allSettled([
          getMemoryStatus(workspacePath),
          getMemoryRecords(workspacePath, { status: "active", limit: 20 }),
          getMemoryRecords(workspacePath, { status: "deleted", limit: 20 }),
          getMemorySummaries(workspacePath, 10),
          getMemoryUsage(workspacePath, { limit: 20 }),
          getMemoryJobs(workspacePath, 20),
          getMemoryRecallTraces(workspacePath, { limit: 20 }),
        ]);
      if (requestId !== requestSeq.current) return;
      if (statusResult.status === "fulfilled") setStatus(statusResult.value);
      if (recordsResult.status === "fulfilled") setRecords(recordsResult.value.items ?? []);
      if (deletedResult.status === "fulfilled") setDeletedRecords(deletedResult.value.items ?? []);
      if (summariesResult.status === "fulfilled") setSummaries(summariesResult.value.items ?? []);
      if (usageResult.status === "fulfilled") setUsage(usageResult.value.items ?? []);
      if (jobsResult.status === "fulfilled") setJobs(jobsResult.value.items ?? []);
      if (tracesResult.status === "fulfilled") setTraces(tracesResult.value.items ?? []);
      const failures = [statusResult, recordsResult, summariesResult]
        .filter((result): result is PromiseRejectedResult => result.status === "rejected")
        .map((result) => errorMessage(result.reason));
      for (const optional of [deletedResult, usageResult, jobsResult, tracesResult]) {
        if (optional.status !== "rejected") continue;
        if (optional.reason instanceof RuntimeApiError && optional.reason.status === 403) {
          continue;
        }
        failures.push(errorMessage(optional.reason));
      }
      if (failures.length > 0) setError(failures.join("; "));
    } catch (loadError) {
      if (requestId === requestSeq.current) setError(errorMessage(loadError));
    } finally {
      if (requestId === requestSeq.current) setLoading(false);
    }
  }, [workspacePath]);

  useEffect(() => {
    void reload();
  }, [reload]);

  async function rebuild() {
    if (!workspacePath) return;
    setLoading(true);
    try {
      await rebuildMemorySummaries(workspacePath);
      await reload();
    } catch (actionError) {
      setError(errorMessage(actionError));
    } finally {
      setLoading(false);
    }
  }

  async function revalidate(record: RuntimeMemoryRecord) {
    if (!workspacePath) return;
    setLoading(true);
    try {
      await revalidateMemoryRecord(record.id, workspacePath);
      await reload();
    } catch (actionError) {
      setError(errorMessage(actionError));
    } finally {
      setLoading(false);
    }
  }

  async function togglePin(record: RuntimeMemoryRecord) {
    if (!workspacePath) return;
    setLoading(true);
    setError(null);
    try {
      await setMemoryRecordPinned(record.id, !record.pinned, workspacePath);
      await reload();
    } catch (actionError) {
      setError(errorMessage(actionError));
    } finally {
      setLoading(false);
    }
  }

  async function restore(record: RuntimeMemoryRecord) {
    if (!workspacePath) return;
    setLoading(true);
    setError(null);
    try {
      await restoreMemoryRecord(record.id, workspacePath);
      await reload();
    } catch (actionError) {
      setError(errorMessage(actionError));
    } finally {
      setLoading(false);
    }
  }

  async function correct(record: RuntimeMemoryRecord) {
    if (!workspacePath) return;
    const nextText = window.prompt(t("harness.memoryCorrectPrompt"), record.text);
    if (!nextText?.trim() || nextText.trim() === record.text.trim()) return;
    setLoading(true);
    try {
      await correctMemoryRecord(record.id, nextText.trim(), workspacePath);
      await reload();
    } catch (actionError) {
      setError(errorMessage(actionError));
    } finally {
      setLoading(false);
    }
  }

  async function forget(record: RuntimeMemoryRecord) {
    if (!workspacePath) return;
    if (!window.confirm(t("harness.memoryForgetConfirm", { text: record.text }))) {
      return;
    }
    setLoading(true);
    try {
      await deleteMemoryRecord(record.id, workspacePath);
      await reload();
    } catch (actionError) {
      setError(errorMessage(actionError));
    } finally {
      setLoading(false);
    }
  }

  async function exportMemory(format: "jsonl" | "markdown") {
    if (!workspacePath) return;
    setLoading(true);
    setError(null);
    try {
      await downloadMemoryExport(workspacePath, format);
    } catch (actionError) {
      setError(errorMessage(actionError));
    } finally {
      setLoading(false);
    }
  }

  async function resetAll() {
    if (!workspacePath || !window.confirm(t("harness.memoryResetConfirm"))) return;
    setLoading(true);
    setError(null);
    try {
      await resetMemory(workspacePath);
      await reload();
    } catch (actionError) {
      setError(errorMessage(actionError));
    } finally {
      setLoading(false);
    }
  }

  if (!workspacePath) return null;
  const v2Available = status !== null && status.mode !== "off" && !status.fallback;
  const actionsDisabled = loading || !v2Available;
  const privateBadge = status?.mode === "on";
  const failedJobs = jobs.filter((job) => job.status === "failed" || job.last_error);

  return (
    <SettingsPanelCard title={t("harness.memoryV2Title")}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs leading-5 text-muted-foreground">
          {t("harness.memoryV2Description")}
        </p>
        <div className="flex flex-wrap gap-2">
          <Button
            variant="secondary"
            size="sm"
            disabled={loading}
            onClick={() => void reload()}
          >
            <RefreshCwIcon size={14} />
            {t("harness.refresh")}
          </Button>
          <Button variant="secondary" size="sm" disabled={actionsDisabled} onClick={() => void rebuild()}>
            {t("harness.memoryRebuildSummary")}
          </Button>
          <Button
            variant="secondary"
            size="sm"
            disabled={actionsDisabled}
            onClick={() => void exportMemory("jsonl")}
          >
            <DownloadIcon size={14} />
            {t("harness.memoryExportJsonl")}
          </Button>
          <Button
            variant="secondary"
            size="sm"
            disabled={actionsDisabled}
            onClick={() => void exportMemory("markdown")}
          >
            <DownloadIcon size={14} />
            {t("harness.memoryExportMarkdown")}
          </Button>
          <Button
            variant="secondary"
            size="sm"
            className="text-danger"
            disabled={actionsDisabled}
            onClick={() => void resetAll()}
          >
            {t("harness.memoryReset")}
          </Button>
        </div>
      </div>

      {error ? (
        <p role="alert" className="mt-3 rounded-card border border-danger/30 bg-danger/5 px-3 py-2 text-xs text-danger">
          {error}
        </p>
      ) : null}

      {status?.fallback || status?.mode === "off" ? (
        <p className="mt-3 rounded-card border border-border bg-surface-softer px-3 py-2 text-xs text-muted-foreground">
          {t("harness.memoryV2Unavailable", {
            reason: status?.fallback_reason || status?.mode || "",
          })}
        </p>
      ) : null}

      <div aria-busy={loading}>
      {v2Available ? (
        <>
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
              <div className="text-muted-foreground">{t("harness.memorySummary")}</div>
              <div className="mt-1 font-medium text-foreground">
                {t("harness.memoryRevision", { revision: String(status.summary_revision ?? 0) })} ·{" "}
                {status.summary_status || "-"} ·{" "}
                {status.summary_freshness || "-"}
              </div>
            </div>
            <div className="rounded-card border border-border bg-surface-softer px-3 py-2">
              <div className="text-muted-foreground">{t("harness.memoryPinnedBudget")}</div>
              <div className="mt-1 font-medium text-foreground">
                {status.pinned_count ?? 0} · {Math.round((status.pinned_budget_ratio ?? 0) * 100)}%
              </div>
            </div>
          </div>
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
      ) : null}

      {v2Available ? (
        <MemoryV2WhyNot
          t={t}
          workspacePath={workspacePath}
          disabled={actionsDisabled}
          onError={setError}
        />
      ) : null}

      {v2Available && summaries.length > 0 ? (
        <div className="mt-3 space-y-2">
          {summaries.slice(0, 3).map((summary) => (
            <div key={summary.id} className="rounded-card border border-border px-3 py-2 text-xs">
              <div className="flex flex-wrap items-center gap-2">
                <Badge>{summary.status}</Badge>
                <Badge>{summary.freshness_status}</Badge>
                <span className="text-muted-foreground">
                  {t("harness.memorySummaryMeta", {
                    revision: String(summary.revision),
                    tokens: String(summary.token_estimate),
                    items: String(summary.source_count),
                  })}
                </span>
              </div>
              <p className="mt-2 line-clamp-3 whitespace-pre-wrap text-foreground">{summary.text}</p>
            </div>
          ))}
        </div>
      ) : null}

      {v2Available ? <div className="mt-3 space-y-2">
        {records.length > 0 ? (
          records.slice(0, 10).map((record) => (
            <div key={record.id} className="rounded-card-lg border border-border bg-surface-softer px-3 py-2.5">
              <p className="text-sm leading-6 text-foreground">{record.text}</p>
              <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <Badge>{record.kind}</Badge>
                <Badge>{record.freshness_status}</Badge>
                {privateBadge && record.sensitivity === "private" ? <Badge>{t("harness.memoryPrivate")}</Badge> : null}
                {record.pinned ? <Badge>{t("harness.memoryPinned")}</Badge> : null}
                <span>{record.source_kind}</span>
                {record.source_session_id ? (
                  <span>{t("harness.memorySourceSession", { id: record.source_session_id })}</span>
                ) : null}
                <span>{t("harness.memoryUseCount", { count: record.use_count ?? 0 })}</span>
                {record.last_verified_at ? <span>{record.last_verified_at}</span> : null}
                {record.stale_reason ? <span>{record.stale_reason}</span> : null}
                {(record.tags ?? []).map((tag) => (
                  <Badge key={`${record.id}-${tag}`}>#{tag}</Badge>
                ))}
              </div>
              <div className="mt-2 flex flex-wrap gap-2">
                <Button variant="secondary" size="sm" disabled={actionsDisabled} onClick={() => void revalidate(record)}>
                  {t("harness.memoryRevalidate")}
                </Button>
                <Button variant="secondary" size="sm" disabled={actionsDisabled} onClick={() => void togglePin(record)}>
                  {record.pinned ? t("harness.memoryUnpin") : t("harness.memoryPin")}
                </Button>
                <Button variant="secondary" size="sm" disabled={actionsDisabled} onClick={() => void correct(record)}>
                  {t("harness.memoryCorrect")}
                </Button>
                <Button variant="secondary" size="sm" disabled={actionsDisabled} onClick={() => void forget(record)}>
                  {t("harness.memoryForget")}
                </Button>
              </div>
            </div>
          ))
        ) : !loading && !error ? (
          <SettingsEmptyState variant="dashed">{t("harness.memoryV2Empty")}</SettingsEmptyState>
        ) : null}
      </div> : null}

      {v2Available && deletedRecords.length > 0 ? (
        <div className="mt-3 space-y-2">
          <div className="text-xs font-medium text-foreground">{t("harness.memoryDeletedTitle")}</div>
          {deletedRecords.slice(0, 10).map((record) => (
            <div key={record.id} className="rounded-card border border-border px-3 py-2 text-xs">
              <div className="text-muted-foreground">{record.text}</div>
              <div className="mt-1 flex flex-wrap items-center gap-2 text-muted-foreground">
                <Badge>{record.status}</Badge>
                <span>{record.id}</span>
                <Button variant="secondary" size="sm" disabled={actionsDisabled} onClick={() => void restore(record)}>
                  {t("harness.memoryRestore")}
                </Button>
              </div>
            </div>
          ))}
        </div>
      ) : null}

      {v2Available && usage.length > 0 ? (
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
      {v2Available && traces.length > 0 ? (
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
      </div>
    </SettingsPanelCard>
  );
}
