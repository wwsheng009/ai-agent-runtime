import { type TFunction } from "i18next";
import { DownloadIcon, RefreshCwIcon } from "lucide-react";
import { useCallback, useEffect, useState } from "react";

import {
  correctMemoryRecord,
  deleteMemoryRecord,
  getMemoryRecords,
  getMemoryStatus,
  getMemorySummaries,
  getMemoryUsage,
  memoryExportUrl,
  rebuildMemorySummaries,
  revalidateMemoryRecord,
  type RuntimeMemoryRecord,
  type RuntimeMemoryStatus,
  type RuntimeMemorySummary,
  type RuntimeMemoryUsage,
} from "@/lib/runtime-api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

import { SettingsEmptyState } from "../settings-empty-state";
import { SettingsPanelCard } from "../settings-panel-card";

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
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const reload = useCallback(async () => {
    if (!workspacePath) {
      setStatus(null);
      setRecords([]);
      setSummaries([]);
      setUsage([]);
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const [statusResponse, recordsResponse, summariesResponse, usageResponse] =
        await Promise.all([
          getMemoryStatus(workspacePath),
          getMemoryRecords(workspacePath, { status: "active", limit: 20 }),
          getMemorySummaries(workspacePath, 10),
          getMemoryUsage(workspacePath, { limit: 20 }),
        ]);
      setStatus(statusResponse);
      setRecords(recordsResponse.items ?? []);
      setSummaries(summariesResponse.items ?? []);
      setUsage(usageResponse.items ?? []);
    } catch (loadError) {
      setError(errorMessage(loadError));
    } finally {
      setLoading(false);
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

  if (!workspacePath) return null;

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
          <Button variant="secondary" size="sm" disabled={loading} onClick={() => void rebuild()}>
            {t("harness.memoryRebuildSummary")}
          </Button>
          <Button
            variant="secondary"
            size="sm"
            onClick={() =>
              window.open(memoryExportUrl(workspacePath, "markdown"), "_blank", "noopener,noreferrer")
            }
          >
            <DownloadIcon size={14} />
            {t("harness.memoryExport")}
          </Button>
        </div>
      </div>

      {error ? (
        <p className="mt-3 rounded-card border border-danger/30 bg-danger/5 px-3 py-2 text-xs text-danger">
          {error}
        </p>
      ) : null}

      {status ? (
        <>
          <div className="mt-3 grid gap-2 text-xs sm:grid-cols-2 lg:grid-cols-4">
            <div className="rounded-card border border-border bg-surface-softer px-3 py-2">
              <div className="text-muted-foreground">{t("harness.memoryMode")}</div>
              <div className="mt-1 font-medium text-foreground">{status.mode}</div>
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
        </>
      ) : null}

      {summaries.length > 0 ? (
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

      <div className="mt-3 space-y-2">
        {records.length > 0 ? (
          records.slice(0, 10).map((record) => (
            <div key={record.id} className="rounded-card-lg border border-border bg-surface-softer px-3 py-2.5">
              <p className="text-sm leading-6 text-foreground">{record.text}</p>
              <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <Badge>{record.kind}</Badge>
                <Badge>{record.freshness_status}</Badge>
                {record.pinned ? <Badge>{t("harness.memoryPinned")}</Badge> : null}
                <span>{record.source_kind}</span>
                <span>{t("harness.memoryUseCount", { count: record.use_count ?? 0 })}</span>
                {record.last_verified_at ? <span>{record.last_verified_at}</span> : null}
              </div>
              <div className="mt-2 flex flex-wrap gap-2">
                <Button variant="secondary" size="sm" disabled={loading} onClick={() => void revalidate(record)}>
                  {t("harness.memoryRevalidate")}
                </Button>
                <Button variant="secondary" size="sm" disabled={loading} onClick={() => void correct(record)}>
                  {t("harness.memoryCorrect")}
                </Button>
                <Button variant="secondary" size="sm" disabled={loading} onClick={() => void forget(record)}>
                  {t("harness.memoryForget")}
                </Button>
              </div>
            </div>
          ))
        ) : (
          <SettingsEmptyState variant="dashed">{t("harness.memoryV2Empty")}</SettingsEmptyState>
        )}
      </div>

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
    </SettingsPanelCard>
  );
}
