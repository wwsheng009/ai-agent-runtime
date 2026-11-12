import { type TFunction } from "i18next";
import { DownloadIcon, RefreshCwIcon } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

import {
  activateMemorySummary,
  correctMemoryRecord,
  deleteMemoryRecord,
  downloadMemoryExport,
  getMemoryJobs,
  getMemoryRecallTraces,
  getMemoryRecords,
  getMemoryStatus,
  getMemorySummaries,
  getMemoryUsage,
  purgeMemoryTurnWindows,
  rebuildMemorySummaries,
  resetMemory,
  revalidateMemoryRecord,
  restoreMemoryRecord,
  RuntimeApiError,
  setMemorySessionMode,
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
import {
  MemoryV2Alerts,
  MemoryV2Diagnostics,
  MemoryV2SessionGovernance,
  MemoryV2StatusGrid,
  MemoryV2Summaries,
  MemoryV2UsageAndTraces,
} from "./memory-v2-sections";
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
  const [notice, setNotice] = useState<string | null>(null);
  const [sessionID, setSessionID] = useState("");
  const [sessionMode, setSessionMode] = useState<"enabled" | "disabled" | "polluted">("enabled");
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
      setNotice(null);
      return;
    }
    const requestId = ++requestSeq.current;
    setLoading(true);
    setError(null);
    setNotice(null);
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

  async function activateRevision(summary: RuntimeMemorySummary) {
    if (!workspacePath) return;
    setLoading(true);
    setError(null);
    try {
      await activateMemorySummary(summary.id, workspacePath);
      await reload();
      setNotice(
        t("harness.memoryActivateSummaryDone", {
          revision: String(summary.revision),
        }),
      );
    } catch (actionError) {
      setError(errorMessage(actionError));
    } finally {
      setLoading(false);
    }
  }

  async function applySessionMode() {
    if (!workspacePath) return;
    const id = sessionID.trim();
    if (!id) {
      setError(t("harness.memorySessionIDRequired"));
      return;
    }
    setLoading(true);
    setError(null);
    try {
      await setMemorySessionMode(id, sessionMode, workspacePath);
      await reload();
      setNotice(
        t("harness.memorySessionModeApplied", {
          session: id,
          mode: sessionMode,
        }),
      );
    } catch (actionError) {
      setError(errorMessage(actionError));
    } finally {
      setLoading(false);
    }
  }

  async function purgeWindows(all: boolean) {
    if (!workspacePath) return;
    const id = sessionID.trim();
    if (!all && !id) {
      setError(t("harness.memorySessionIDRequired"));
      return;
    }
    if (!window.confirm(all
      ? t("harness.memoryPurgeAllWindowsConfirm")
      : t("harness.memoryPurgeSessionWindowsConfirm", { session: id }))) {
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const result = await purgeMemoryTurnWindows(
        workspacePath,
        all ? { all: true } : { sessionId: id },
      );
      await reload();
      setNotice(
        t("harness.memoryPurgeWindowsDone", {
          windows: String(result.windows_deleted),
          digests: String(result.digests_released),
        }),
      );
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

      <MemoryV2Alerts error={error} notice={notice} />

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
          <MemoryV2StatusGrid t={t} status={status} />
          <MemoryV2Diagnostics t={t} status={status} jobs={jobs} />
          <MemoryV2SessionGovernance
            t={t}
            sessionID={sessionID}
            sessionMode={sessionMode}
            disabled={actionsDisabled}
            onSessionIDChange={setSessionID}
            onSessionModeChange={setSessionMode}
            onApplySessionMode={() => void applySessionMode()}
            onPurgeSession={() => void purgeWindows(false)}
            onPurgeAll={() => void purgeWindows(true)}
          />
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

      {v2Available ? (
        <MemoryV2Summaries
          t={t}
          summaries={summaries}
          disabled={actionsDisabled}
          onActivate={(summary) => void activateRevision(summary)}
        />
      ) : null}

      {v2Available ? <div className="mt-3 space-y-2">
        {records.length > 0 ? (
          records.slice(0, 10).map((record) => (
            <div key={record.id} className="rounded-card-lg border border-border bg-surface-softer px-3 py-2.5">
              <p className="text-sm leading-6 text-foreground">{record.text}</p>
              <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <Badge>{record.kind}</Badge>
                <Badge>{record.freshness_status}</Badge>
                {status.mode === "on" && record.sensitivity === "private" ? <Badge>{t("harness.memoryPrivate")}</Badge> : null}
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

      {v2Available ? <MemoryV2UsageAndTraces t={t} usage={usage} traces={traces} /> : null}
      </div>
    </SettingsPanelCard>
  );
}
