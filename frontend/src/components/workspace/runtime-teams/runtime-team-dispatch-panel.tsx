import { LoaderCircleIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { MessageMarkdown } from "@/components/workspace/message-markdown";
import {
  buildDispatchComparisonRows,
  getDispatchOutcomeNarrative,
  shouldExpectDispatchOutcomeSummary,
  sortDispatchComparisonRows,
  summarizeDispatchBatch,
  type DispatchMonitorEntry,
  type DispatchTeamReadiness,
  type DispatchTemplateMode,
  isTerminalDispatchStatus,
  prettyEventType,
  statusTone,
  truncateIdentifier,
  type MultiTeamDispatchResult,
} from "@/components/workspace/runtime-teams/shared";
import { cn, formatRelativeTimestamp } from "@/lib/utils";
import {
  type RuntimeTeamRecord,
  type RuntimeTeamSummaryEntry,
} from "@/types/runtime";

type RuntimeTeamDispatchPanelProps = {
  dispatchMonitor: DispatchMonitorEntry[];
  dispatchMonitorCounts: Record<string, number>;
  dispatchMonitorError: string | null;
  dispatchTaskDeliverablesDraft: string;
  dispatchTaskError: string | null;
  dispatchTaskGoalDraft: string;
  dispatchTaskInputsDraft: string;
  dispatchTaskPriorityDraft: string;
  dispatchTaskResults: MultiTeamDispatchResult[];
  dispatchTaskTitleDraft: string;
  dispatchTeamReadiness: Record<string, DispatchTeamReadiness>;
  dispatchTemplateMode: DispatchTemplateMode;
  isDispatchMonitorLoading: boolean;
  isDispatchReadinessLoading: boolean;
  isDispatchingTask: boolean;
  isProvisioningDispatch: boolean;
  onDispatchTaskDeliverablesDraftChange: (value: string) => void;
  onDispatchTaskGoalDraftChange: (value: string) => void;
  onDispatchTaskInputsDraftChange: (value: string) => void;
  onDispatchTaskPriorityDraftChange: (value: string) => void;
  onDispatchTaskTitleDraftChange: (value: string) => void;
  onDispatchTaskToTeams: () => void | Promise<void>;
  onDispatchTemplateModeChange: (mode: DispatchTemplateMode) => void;
  onProvisionStrategyDraftChange: (value: string) => void;
  onProvisionTeamCountDraftChange: (value: string) => void;
  onProvisionTeammateNamePrefixDraftChange: (value: string) => void;
  onProvisionTeammateProfileDraftChange: (value: string) => void;
  onProvisionTeamsAndDispatch: () => void | Promise<void>;
  onProvisionUserPrefixDraftChange: (value: string) => void;
  onProvisionWorkspaceDraftChange: (value: string) => void;
  onRefreshDispatchMonitor: () => void | Promise<void>;
  onToggleDispatchTeam: (teamId: string) => void;
  provisionStrategyDraft: string;
  provisionTeamCountDraft: string;
  provisionTeammateNamePrefixDraft: string;
  provisionTeammateProfileDraft: string;
  provisionUserPrefixDraft: string;
  provisionWorkspaceDraft: string;
  selectedDispatchTeamIds: string[];
  summaryMap: Map<string, RuntimeTeamSummaryEntry>;
  teams: RuntimeTeamRecord[];
};

const dispatchControlClass =
  "w-full rounded-[0.75rem] border border-white/8 bg-black/15 px-3 py-2 text-sm text-[var(--foreground)] outline-none placeholder:text-white/28";

const dispatchStatusPillClass =
  "rounded-[0.65rem] border px-2 py-0.5 app-text-10 uppercase tracking-[0.12em]";

const dispatchSummaryPillClass =
  "rounded-[0.65rem] border px-2.5 py-1 text-[11px] uppercase tracking-[0.12em]";

const compactMarkdownClass =
  "app-text-12 [&>p]:my-0 [&>ul]:my-2 [&>ol]:my-2 [&>blockquote]:my-2 [&>div]:my-2";

export function RuntimeTeamDispatchPanel({
  dispatchMonitor,
  dispatchMonitorCounts,
  dispatchMonitorError,
  dispatchTaskDeliverablesDraft,
  dispatchTaskError,
  dispatchTaskGoalDraft,
  dispatchTaskInputsDraft,
  dispatchTaskPriorityDraft,
  dispatchTaskResults,
  dispatchTaskTitleDraft,
  dispatchTeamReadiness,
  dispatchTemplateMode,
  isDispatchMonitorLoading,
  isDispatchReadinessLoading,
  isDispatchingTask,
  isProvisioningDispatch,
  onDispatchTaskDeliverablesDraftChange,
  onDispatchTaskGoalDraftChange,
  onDispatchTaskInputsDraftChange,
  onDispatchTaskPriorityDraftChange,
  onDispatchTaskTitleDraftChange,
  onDispatchTaskToTeams,
  onDispatchTemplateModeChange,
  onProvisionStrategyDraftChange,
  onProvisionTeamCountDraftChange,
  onProvisionTeammateNamePrefixDraftChange,
  onProvisionTeammateProfileDraftChange,
  onProvisionTeamsAndDispatch,
  onProvisionUserPrefixDraftChange,
  onProvisionWorkspaceDraftChange,
  onRefreshDispatchMonitor,
  onToggleDispatchTeam,
  provisionStrategyDraft,
  provisionTeamCountDraft,
  provisionTeammateNamePrefixDraft,
  provisionTeammateProfileDraft,
  provisionUserPrefixDraft,
  provisionWorkspaceDraft,
  selectedDispatchTeamIds,
  summaryMap,
  teams,
}: RuntimeTeamDispatchPanelProps) {
  const comparisonRows = sortDispatchComparisonRows(
    buildDispatchComparisonRows(dispatchTaskResults, dispatchMonitor),
  );
  const batchSummary = summarizeDispatchBatch(comparisonRows);
  const batchStatusEntries = Object.entries(batchSummary.statusCounts);
  const monitorCountEntries = Object.entries(dispatchMonitorCounts);
  const terminalRowsWithSummary = batchSummary.terminalRows.filter(
    (row) => shouldExpectDispatchOutcomeSummary(row) && Boolean(row.summary?.trim()),
  );
  const terminalRowsMissingSummary = batchSummary.terminalRows.filter(
    (row) => shouldExpectDispatchOutcomeSummary(row) && !row.summary?.trim(),
  );

  return (
    <div className="rounded-[0.95rem] border border-white/8 bg-white/[0.035] p-3.5">
      <div className="flex items-start justify-between gap-3">
        <div>
          <div className="text-sm font-semibold text-[var(--foreground)]">
            Multi-team next task
          </div>
          <div className="mt-1 text-xs text-[var(--muted-foreground)]">
            Create one ready task across multiple active teams so their orchestrators can
            run in parallel.
          </div>
        </div>
        <Badge>{selectedDispatchTeamIds.length} selected</Badge>
      </div>

      <div className="mt-3 rounded-[0.8rem] border border-white/8 bg-white/[0.03] px-3 py-2.5">
        <div className="app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
          Provision runnable teams and dispatch
        </div>
        <div className="mt-2.5 grid gap-2.5 sm:grid-cols-2 xl:grid-cols-3">
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              Team count
            </div>
            <input
              value={provisionTeamCountDraft}
              onChange={(event) => onProvisionTeamCountDraftChange(event.target.value)}
              placeholder="2"
              className={dispatchControlClass}
            />
          </div>
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              Workspace id
            </div>
            <input
              value={provisionWorkspaceDraft}
              onChange={(event) => onProvisionWorkspaceDraftChange(event.target.value)}
              placeholder="fanout-workspace"
              className={dispatchControlClass}
            />
          </div>
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              Strategy
            </div>
            <input
              value={provisionStrategyDraft}
              onChange={(event) => onProvisionStrategyDraftChange(event.target.value)}
              placeholder="parallel-fanout"
              className={dispatchControlClass}
            />
          </div>
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              User prefix
            </div>
            <input
              value={provisionUserPrefixDraft}
              onChange={(event) => onProvisionUserPrefixDraftChange(event.target.value)}
              placeholder="fanout-user"
              className={dispatchControlClass}
            />
          </div>
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              Teammate name prefix
            </div>
            <input
              value={provisionTeammateNamePrefixDraft}
              onChange={(event) =>
                onProvisionTeammateNamePrefixDraftChange(event.target.value)
              }
              placeholder="Fanout Worker"
              className={dispatchControlClass}
            />
          </div>
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              Teammate profile
            </div>
            <input
              value={provisionTeammateProfileDraft}
              onChange={(event) =>
                onProvisionTeammateProfileDraftChange(event.target.value)
              }
              placeholder="parallel execution worker"
              className={dispatchControlClass}
            />
          </div>
        </div>
        <div className="mt-2.5 flex flex-col gap-2.5 sm:flex-row sm:items-center sm:justify-between">
          <div className="text-xs text-[var(--muted-foreground)]">
            Each provisioned team gets a lead session, a worker session, one idle
            teammate, and the current next task.
          </div>
          <Button
            variant="secondary"
            size="sm"
            onClick={onProvisionTeamsAndDispatch}
            disabled={isProvisioningDispatch}
          >
            {isProvisioningDispatch ? (
              <LoaderCircleIcon size={14} className="animate-spin" />
            ) : null}
            Provision runnable teams and dispatch
          </Button>
        </div>
      </div>

      <div className="mt-3 rounded-[0.8rem] border border-white/8 bg-white/[0.03] px-3 py-2.5">
        <div className="app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
          Fan-out template
        </div>
        <div className="mt-2.5 flex flex-wrap gap-1.5">
          <button
            type="button"
            onClick={() => onDispatchTemplateModeChange("review_implement_verify")}
            className={cn(
              "rounded-[0.65rem] border px-2.5 py-1.5 text-base uppercase tracking-[0.12em] transition",
              dispatchTemplateMode === "review_implement_verify"
                ? "border-[#f0c77b]/24 bg-[#f0c77b]/10 text-[#f0c77b]"
                : "border-white/10 bg-white/4 text-[var(--muted-foreground)] hover:border-white/14 hover:bg-white/7 hover:text-[var(--foreground)]",
            )}
          >
            Review / Implement / Verify
          </button>
          <button
            type="button"
            onClick={() => onDispatchTemplateModeChange("mirror")}
            className={cn(
              "rounded-[0.65rem] border px-2.5 py-1.5 text-base uppercase tracking-[0.12em] transition",
              dispatchTemplateMode === "mirror"
                ? "border-[#f0c77b]/24 bg-[#f0c77b]/10 text-[#f0c77b]"
                : "border-white/10 bg-white/4 text-[var(--muted-foreground)] hover:border-white/14 hover:bg-white/7 hover:text-[var(--foreground)]",
            )}
          >
            Mirror Same Task
          </button>
        </div>
        <div className="mt-2.5 text-sm leading-6 text-[var(--muted-foreground)]">
          {dispatchTemplateMode === "mirror"
            ? "Every selected team receives the same task payload."
            : "Teams receive role-specific variants of the same next task so they execute from different angles."}
        </div>
      </div>

      <div className="mt-3 space-y-1.5">
        {teams.length === 0 ? (
          <div className="rounded-[0.75rem] border border-dashed border-white/10 px-3 py-2.5 text-sm leading-6 text-[var(--muted-foreground)]">
            No existing teams yet. Use the provision action above to create runnable
            teams and fan out the next task.
          </div>
        ) : null}
        {teams.map((team) => {
          const checked = selectedDispatchTeamIds.includes(team.id);
          const summary = summaryMap.get(team.id);
          const readiness = dispatchTeamReadiness[team.id];

          return (
            <label
              key={`dispatch-${team.id}`}
              className={cn(
                "flex cursor-pointer items-center justify-between gap-3 rounded-[0.8rem] border px-3 py-2.5 transition",
                checked
                  ? "border-[#f0c77b]/24 bg-[#f0c77b]/8"
                  : "border-white/8 bg-white/4 hover:border-white/14 hover:bg-white/7",
              )}
            >
              <span className="flex min-w-0 items-center gap-3">
                <input
                  type="checkbox"
                  checked={checked}
                  onChange={() => onToggleDispatchTeam(team.id)}
                  className="size-4 rounded border-white/14 bg-transparent"
                />
                <span className="min-w-0">
                  <span className="block truncate text-[13px] font-semibold text-[var(--foreground)]">
                    {truncateIdentifier(team.id, 18)}
                  </span>
                  <span className="block truncate text-xs text-[var(--muted-foreground)]">
                    {summary?.tasks.total ?? 0} tasks · {summary?.teammates.total ?? 0} teammates
                  </span>
                  <span className="mt-0.5 block truncate app-text-11 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
                    {isDispatchReadinessLoading && !readiness
                      ? "checking executability..."
                      : readiness
                        ? readiness.reason
                        : "readiness unavailable"}
                  </span>
                </span>
              </span>
              <span className="flex shrink-0 flex-col items-end gap-2">
                <span className="app-text-11 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
                  {team.status || "unknown"}
                </span>
                <span
                  className={cn(
                    dispatchStatusPillClass,
                    readiness?.executable
                      ? "border-[#8fd0c6]/24 bg-[#8fd0c6]/10 text-[#8fd0c6]"
                      : "border-white/10 bg-white/6 text-[var(--muted-foreground)]",
                  )}
                >
                  {readiness?.executable ? "executable" : "not ready"}
                </span>
              </span>
            </label>
          );
        })}
      </div>

      <div className="mt-3 grid gap-2.5">
        <div>
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            Task title
          </div>
          <input
            value={dispatchTaskTitleDraft}
            onChange={(event) => onDispatchTaskTitleDraftChange(event.target.value)}
            placeholder="Parallel review of runtime stream stability"
            className={dispatchControlClass}
          />
        </div>

        <div>
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            Goal
          </div>
          <textarea
            value={dispatchTaskGoalDraft}
            onChange={(event) => onDispatchTaskGoalDraftChange(event.target.value)}
            placeholder="Have each selected team tackle the same next task from a different angle and report outcomes independently."
            className={`min-h-24 ${dispatchControlClass} resize-y leading-6`}
          />
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              Inputs
            </div>
            <textarea
              value={dispatchTaskInputsDraft}
              onChange={(event) => onDispatchTaskInputsDraftChange(event.target.value)}
              placeholder={"spec.md\nopen questions\nexpected risks"}
              className={`min-h-20 ${dispatchControlClass} resize-y leading-6`}
            />
          </div>
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              Deliverables
            </div>
            <textarea
              value={dispatchTaskDeliverablesDraft}
              onChange={(event) =>
                onDispatchTaskDeliverablesDraftChange(event.target.value)
              }
              placeholder={"summary.md\npatch.diff\nvalidation notes"}
              className={`min-h-20 ${dispatchControlClass} resize-y leading-6`}
            />
          </div>
        </div>

        <div className="grid gap-3 sm:grid-cols-[160px_1fr]">
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              Priority
            </div>
            <input
              value={dispatchTaskPriorityDraft}
              onChange={(event) => onDispatchTaskPriorityDraftChange(event.target.value)}
              placeholder="50"
              className={dispatchControlClass}
            />
          </div>
          <div className="flex items-end">
            <div className="text-xs text-[var(--muted-foreground)]">
              Tasks are created with `status=ready`, so active team orchestrators can
              claim and execute them.
            </div>
          </div>
        </div>

        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div className="text-xs text-[var(--muted-foreground)]">
            Use this to fan out the same next task across multiple executable teams
            for parallel execution.
          </div>
          <Button
            variant="primary"
            size="sm"
            onClick={onDispatchTaskToTeams}
            disabled={isDispatchingTask || selectedDispatchTeamIds.length === 0}
          >
            {isDispatchingTask ? (
              <LoaderCircleIcon size={14} className="animate-spin" />
            ) : null}
            Dispatch next task
          </Button>
        </div>

        {dispatchTaskError ? (
          <div className="rounded-[0.75rem] border border-[#f59e7d]/18 bg-[#f59e7d]/8 px-3 py-2.5 text-sm leading-6 text-[var(--muted-foreground)]">
            {dispatchTaskError}
          </div>
        ) : null}

        {dispatchTaskResults.length > 0 ? (
          <div className="space-y-1.5">
            {dispatchTaskResults.map((result) => (
              <div
                key={`dispatch-result-${result.teamId}`}
                className="rounded-[0.8rem] border border-white/8 bg-white/4 px-3 py-2.5"
              >
                <div className="flex items-center justify-between gap-3">
                  <div className="text-[13px] font-semibold text-[var(--foreground)]">
                    {truncateIdentifier(result.teamId, 18)}
                  </div>
                  <span
                    className={cn(
                      dispatchStatusPillClass,
                      result.status === "created"
                        ? "border-[#8fd0c6]/24 bg-[#8fd0c6]/10 text-[#8fd0c6]"
                        : "border-[#f59e7d]/24 bg-[#f59e7d]/10 text-[#f59e7d]",
                    )}
                  >
                    {result.status}
                  </span>
                </div>
                <div className="mt-2 text-sm text-[var(--muted-foreground)]">
                  {result.status === "created"
                    ? `task ${truncateIdentifier(result.taskId, 18)} created`
                    : result.error || "dispatch failed"}
                </div>
              </div>
            ))}
          </div>
        ) : null}

        {dispatchTaskResults.length > 0 ? (
          <div className="rounded-[0.8rem] border border-white/8 bg-white/[0.03] px-3 py-2.5">
            <div className="flex items-center justify-between gap-3">
              <div>
                <div className="text-sm font-semibold text-[var(--foreground)]">
                  Dispatch monitor
                </div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  Follow task execution across the most recent fan-out batch.
                </div>
              </div>
              <Button
                variant="secondary"
                size="sm"
                onClick={onRefreshDispatchMonitor}
                disabled={isDispatchMonitorLoading}
              >
                {isDispatchMonitorLoading ? (
                  <LoaderCircleIcon size={14} className="animate-spin" />
                ) : null}
                Refresh monitor
              </Button>
            </div>

            {dispatchMonitorError ? (
              <div className="mt-2.5 rounded-[0.75rem] border border-[#f59e7d]/18 bg-[#f59e7d]/8 px-3 py-2.5 text-sm leading-6 text-[var(--muted-foreground)]">
                {dispatchMonitorError}
              </div>
            ) : null}

            <div className="mt-2.5 grid gap-2.5 xl:grid-cols-[minmax(0,1fr)_250px]">
              <div className="rounded-[0.8rem] border border-white/8 bg-black/15 px-3 py-2.5">
                <div className="app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                  Batch summary
                </div>
                <div className="mt-2.5 grid gap-1.5 text-xs text-[var(--muted-foreground)] sm:grid-cols-2">
                  <div className="rounded-[0.7rem] border border-white/8 bg-white/4 px-3 py-2">
                    <div className="app-text-10 uppercase tracking-[0.14em]">Attempted</div>
                    <div className="mt-1 text-sm font-semibold text-[var(--foreground)]">
                      {batchSummary.attemptedCount}
                    </div>
                  </div>
                  <div className="rounded-[0.7rem] border border-white/8 bg-white/4 px-3 py-2">
                    <div className="app-text-10 uppercase tracking-[0.14em]">Created</div>
                    <div className="mt-1 text-sm font-semibold text-[var(--foreground)]">
                      {batchSummary.createdCount}
                    </div>
                  </div>
                  <div className="rounded-[0.7rem] border border-white/8 bg-white/4 px-3 py-2">
                    <div className="app-text-10 uppercase tracking-[0.14em]">Failed</div>
                    <div className="mt-1 text-sm font-semibold text-[var(--foreground)]">
                      {batchSummary.failedCount}
                    </div>
                  </div>
                  <div className="rounded-[0.7rem] border border-white/8 bg-white/4 px-3 py-2">
                    <div className="app-text-10 uppercase tracking-[0.14em]">Completed</div>
                    <div className="mt-1 text-sm font-semibold text-[var(--foreground)]">
                      {batchSummary.completedCount}
                    </div>
                  </div>
                  <div className="rounded-[0.7rem] border border-white/8 bg-white/4 px-3 py-2">
                    <div className="app-text-10 uppercase tracking-[0.14em]">In progress</div>
                    <div className="mt-1 text-sm font-semibold text-[var(--foreground)]">
                      {batchSummary.activeCount}
                    </div>
                  </div>
                  <div className="rounded-[0.7rem] border border-white/8 bg-white/4 px-3 py-2">
                    <div className="app-text-10 uppercase tracking-[0.14em]">Pending</div>
                    <div className="mt-1 text-sm font-semibold text-[var(--foreground)]">
                      {batchSummary.pendingCount}
                    </div>
                  </div>
                  <div className="rounded-[0.7rem] border border-white/8 bg-white/4 px-3 py-2">
                    <div className="app-text-10 uppercase tracking-[0.14em]">Terminal</div>
                    <div className="mt-1 text-sm font-semibold text-[var(--foreground)]">
                      {batchSummary.terminalCount}
                    </div>
                  </div>
                  <div className="rounded-[0.7rem] border border-white/8 bg-white/4 px-3 py-2">
                    <div className="app-text-10 uppercase tracking-[0.14em]">Final summaries</div>
                    <div className="mt-1 text-sm font-semibold text-[var(--foreground)]">
                      {batchSummary.finalSummaryCount}
                    </div>
                  </div>
                  <div className="rounded-[0.7rem] border border-white/8 bg-white/4 px-3 py-2">
                    <div className="app-text-10 uppercase tracking-[0.14em]">Summary gaps</div>
                    <div className="mt-1 text-sm font-semibold text-[var(--foreground)]">
                      {batchSummary.terminalWithoutSummaryCount}
                    </div>
                  </div>
                  <div className="rounded-[0.7rem] border border-white/8 bg-white/4 px-3 py-2">
                    <div className="app-text-10 uppercase tracking-[0.14em]">Coverage</div>
                    <div className="mt-1 text-sm font-semibold text-[var(--foreground)]">
                      {batchSummary.monitorCoverageCount}/{batchSummary.createdCount}
                    </div>
                  </div>
                </div>
                <div className="mt-2.5 flex flex-wrap gap-1.5 text-xs uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
                  {batchStatusEntries.map(([status, count]) => (
                    <span
                      key={`dispatch-count-${status}`}
                      className={cn(
                        dispatchSummaryPillClass,
                        statusTone(status.includes("failed") ? "failed" : status),
                      )}
                    >
                      {status}: {count}
                    </span>
                  ))}
                  <span className="rounded-[0.65rem] border border-white/10 bg-white/4 px-2.5 py-1">
                    auto refresh:{" "}
                    {dispatchMonitor.some((entry) => !isTerminalDispatchStatus(entry.status))
                      ? "active"
                      : "idle"}
                  </span>
                  {batchSummary.latestUpdatedAt ? (
                    <span className="rounded-[0.65rem] border border-white/10 bg-white/4 px-2.5 py-1">
                      latest update: {formatRelativeTimestamp(batchSummary.latestUpdatedAt)}
                    </span>
                  ) : null}
                  {monitorCountEntries.length > 0 ? (
                    <span className="rounded-[0.65rem] border border-white/10 bg-white/4 px-2.5 py-1">
                      monitor entries: {dispatchMonitor.length}
                    </span>
                  ) : null}
                </div>
                {monitorCountEntries.length > 0 ? (
                  <div className="mt-2.5 flex flex-wrap gap-1.5 text-xs uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
                    {monitorCountEntries.map(([status, count]) => (
                      <span
                        key={`dispatch-monitor-count-${status}`}
                        className={cn(
                          dispatchSummaryPillClass,
                          statusTone(status.includes("failed") ? "failed" : status),
                        )}
                      >
                        monitor {status}: {count}
                      </span>
                    ))}
                  </div>
                ) : null}
              </div>

              <div className="rounded-[0.8rem] border border-white/8 bg-black/15 px-3 py-2.5">
                <div className="app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                  Outcome compare
                </div>
                <div className="mt-2.5 grid gap-1.5 text-xs text-[var(--muted-foreground)]">
                  <div className="rounded-[0.7rem] border border-white/8 bg-white/4 px-3 py-2">
                    <div className="app-text-10 uppercase tracking-[0.14em]">
                      Final summary coverage
                    </div>
                    <div className="mt-1 text-sm font-semibold text-[var(--foreground)]">
                      {batchSummary.finalSummaryCount}/
                      {batchSummary.summaryEligibleTerminalCount}
                    </div>
                  </div>
                  <div className="rounded-[0.7rem] border border-white/8 bg-white/4 px-3 py-2">
                    <div className="app-text-10 uppercase tracking-[0.14em]">
                      Waiting for summary
                    </div>
                    <div className="mt-1 text-sm font-semibold text-[var(--foreground)]">
                      {batchSummary.terminalWithoutSummaryCount}
                    </div>
                  </div>
                </div>
                <div className="mt-2.5 space-y-1.5">
                  {comparisonRows.map((row) => {
                    const narrative = getDispatchOutcomeNarrative(row);

                    return (
                      <div
                        key={`dispatch-compare-${row.teamId}-${row.taskId ?? "failed"}`}
                        className="rounded-[0.75rem] border border-white/8 bg-white/4 px-3 py-2"
                      >
                        <div className="flex items-center justify-between gap-3">
                          <div className="truncate text-[13px] font-semibold text-[var(--foreground)]">
                            {truncateIdentifier(row.teamId, 18)}
                          </div>
                          <span
                            className={cn(
                              dispatchStatusPillClass,
                              statusTone(row.created ? row.status : "failed"),
                            )}
                          >
                            {row.outcomeLabel}
                          </span>
                        </div>
                        <div className="mt-1.5 flex flex-wrap gap-1.5 app-text-11 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
                          <span>{row.detailLabel}</span>
                          <span>{row.detailText}</span>
                        </div>
                        <div className="mt-1.5">
                          <div className="app-text-11 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
                            {narrative.label}
                          </div>
                          <MessageMarkdown
                            className={`mt-1 ${compactMarkdownClass}`}
                            content={narrative.text}
                          />
                        </div>
                        <div className="mt-1.5 flex flex-wrap gap-1.5 app-text-11 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
                          <span>{row.isTerminal ? "terminal" : "active"}</span>
                          {row.taskId ? <span>task {truncateIdentifier(row.taskId, 16)}</span> : null}
                          {row.assignee ? <span>assignee {row.assignee}</span> : null}
                        </div>
                        {row.updatedAt ? (
                          <div className="mt-1.5 app-text-11 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
                            updated {formatRelativeTimestamp(row.updatedAt)}
                          </div>
                        ) : null}
                      </div>
                    );
                  })}
                </div>
                {terminalRowsWithSummary.length > 0 ? (
                  <div className="mt-3 rounded-[0.75rem] border border-white/8 bg-white/4 px-3 py-2.5">
                    <div className="app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                      Terminal outcomes with summary
                    </div>
                    <div className="mt-2.5 space-y-1.5">
                      {terminalRowsWithSummary.slice(0, 3).map((row) => (
                        <div
                          key={`dispatch-terminal-${row.teamId}-${row.taskId ?? row.outcomeKey}`}
                          className="rounded-[0.7rem] border border-white/8 bg-black/15 px-3 py-2"
                        >
                          <div className="flex items-center justify-between gap-3">
                            <div className="text-[11px] font-semibold text-[var(--foreground)]">
                              {truncateIdentifier(row.teamId, 18)}
                            </div>
                            <span
                              className={cn(
                                dispatchStatusPillClass,
                                statusTone(row.status),
                              )}
                            >
                              {row.outcomeLabel}
                            </span>
                          </div>
                          {row.summary ? (
                            <MessageMarkdown
                              className={`mt-1.5 ${compactMarkdownClass}`}
                              content={row.summary}
                            />
                          ) : null}
                        </div>
                      ))}
                    </div>
                  </div>
                ) : null}
                {terminalRowsMissingSummary.length > 0 ? (
                  <div className="mt-3 rounded-[0.75rem] border border-white/8 bg-white/4 px-3 py-2.5">
                    <div className="app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                      Terminal outcomes missing final summary
                    </div>
                    <div className="mt-2.5 space-y-1.5">
                      {terminalRowsMissingSummary.slice(0, 3).map((row) => {
                        const narrative = getDispatchOutcomeNarrative(row);

                        return (
                          <div
                            key={`dispatch-terminal-gap-${row.teamId}-${row.taskId ?? row.outcomeKey}`}
                            className="rounded-[0.7rem] border border-white/8 bg-black/15 px-3 py-2"
                          >
                            <div className="flex items-center justify-between gap-3">
                              <div className="text-[11px] font-semibold text-[var(--foreground)]">
                                {truncateIdentifier(row.teamId, 18)}
                              </div>
                              <span
                                className={cn(
                                  dispatchStatusPillClass,
                                  statusTone(row.status),
                                )}
                              >
                                {row.outcomeLabel}
                              </span>
                            </div>
                            <MessageMarkdown
                              className={`mt-1.5 ${compactMarkdownClass}`}
                              content={narrative.text}
                            />
                          </div>
                        );
                      })}
                    </div>
                  </div>
                ) : null}
              </div>
            </div>

            <div className="mt-2.5 space-y-1.5">
              {dispatchMonitor.length > 0 ? (
                dispatchMonitor.map((entry) => (
                  <div
                    key={`dispatch-monitor-${entry.teamId}-${entry.taskId}`}
                    className="rounded-[0.8rem] border border-white/8 bg-black/15 px-3 py-2.5"
                  >
                    <div className="flex items-start justify-between gap-3">
                      <div className="min-w-0">
                        <div className="truncate text-[13px] font-semibold text-[var(--foreground)]">
                          {truncateIdentifier(entry.teamId, 18)}
                        </div>
                        <div className="mt-0.5 text-xs text-[var(--muted-foreground)]">
                          task {truncateIdentifier(entry.taskId, 18)}
                        </div>
                      </div>
                      <span
                        className={cn(
                          dispatchStatusPillClass,
                          statusTone(entry.status),
                        )}
                      >
                        {entry.status}
                      </span>
                    </div>

                    <div className="mt-2 flex flex-wrap gap-2.5 text-xs text-[var(--muted-foreground)]">
                      {entry.assignee ? <span>assignee {entry.assignee}</span> : null}
                      {entry.lastEventType ? (
                        <span>event {prettyEventType(entry.lastEventType)}</span>
                      ) : null}
                      {entry.updatedAt ? (
                        <span>updated {formatRelativeTimestamp(entry.updatedAt)}</span>
                      ) : null}
                    </div>

                    {entry.summary ? (
                      <MessageMarkdown
                        className="mt-2 app-text-13 [&>p]:my-0 [&>ul]:my-2 [&>ol]:my-2 [&>blockquote]:my-2 [&>div]:my-2"
                        content={entry.summary}
                      />
                    ) : null}

                    {entry.mailboxPreview.length > 0 ? (
                      <div className="mt-2 space-y-1.5">
                        {entry.mailboxPreview.map((line, index) => (
                          <div
                            key={`dispatch-mailbox-${entry.teamId}-${entry.taskId}-${index}`}
                            className="rounded-[0.7rem] border border-white/8 bg-white/4 px-3 py-2"
                          >
                            <MessageMarkdown
                              className={compactMarkdownClass}
                              content={line}
                            />
                          </div>
                        ))}
                      </div>
                    ) : null}

                    {entry.error ? (
                      <div className="mt-2 rounded-[0.7rem] border border-[#f59e7d]/18 bg-[#f59e7d]/8 px-3 py-2 text-sm leading-6 text-[var(--muted-foreground)]">
                        {entry.error}
                      </div>
                    ) : null}
                  </div>
                ))
              ) : (
                <div className="text-sm text-[var(--muted-foreground)]">
                  No dispatch monitor data available yet.
                </div>
              )}
            </div>
          </div>
        ) : null}
      </div>
    </div>
  );
}
