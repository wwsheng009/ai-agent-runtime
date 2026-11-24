import { RefreshCcwIcon } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";

import { RuntimeApiError } from "@/api/runtime/shared";
import { Badge } from "@/components/ui/badge";
import {
  getRuntimeTeamRun,
  getRuntimeTeamRunPreflight,
  isRuntimeTeamProductCapabilitySupported,
  startRuntimeTeamRun,
  type RuntimeTeamProductCapabilities,
  type RuntimeTeamRunPreflightFinding,
  type RuntimeTeamRunPreflightReport,
  type RuntimeTeamRun,
} from "@/lib/runtime-api";
import { cn } from "@/lib/utils";

import { TeamStudioPanel } from "./team-studio/team-studio-panel";

const terminalStatuses = new Set([
  "done",
  "failed",
  "partially_completed",
  "canceled",
]);
const maxTerminalSummaryObservations = 8;
const minRunPollDelayMs = 1_000;
const maxRunPollDelayMs = 8_000;
const maxTransientErrorPollDelayMs = 8_000;
const terminalSummaryPollDelayMs = 5_000;

function isTerminal(run: RuntimeTeamRun): boolean {
  return terminalStatuses.has(run.status.trim().toLowerCase());
}

function isTemporaryRunReadFailure(error: unknown): boolean {
  if (error instanceof RuntimeApiError) {
    return error.status === 429 || error.status === 503;
  }
  return (
    error instanceof TypeError ||
    (typeof DOMException !== "undefined" &&
      error instanceof DOMException &&
      error.name === "TimeoutError")
  );
}

function isDocumentHidden(): boolean {
  return document.visibilityState === "hidden";
}

function shouldObserve(
  run: RuntimeTeamRun,
  terminalSummaryObservations = 0,
): boolean {
  if (run.status.trim().toLowerCase() === "draft") {
    return false;
  }
  return (
    !isTerminal(run) ||
    (run.summary_state === "pending" &&
      terminalSummaryObservations < maxTerminalSummaryObservations)
  );
}

function createIdempotencyKey(runId: string): string {
  const cryptoApi = globalThis.crypto;
  if (cryptoApi && typeof cryptoApi.randomUUID === "function") {
    return `web-team-run-start-${runId}-${cryptoApi.randomUUID()}`;
  }
  return `web-team-run-start-${runId}-${Date.now()}`;
}

function getOrCreateStartKey(runId: string, version: number): string {
  const storageKey = `team-run-start:${runId}:${version}`;
  try {
    const existing = window.sessionStorage.getItem(storageKey);
    if (existing) {
      return existing;
    }
  } catch {
    // Storage may be unavailable in privacy-restricted browser contexts.
  }
  const idempotencyKey = createIdempotencyKey(runId);
  try {
    window.sessionStorage.setItem(storageKey, idempotencyKey);
  } catch {
    // Keep this attempt usable even when sessionStorage is disabled.
  }
  return idempotencyKey;
}

function clearStartKey(runId: string, version: number): void {
  try {
    window.sessionStorage.removeItem(`team-run-start:${runId}:${version}`);
  } catch {
    // Storage may be unavailable in privacy-restricted browser contexts.
  }
}

function formatTimestamp(value: string | null | undefined): string {
  if (!value) {
    return "—";
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return date.toLocaleString();
}

type RuntimeTeamRunsViewProps = {
  capabilities: RuntimeTeamProductCapabilities;
  error: string | null;
  isLoading: boolean;
  onOpenLegacyTeam: (teamId: string) => void;
  onRetry: () => void;
  onRunUpdated: (run: RuntimeTeamRun) => void;
  runs: RuntimeTeamRun[];
};

export function RuntimeTeamRunsView({
  capabilities,
  error,
  isLoading,
  onOpenLegacyTeam,
  onRetry,
  onRunUpdated,
  runs,
}: RuntimeTeamRunsViewProps) {
  const { t } = useTranslation("workspace");
  const [searchParams, setSearchParams] = useSearchParams();
  const selectedRunId = searchParams.get("team_run_id") ?? "";
  const [studioOpen, setStudioOpen] = useState(false);
  const [linkedRun, setLinkedRun] = useState<RuntimeTeamRun | null>(null);
  const [loadingRunId, setLoadingRunId] = useState("");
  const [linkedRunError, setLinkedRunError] = useState<{
    runId: string;
    message: string;
  } | null>(null);
  const [selectionRequestVersion, setSelectionRequestVersion] = useState(0);
  const onRunUpdatedRef = useRef(onRunUpdated);
  onRunUpdatedRef.current = onRunUpdated;
  const tRef = useRef(t);
  tRef.current = t;

  const selectRun = useCallback(
    (runId: string, replace = false) => {
      setSearchParams(
        (current) => {
          const next = new URLSearchParams(current);
          next.set("team_view", "runs");
          if (runId) {
            next.set("team_run_id", runId);
          } else {
            next.delete("team_run_id");
          }
          return next;
        },
        { replace },
      );
    },
    [setSearchParams],
  );

  const runFromList = runs.find((run) => run.id === selectedRunId) ?? null;
  const selectedRun =
    runFromList ?? (linkedRun?.id === selectedRunId ? linkedRun : null);

  useEffect(() => {
    if (!selectedRunId && !isLoading && runs.length > 0) {
      selectRun(runs[0].id, true);
    }
  }, [isLoading, runs, selectRun, selectedRunId]);

  useEffect(() => {
    if (!selectedRunId || runFromList) {
      setLinkedRun(null);
      setLoadingRunId("");
      setLinkedRunError(null);
      return;
    }

    const controller = new AbortController();
    setLoadingRunId(selectedRunId);
    setLinkedRunError(null);
    void getRuntimeTeamRun(selectedRunId, { signal: controller.signal })
      .then((response) => {
        if (controller.signal.aborted) {
          return;
        }
        setLinkedRun(response.run);
        onRunUpdatedRef.current(response.run);
      })
      .catch((loadError: unknown) => {
        if (controller.signal.aborted) {
          return;
        }
        setLinkedRunError({
          runId: selectedRunId,
          message:
            loadError instanceof Error
              ? loadError.message
              : tRef.current(
                  "panels.teamsPanels.runs.selectedRunUnavailable",
                ),
        });
      })
      .finally(() => {
        if (!controller.signal.aborted) {
          setLoadingRunId((current) =>
            current === selectedRunId ? "" : current,
          );
        }
      });

    return () => controller.abort();
  }, [
    runFromList,
    selectedRunId,
    selectionRequestVersion,
  ]);
  const canCreateDraft = isRuntimeTeamProductCapabilitySupported(
    capabilities,
    "run_draft_create",
  );

  function handleStudioCreated(run: RuntimeTeamRun) {
    onRunUpdated(run);
    selectRun(run.id);
    setStudioOpen(false);
  }

  return (
    <>
      <div className="mb-3 flex flex-col gap-3 rounded-panel-lg border border-border bg-surface-softer px-3.5 py-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <div className="app-text-10 uppercase tracking-[0.16em] text-muted-foreground">
            {t("panels.teamsPanels.studio.quickStartEyebrow")}
          </div>
          <p className="mt-1 text-sm leading-6 text-muted-foreground">
            {canCreateDraft
              ? t("panels.teamsPanels.studio.quickStart")
              : t("panels.teamsPanels.studio.quickStartUnsupported")}
          </p>
        </div>
        {canCreateDraft ? (
          <button
            type="button"
            onClick={() => setStudioOpen((open) => !open)}
            className="min-h-11 shrink-0 rounded-control border border-accent-teal/35 bg-accent-teal/10 px-3 py-2 text-sm font-medium text-foreground transition hover:bg-accent-teal/20"
          >
            {studioOpen
              ? t("panels.teamsPanels.studio.close")
              : t("panels.teamsPanels.studio.open")}
          </button>
        ) : null}
      </div>
      {studioOpen ? (
        <TeamStudioPanel
          capabilities={capabilities}
          onCancel={() => setStudioOpen(false)}
          onCreated={handleStudioCreated}
        />
      ) : null}
      <div className="grid gap-3 xl:grid-cols-[19rem_minmax(0,1fr)]">
      <aside className="rounded-panel-lg border border-border bg-surface-softer p-2.5">
        <div className="mb-2.5 flex items-center justify-between gap-3 px-0.5">
          <div>
            <div className="app-text-10 uppercase tracking-[0.16em] text-muted-foreground">
              {t("panels.teamsPanels.runs.title")}
            </div>
            <div className="mt-0.5 text-sm font-semibold text-foreground">
              {t("panels.teamsPanels.runs.selectRun")}
            </div>
          </div>
          <Badge>{runs.length}</Badge>
        </div>

        {error ? (
          <div className="mb-2 rounded-card border border-accent-gold/25 bg-accent-gold/8 px-3 py-2 text-xs leading-5 text-muted-foreground">
            {t("panels.teamsPanels.runs.unavailable", { error })}
            <button
              type="button"
              onClick={onRetry}
              className="mt-2 inline-flex min-h-11 items-center gap-1.5 rounded-control border border-border px-2.5 py-1.5 text-xs font-medium text-foreground transition hover:border-border-strong hover:bg-surface-soft-hover"
            >
              <RefreshCcwIcon size={13} aria-hidden="true" />
              {t("panels.teamsPanels.runs.retry")}
            </button>
          </div>
        ) : null}

        {isLoading && runs.length === 0 ? (
          <div className="rounded-card border border-dashed border-border px-3 py-3 text-sm leading-6 text-muted-foreground">
            {t("panels.teamsPanels.runs.loading")}
          </div>
        ) : runs.length > 0 ? (
          <div className="space-y-1.5 xl:max-h-[calc(100vh-18rem)] xl:overflow-y-auto xl:pr-1">
            {runs.map((run) => {
              const isActive = run.id === selectedRunId;
              return (
                <button
                  key={run.id}
                  type="button"
                  onClick={() => selectRun(run.id)}
                  aria-current={isActive ? "true" : undefined}
                  className={cn(
                    "w-full rounded-card border px-3 py-2.5 text-left transition",
                    isActive
                      ? "border-accent-teal/30 bg-accent-teal/10 shadow-[0_0_0_1px_rgba(143,208,198,0.12)]"
                      : "border-border bg-surface-soft hover:border-border-strong hover:bg-surface-soft-hover",
                  )}
                >
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <div className="truncate text-sm font-semibold text-foreground">
                        {run.goal || t("panels.teamsPanels.runs.untitled")}
                      </div>
                      <div className="mt-1 truncate font-mono text-[11px] text-muted-foreground">
                        {run.id}
                      </div>
                    </div>
                    <span className="shrink-0 app-text-11 uppercase tracking-[0.12em] text-muted-foreground">
                      {run.status || t("panels.teamsPanels.details.statusUnknown")}
                    </span>
                  </div>
                  <div className="mt-2 flex flex-wrap gap-1.5 app-text-11 uppercase tracking-[0.12em] text-muted-foreground">
                    <span>{run.phase}</span>
                    <span>{t("panels.teamsPanels.runs.summaryShort", { state: run.summary_state })}</span>
                  </div>
                </button>
              );
            })}
          </div>
        ) : !isLoading ? (
          <div className="rounded-card border border-dashed border-border px-3 py-3 text-sm leading-6 text-muted-foreground">
            {t("panels.teamsPanels.runs.empty")}
          </div>
        ) : null}
      </aside>

      <div className="min-w-0">
        {selectedRun ? (
          <RuntimeTeamRunMonitor
            capabilities={capabilities}
            onOpenLegacyTeam={onOpenLegacyTeam}
            onRunUpdated={onRunUpdated}
            run={selectedRun}
          />
        ) : selectedRunId &&
          loadingRunId === selectedRunId ? (
          <p
            role="status"
            aria-live="polite"
            className="rounded-panel-lg border border-dashed border-border bg-surface-softer px-5 py-8 text-center text-sm leading-6 text-muted-foreground"
          >
            {t("panels.teamsPanels.runs.loadingLinkedRun", {
              id: selectedRunId,
            })}
          </p>
        ) : selectedRunId &&
          linkedRunError?.runId === selectedRunId ? (
          <div
            role="alert"
            className="rounded-panel-lg border border-accent-gold/25 bg-accent-gold/8 px-5 py-5 text-sm leading-6 text-muted-foreground"
          >
            <p>
              {t("panels.teamsPanels.runs.linkedRunUnavailable", {
                id: selectedRunId,
                error: linkedRunError.message,
              })}
            </p>
            <button
              type="button"
              onClick={() => {
                setSelectionRequestVersion((version) => version + 1);
                onRetry();
              }}
              className="mt-3 min-h-11 rounded-control border border-border px-3 py-2 text-xs font-medium text-foreground transition hover:border-border-strong hover:bg-surface-soft-hover"
            >
              {t("panels.teamsPanels.runs.retry")}
            </button>
          </div>
        ) : (
          <div className="rounded-panel-lg border border-dashed border-border bg-surface-softer px-5 py-8 text-center text-sm leading-6 text-muted-foreground">
            {t("panels.teamsPanels.runs.noSelection")}
          </div>
        )}
      </div>
    </div>
    </>
  );
}

type RuntimeTeamRunMonitorProps = {
  capabilities: RuntimeTeamProductCapabilities;
  onOpenLegacyTeam: (teamId: string) => void;
  onRunUpdated: (run: RuntimeTeamRun) => void;
  run: RuntimeTeamRun;
};

function RuntimeTeamRunMonitor({
  capabilities,
  onOpenLegacyTeam,
  onRunUpdated,
  run,
}: RuntimeTeamRunMonitorProps) {
  const { t } = useTranslation("workspace");
  const [observedRun, setObservedRun] = useState(run);
  const [error, setError] = useState<string | null>(null);
  const [temporarilyUnavailable, setTemporarilyUnavailable] = useState(false);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [isStarting, setIsStarting] = useState(false);
  const [startError, setStartError] = useState<string | null>(null);
  const [preflight, setPreflight] =
    useState<RuntimeTeamRunPreflightReport | null>(null);
  const [preflightError, setPreflightError] = useState<string | null>(null);
  const [isPreflightLoading, setIsPreflightLoading] = useState(false);
  const requestControllerRef = useRef<AbortController | null>(null);
  const preflightControllerRef = useRef<AbortController | null>(null);
  const terminalSummaryObservationRef = useRef({
    runId: run.id,
    count: 0,
  });
  const pollDelayRef = useRef(minRunPollDelayMs);
  const pollProgressRef = useRef({
    runId: run.id,
    noProgressCount: 0,
    failureCount: 0,
  });
  const observedRunRef = useRef(run);
  const onRunUpdatedRef = useRef(onRunUpdated);
  onRunUpdatedRef.current = onRunUpdated;
  const selectedRunIdRef = useRef(run.id);
  selectedRunIdRef.current = run.id;
  const tRef = useRef(t);
  tRef.current = t;

  useEffect(() => {
    observedRunRef.current = run;
    setObservedRun(run);
    setError(null);
    setTemporarilyUnavailable(false);
    setStartError(null);
    setPreflight(null);
    setPreflightError(null);
  }, [run.id, run.version, run.status]);

  const loadPreflight = useCallback(async (targetRun: RuntimeTeamRun = run) => {
    const isDraft = targetRun.status.trim().toLowerCase() === "draft";
    const supported = isRuntimeTeamProductCapabilitySupported(
      capabilities,
      "run_preflight",
    );
    if (
      targetRun.id !== selectedRunIdRef.current ||
      !isDraft ||
      !supported
    ) {
      preflightControllerRef.current?.abort();
      setIsPreflightLoading(false);
      setPreflight(null);
      setPreflightError(null);
      return null;
    }

    preflightControllerRef.current?.abort();
    const controller = new AbortController();
    preflightControllerRef.current = controller;
    setIsPreflightLoading(true);
    setPreflightError(null);
    try {
      const response = await getRuntimeTeamRunPreflight(targetRun.id, {
        signal: controller.signal,
      });
      if (
        controller.signal.aborted ||
        preflightControllerRef.current !== controller ||
        targetRun.id !== selectedRunIdRef.current
      ) {
        return response.preflight;
      }
      if (
        response.preflight.run_id !== targetRun.id ||
        response.preflight.version !== targetRun.version
      ) {
        setPreflight(null);
        return null;
      }
      setPreflight(response.preflight);
      setPreflightError(null);
      return response.preflight;
    } catch (loadError) {
      if (
        controller.signal.aborted ||
        preflightControllerRef.current !== controller
      ) {
        return null;
      }
      setPreflight(null);
      setPreflightError(
        loadError instanceof Error
          ? loadError.message
          : tRef.current("panels.teamsPanels.runs.preflightUnavailable"),
      );
      return null;
    } finally {
      if (
        !controller.signal.aborted &&
        preflightControllerRef.current === controller &&
        targetRun.id === selectedRunIdRef.current
      ) {
        setIsPreflightLoading(false);
      }
    }
  }, [capabilities, run.id, run.status, run.version]);

  useEffect(() => {
    void loadPreflight();
    return () => {
      preflightControllerRef.current?.abort();
      preflightControllerRef.current = null;
    };
  }, [loadPreflight]);

  const observeRun = useCallback(async () => {
    requestControllerRef.current?.abort();
    const controller = new AbortController();
    requestControllerRef.current = controller;
    setIsRefreshing(true);
    try {
      const response = await getRuntimeTeamRun(run.id, {
        signal: controller.signal,
      });
      if (
        controller.signal.aborted ||
        requestControllerRef.current !== controller ||
        run.id !== selectedRunIdRef.current
      ) {
        return null;
      }
      const previousRun = observedRunRef.current;
      if (
        response.run.version < previousRun.version ||
        response.run.event_seq < previousRun.event_seq
      ) {
        // A refresh can finish after another surface has already delivered a
        // newer snapshot. Never move the monitor's authoritative view or
        // cursor backwards when that happens.
        return previousRun;
      }
      const madeProgress =
        response.run.event_seq > previousRun.event_seq ||
        response.run.version > previousRun.version;
      observedRunRef.current = response.run;
      setObservedRun(response.run);
      onRunUpdatedRef.current(response.run);
      setError(null);
      setTemporarilyUnavailable(false);
      if (
        response.run.summary_state === "pending" &&
        isTerminal(response.run)
      ) {
        pollProgressRef.current.noProgressCount = 0;
        pollProgressRef.current.failureCount = 0;
        pollDelayRef.current = terminalSummaryPollDelayMs;
      } else if (!isTerminal(response.run)) {
        pollProgressRef.current.failureCount = 0;
        if (madeProgress) {
          pollProgressRef.current.noProgressCount = 0;
          pollDelayRef.current = minRunPollDelayMs;
        } else {
          pollProgressRef.current.noProgressCount += 1;
          pollDelayRef.current = Math.min(
            minRunPollDelayMs *
              2 ** (pollProgressRef.current.noProgressCount - 1),
            maxRunPollDelayMs,
          );
        }
      }
      return response.run;
    } catch (loadError) {
      if (
        controller.signal.aborted ||
        requestControllerRef.current !== controller
      ) {
        return null;
      }
      setError(
        loadError instanceof Error
          ? loadError.message
          : tRef.current("panels.teamsPanels.runs.refreshFailed"),
      );
      const isRetryableResponse =
        loadError instanceof RuntimeApiError &&
        (loadError.status === 429 || loadError.status === 503);
      setTemporarilyUnavailable(isTemporaryRunReadFailure(loadError));
      pollProgressRef.current.failureCount += 1;
      const backoffDelay = Math.min(
        minRunPollDelayMs *
          2 ** (pollProgressRef.current.failureCount - 1),
        maxTransientErrorPollDelayMs,
      );
      pollDelayRef.current =
        isRetryableResponse && loadError.retryAfterMs !== null
          ? Math.max(minRunPollDelayMs, loadError.retryAfterMs)
          : backoffDelay;
      return null;
    } finally {
      if (
        !controller.signal.aborted &&
        requestControllerRef.current === controller
      ) {
        setIsRefreshing(false);
      }
    }
  }, [run.id]);

  useEffect(() => {
    let cancelled = false;
    let timer: number | undefined;
    if (pollProgressRef.current.runId !== run.id) {
      pollProgressRef.current = {
        runId: run.id,
        noProgressCount: 0,
        failureCount: 0,
      };
      pollDelayRef.current = minRunPollDelayMs;
    }
    if (terminalSummaryObservationRef.current.runId !== run.id) {
      terminalSummaryObservationRef.current = { runId: run.id, count: 0 };
    }

    const observe = async () => {
      if (isDocumentHidden()) {
        return;
      }
      const nextRun = await observeRun();
      if (cancelled || isDocumentHidden()) {
        return;
      }
      // The callback outlives the render that created it. Use the ref on
      // failed reads so a terminal snapshot followed by a transient error
      // cannot fall back to the initial active snapshot and reset the bounded
      // summary observation window.
      const current = nextRun ?? observedRunRef.current;
      if (isTerminal(current) && current.summary_state === "pending") {
        terminalSummaryObservationRef.current.count += 1;
      } else if (!isTerminal(current)) {
        terminalSummaryObservationRef.current.count = 0;
      }
      if (
        shouldObserve(
          current,
          terminalSummaryObservationRef.current.count,
        )
      ) {
        timer = window.setTimeout(observe, pollDelayRef.current);
      }
    };

    const handleVisibilityChange = () => {
      if (cancelled) {
        return;
      }
      if (isDocumentHidden()) {
        if (timer !== undefined) {
          window.clearTimeout(timer);
          timer = undefined;
        }
        requestControllerRef.current?.abort();
        requestControllerRef.current = null;
        setIsRefreshing(false);
        return;
      }
      if (timer !== undefined) {
        window.clearTimeout(timer);
        timer = undefined;
      }
      void observe();
    };
    document.addEventListener("visibilitychange", handleVisibilityChange);

    if (
      shouldObserve(
        run,
        terminalSummaryObservationRef.current.count,
      )
    ) {
      void observe();
    }

    return () => {
      cancelled = true;
      if (timer !== undefined) {
        window.clearTimeout(timer);
      }
      document.removeEventListener("visibilitychange", handleVisibilityChange);
      requestControllerRef.current?.abort();
      requestControllerRef.current = null;
      preflightControllerRef.current?.abort();
      preflightControllerRef.current = null;
    };
    // The selected run ID and Runtime observation callback define this polling
    // lifecycle. `observedRun` is intentionally last-known-good local state.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [observeRun, run.id, run.status, run.summary_state]);

  const canStart =
    observedRun.status.trim().toLowerCase() === "draft" &&
    isRuntimeTeamProductCapabilitySupported(capabilities, "run_start") &&
    isRuntimeTeamProductCapabilitySupported(capabilities, "run_preflight") &&
    !isPreflightLoading &&
    preflight?.run_id === observedRun.id &&
    preflight.version === observedRun.version &&
    preflight.ready;
  const isDraftRun = observedRun.status.trim().toLowerCase() === "draft";
  const preflightIsSupported = isRuntimeTeamProductCapabilitySupported(
    capabilities,
    "run_preflight",
  );
  const preflightFindings = preflight?.findings ?? [];
  const preflightBlockers =
    preflight?.blockers ??
    preflightFindings.filter((finding) => finding.severity === "blocker");
  const preflightWarnings =
    preflight?.warnings ??
    preflightFindings.filter((finding) => finding.severity === "warning");
  const preflightInfos =
    preflight?.infos ??
    preflightFindings.filter((finding) => finding.severity === "info");

  async function handleStart() {
    if (!canStart || isStarting) {
      return;
    }
    const attemptedRun = observedRun;
    setIsStarting(true);
    setStartError(null);
    const idempotencyKey = getOrCreateStartKey(
      attemptedRun.id,
      attemptedRun.version,
    );
    try {
      const response = await startRuntimeTeamRun(
        attemptedRun.id,
        { expected_version: attemptedRun.version },
        { idempotencyKey },
      );
      clearStartKey(attemptedRun.id, attemptedRun.version);
      if (selectedRunIdRef.current !== attemptedRun.id) {
        return;
      }
      setObservedRun(response.run);
      observedRunRef.current = response.run;
      onRunUpdatedRef.current(response.run);
    } catch (startFailure) {
      if (selectedRunIdRef.current !== attemptedRun.id) {
        return;
      }
      setStartError(
        startFailure instanceof Error
          ? startFailure.message
          : t("panels.teamsPanels.runs.startFailed"),
      );
      // Start may have lost a version race, or the server may have committed
      // while its response was lost. Refresh Runtime state before allowing a
      // retry; only a matching fresh draft preflight can enable Start again.
      setPreflight(null);
      setPreflightError(null);
      const latestRun = await observeRun();
      if (!latestRun || selectedRunIdRef.current !== attemptedRun.id) {
        return;
      }
      if (
        latestRun.status.trim().toLowerCase() !== "draft" ||
        latestRun.version !== attemptedRun.version
      ) {
        clearStartKey(attemptedRun.id, attemptedRun.version);
      }
      if (latestRun.status.trim().toLowerCase() === "draft") {
        await loadPreflight(latestRun);
      } else {
        setStartError(null);
      }
    } finally {
      if (selectedRunIdRef.current === attemptedRun.id) {
        setIsStarting(false);
      }
    }
  }

  const summaryLabel =
    observedRun.summary_state === "pending"
      ? t("panels.teamsPanels.runs.summaryStates.pending")
      : observedRun.summary_state === "ready"
        ? t("panels.teamsPanels.runs.summaryStates.ready")
        : observedRun.summary_state === "failed"
          ? t("panels.teamsPanels.runs.summaryStates.failed")
          : observedRun.summary_state === "not_applicable"
            ? t("panels.teamsPanels.runs.summaryStates.not_applicable")
            : t("panels.teamsPanels.runs.summaryStates.unknown");
  const statusLabel = observedRun.status || t("panels.teamsPanels.details.statusUnknown");

  return (
    <section className="rounded-panel border border-border bg-surface-solid p-3">
      <div className="flex flex-col gap-3 border-b border-border pb-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <div className="app-text-10 uppercase tracking-[0.16em] text-muted-foreground">
            {t("panels.teamsPanels.runs.monitorEyebrow")}
          </div>
          <h3 className="mt-1 break-words text-lg font-semibold text-foreground">
            {observedRun.goal || t("panels.teamsPanels.runs.untitled")}
          </h3>
          <div className="mt-1 break-all font-mono text-xs text-muted-foreground">
            {observedRun.id}
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <Badge>{statusLabel}</Badge>
          <Badge>{observedRun.phase}</Badge>
          <Badge>{summaryLabel}</Badge>
        </div>
      </div>

      {error ? (
        <div
          role="status"
          aria-live="polite"
          className="mt-3 rounded-card border border-accent-gold/25 bg-accent-gold/8 px-3 py-2 text-sm leading-6 text-muted-foreground"
        >
          {temporarilyUnavailable
            ? t("panels.teamsPanels.runs.temporarilyUnavailable", { error })
            : t("panels.teamsPanels.runs.stale", { error })}
        </div>
      ) : null}
      {startError ? (
        <div className="mt-3 rounded-card border border-accent-red/25 bg-accent-red/8 px-3 py-2 text-sm leading-6 text-muted-foreground">
          {t("panels.teamsPanels.runs.startFailedWithReason", { error: startError })}
        </div>
      ) : null}

      <dl className="mt-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <RunFact label={t("panels.teamsPanels.runs.phase")} value={observedRun.phase} />
        <RunFact label={t("panels.teamsPanels.runs.version")} value={String(observedRun.version)} />
        <RunFact
          label={t("panels.teamsPanels.runs.compatibilityTeamEventSequence")}
          value={String(observedRun.event_seq)}
        />
        <RunFact label={t("panels.teamsPanels.runs.summary")} value={summaryLabel} />
        <RunFact label={t("panels.teamsPanels.runs.createdAt")} value={formatTimestamp(observedRun.created_at)} />
        <RunFact label={t("panels.teamsPanels.runs.startedAt")} value={formatTimestamp(observedRun.started_at)} />
        <RunFact label={t("panels.teamsPanels.runs.terminalAt")} value={formatTimestamp(observedRun.terminal_at)} />
        <RunFact
          label={t("panels.teamsPanels.runs.compatibility")}
          value={observedRun.compatibility_mode || "—"}
        />
      </dl>

      <div className="mt-4 rounded-card border border-border bg-surface-softer px-3 py-3 text-sm leading-6 text-muted-foreground">
        {observedRun.summary_state === "pending"
          ? t("panels.teamsPanels.runs.summaryPending")
          : observedRun.summary_state === "ready"
            ? t("panels.teamsPanels.runs.summaryReady")
            : observedRun.summary_state === "failed"
              ? t("panels.teamsPanels.runs.summaryFailed")
              : t("panels.teamsPanels.runs.summaryNotApplicable")}
      </div>

      {isDraftRun ? (
        <section
          aria-labelledby="team-run-preflight-title"
          className="mt-4 rounded-card border border-border bg-surface-softer p-3"
        >
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h4
                id="team-run-preflight-title"
                className="text-sm font-semibold text-foreground"
              >
                {t("panels.teamsPanels.runs.preflightTitle")}
              </h4>
              <p className="mt-1 text-xs leading-5 text-muted-foreground">
                {t("panels.teamsPanels.runs.preflightBoundary")}
              </p>
            </div>
            {preflightIsSupported ? (
              <button
                type="button"
                onClick={() => void loadPreflight()}
                disabled={isPreflightLoading}
                className="min-h-11 rounded-control border border-border px-3 py-2 text-xs font-medium text-muted-foreground transition hover:border-border-strong hover:bg-surface-soft-hover hover:text-foreground disabled:cursor-wait disabled:opacity-60"
              >
                {isPreflightLoading
                  ? t("panels.teamsPanels.runs.preflightChecking")
                  : t("panels.teamsPanels.runs.retryPreflight")}
              </button>
            ) : null}
          </div>
          {capabilities.worker_session_liveness === "unsupported" ? (
            <p className="mt-3 rounded-control border border-accent-gold/25 bg-accent-gold/8 px-3 py-2 text-sm leading-6 text-muted-foreground">
              {t(
                "panels.teamsPanels.runs.workerSessionLivenessUnsupported",
              )}
            </p>
          ) : null}

          {!preflightIsSupported ? (
            <p className="mt-3 rounded-control border border-accent-gold/25 bg-accent-gold/8 px-3 py-2 text-sm leading-6 text-muted-foreground">
              {t("panels.teamsPanels.runs.preflightUnsupported")}
            </p>
          ) : isPreflightLoading && !preflight ? (
            <p role="status" aria-live="polite" className="mt-3 text-sm leading-6 text-muted-foreground">
              {t("panels.teamsPanels.runs.preflightLoading")}
            </p>
          ) : preflightError ? (
            <div
              role="alert"
              className="mt-3 rounded-control border border-accent-red/25 bg-accent-red/8 px-3 py-2 text-sm leading-6 text-muted-foreground"
            >
              {t("panels.teamsPanels.runs.preflightError", {
                error: preflightError,
              })}
            </div>
          ) : preflight && preflight.version === observedRun.version ? (
            <div className="mt-3 space-y-3">
              <div
                role="status"
                aria-live="polite"
                aria-atomic="true"
                className="flex flex-wrap items-center gap-2 text-sm"
              >
                <Badge>
                  {preflight.ready
                    ? t("panels.teamsPanels.runs.preflightReady")
                    : t("panels.teamsPanels.runs.preflightBlocked")}
                </Badge>
                <span className="text-muted-foreground">
                  {t("panels.teamsPanels.runs.preflightCounts", {
                    blockers: preflight.blocker_count,
                    warnings: preflight.warning_count,
                    infos: preflight.info_count,
                  })}
                </span>
              </div>
              {preflightBlockers.length > 0 ? (
                <RunPreflightFindingGroup
                  findings={preflightBlockers}
                  title={t("panels.teamsPanels.runs.preflightBlockers")}
                />
              ) : null}
              {preflightWarnings.length > 0 ? (
                <RunPreflightFindingGroup
                  findings={preflightWarnings}
                  title={t("panels.teamsPanels.runs.preflightWarnings")}
                />
              ) : null}
              {preflightInfos.length > 0 ? (
                <RunPreflightFindingGroup
                  findings={preflightInfos}
                  title={t("panels.teamsPanels.runs.preflightInfos")}
                />
              ) : null}
              {preflightFindings.length === 0 ? (
                <p className="text-sm leading-6 text-muted-foreground">
                  {t("panels.teamsPanels.runs.preflightNoFindings")}
                </p>
              ) : null}
              <p className="text-xs leading-5 text-muted-foreground">
                {t("panels.teamsPanels.runs.routePreviewDisabled", {
                  capability: preflight.route_preview_capability,
                })}
              </p>
            </div>
          ) : isPreflightLoading ? (
            <p role="status" aria-live="polite" className="mt-3 text-sm leading-6 text-muted-foreground">
              {t("panels.teamsPanels.runs.preflightLoading")}
            </p>
          ) : (
            <p role="status" className="mt-3 text-sm leading-6 text-muted-foreground">
              {t("panels.teamsPanels.runs.preflightStale")}
            </p>
          )}
        </section>
      ) : null}

      <div className="mt-4 flex flex-wrap items-center gap-2">
        {isDraftRun &&
        isRuntimeTeamProductCapabilitySupported(capabilities, "run_start") ? (
          <button
            type="button"
            onClick={() => void handleStart()}
            disabled={isStarting || !canStart}
            className="min-h-11 rounded-control border border-accent-teal/35 bg-accent-teal/10 px-3 py-2 text-sm font-medium text-foreground transition hover:bg-accent-teal/20 disabled:cursor-not-allowed disabled:opacity-60"
          >
            {isStarting
              ? t("panels.teamsPanels.runs.starting")
              : t("panels.teamsPanels.runs.start")}
          </button>
        ) : isDraftRun ? (
          <span className="text-xs leading-5 text-muted-foreground">
            {t("panels.teamsPanels.runs.startUnsupported")}
          </span>
        ) : null}
        <button
          type="button"
          onClick={() => void observeRun()}
          disabled={isRefreshing}
          className="inline-flex min-h-11 items-center gap-1.5 rounded-control border border-border px-3 py-2 text-sm font-medium text-muted-foreground transition hover:border-border-strong hover:bg-surface-soft-hover hover:text-foreground disabled:cursor-wait disabled:opacity-60"
        >
          <RefreshCcwIcon
            size={14}
            aria-hidden="true"
            className={cn(isRefreshing ? "animate-spin" : "")}
          />
          {t("panels.teamsPanels.runs.refresh")}
        </button>
        {observedRun.legacy_team_id ? (
          <button
            type="button"
            onClick={() => onOpenLegacyTeam(observedRun.legacy_team_id ?? "")}
            className="min-h-11 rounded-control border border-border px-3 py-2 text-sm font-medium text-muted-foreground transition hover:border-border-strong hover:bg-surface-soft-hover hover:text-foreground"
          >
            {t("panels.teamsPanels.runs.openLegacyTeam")}
          </button>
        ) : null}
      </div>

      <p className="mt-3 text-xs leading-5 text-muted-foreground/80">
        {t("panels.teamsPanels.runs.readOnlyBoundary")}
      </p>
    </section>
  );
}

function RunPreflightFindingGroup({
  findings,
  title,
}: {
  findings: RuntimeTeamRunPreflightFinding[];
  title: string;
}) {
  const { t } = useTranslation("workspace");

  return (
    <div>
      <h5 className="text-xs font-semibold uppercase tracking-[0.12em] text-muted-foreground">
        {title}
      </h5>
      <ul className="mt-2 space-y-2">
        {findings.map((finding, index) => (
          <li
            key={`${finding.code}-${finding.task_id ?? finding.teammate_id ?? finding.path ?? index}`}
            className={cn(
              "rounded-control border px-3 py-2 text-sm leading-6",
              finding.severity === "blocker"
                ? "border-accent-red/25 bg-accent-red/8 text-foreground"
                : finding.severity === "warning"
                  ? "border-accent-gold/25 bg-accent-gold/8 text-foreground"
                  : "border-border bg-surface-solid text-muted-foreground",
            )}
          >
            <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
              <span className="font-mono text-[11px] uppercase tracking-[0.08em] text-muted-foreground">
                {finding.code}
              </span>
              {finding.task_id ? (
                <span className="font-mono text-[11px] text-muted-foreground">
                  {t("panels.teamsPanels.runs.findingTask", {
                    id: finding.task_id,
                  })}
                </span>
              ) : null}
              {finding.teammate_id ? (
                <span className="font-mono text-[11px] text-muted-foreground">
                  {t("panels.teamsPanels.runs.findingTeammate", {
                    id: finding.teammate_id,
                  })}
                </span>
              ) : null}
            </div>
            <p className="mt-1 text-foreground">{finding.message}</p>
            {finding.remediation ? (
              <p className="mt-1 text-xs text-muted-foreground">
                {finding.remediation}
              </p>
            ) : null}
          </li>
        ))}
      </ul>
    </div>
  );
}

function RunFact({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0 rounded-card border border-border bg-surface-softer px-3 py-2">
      <dt className="app-text-11 uppercase tracking-[0.12em] text-muted-foreground">
        {label}
      </dt>
      <dd className="mt-1 break-words text-sm text-foreground">{value}</dd>
    </div>
  );
}
