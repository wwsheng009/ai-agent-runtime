import {
  type DispatchBatchSummary,
  type DispatchMonitorEntry,
  isTerminalDispatchStatus,
  statusTone,
} from "@/components/workspace/runtime-teams/shared";
import { cn, formatRelativeTimestamp } from "@/lib/utils";

import { dispatchSummaryPillClass } from "./format";

type DispatchMonitorSummaryProps = {
  batchSummary: DispatchBatchSummary;
  dispatchMonitor: DispatchMonitorEntry[];
  dispatchMonitorCounts: Record<string, number>;
};

export function DispatchMonitorSummary({
  batchSummary,
  dispatchMonitor,
  dispatchMonitorCounts,
}: DispatchMonitorSummaryProps) {
  const batchStatusEntries = Object.entries(batchSummary.statusCounts);
  const monitorCountEntries = Object.entries(dispatchMonitorCounts);

  return (
    <div className="rounded-card border border-white/8 bg-black/15 px-3 py-2.5">
      <div className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
        Batch summary
      </div>
      <div className="mt-2.5 grid gap-1.5 text-xs text-muted-foreground sm:grid-cols-2">
        <div className="rounded-field border border-white/8 bg-white/4 px-3 py-2">
          <div className="app-text-10 uppercase tracking-[0.14em]">Attempted</div>
          <div className="mt-1 text-sm font-semibold text-foreground">
            {batchSummary.attemptedCount}
          </div>
        </div>
        <div className="rounded-field border border-white/8 bg-white/4 px-3 py-2">
          <div className="app-text-10 uppercase tracking-[0.14em]">Created</div>
          <div className="mt-1 text-sm font-semibold text-foreground">
            {batchSummary.createdCount}
          </div>
        </div>
        <div className="rounded-field border border-white/8 bg-white/4 px-3 py-2">
          <div className="app-text-10 uppercase tracking-[0.14em]">Failed</div>
          <div className="mt-1 text-sm font-semibold text-foreground">
            {batchSummary.failedCount}
          </div>
        </div>
        <div className="rounded-field border border-white/8 bg-white/4 px-3 py-2">
          <div className="app-text-10 uppercase tracking-[0.14em]">Completed</div>
          <div className="mt-1 text-sm font-semibold text-foreground">
            {batchSummary.completedCount}
          </div>
        </div>
        <div className="rounded-field border border-white/8 bg-white/4 px-3 py-2">
          <div className="app-text-10 uppercase tracking-[0.14em]">In progress</div>
          <div className="mt-1 text-sm font-semibold text-foreground">
            {batchSummary.activeCount}
          </div>
        </div>
        <div className="rounded-field border border-white/8 bg-white/4 px-3 py-2">
          <div className="app-text-10 uppercase tracking-[0.14em]">Pending</div>
          <div className="mt-1 text-sm font-semibold text-foreground">
            {batchSummary.pendingCount}
          </div>
        </div>
        <div className="rounded-field border border-white/8 bg-white/4 px-3 py-2">
          <div className="app-text-10 uppercase tracking-[0.14em]">Terminal</div>
          <div className="mt-1 text-sm font-semibold text-foreground">
            {batchSummary.terminalCount}
          </div>
        </div>
        <div className="rounded-field border border-white/8 bg-white/4 px-3 py-2">
          <div className="app-text-10 uppercase tracking-[0.14em]">Final summaries</div>
          <div className="mt-1 text-sm font-semibold text-foreground">
            {batchSummary.finalSummaryCount}
          </div>
        </div>
        <div className="rounded-field border border-white/8 bg-white/4 px-3 py-2">
          <div className="app-text-10 uppercase tracking-[0.14em]">Summary gaps</div>
          <div className="mt-1 text-sm font-semibold text-foreground">
            {batchSummary.terminalWithoutSummaryCount}
          </div>
        </div>
        <div className="rounded-field border border-white/8 bg-white/4 px-3 py-2">
          <div className="app-text-10 uppercase tracking-[0.14em]">Coverage</div>
          <div className="mt-1 text-sm font-semibold text-foreground">
            {batchSummary.monitorCoverageCount}/{batchSummary.createdCount}
          </div>
        </div>
      </div>
      <div className="mt-2.5 flex flex-wrap gap-1.5 text-xs uppercase tracking-[0.12em] text-muted-foreground">
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
        <span className="rounded-control border border-white/10 bg-white/4 px-2.5 py-1">
          auto refresh:{" "}
          {dispatchMonitor.some((entry) => !isTerminalDispatchStatus(entry.status))
            ? "active"
            : "idle"}
        </span>
        {batchSummary.latestUpdatedAt ? (
          <span className="rounded-control border border-white/10 bg-white/4 px-2.5 py-1">
            latest update: {formatRelativeTimestamp(batchSummary.latestUpdatedAt)}
          </span>
        ) : null}
        {monitorCountEntries.length > 0 ? (
          <span className="rounded-control border border-white/10 bg-white/4 px-2.5 py-1">
            monitor entries: {dispatchMonitor.length}
          </span>
        ) : null}
      </div>
      {monitorCountEntries.length > 0 ? (
        <div className="mt-2.5 flex flex-wrap gap-1.5 text-xs uppercase tracking-[0.12em] text-muted-foreground">
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
  );
}
