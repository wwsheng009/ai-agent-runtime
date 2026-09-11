import { LoaderCircleIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  buildDispatchComparisonRows,
  type DispatchMonitorEntry,
  type MultiTeamDispatchResult,
  sortDispatchComparisonRows,
  summarizeDispatchBatch,
} from "@/components/workspace/runtime-teams/shared";

import { DispatchMonitorEntries } from "./monitor-entries";
import { DispatchMonitorOutcomeCompare } from "./monitor-outcome-compare";
import { DispatchMonitorSummary } from "./monitor-summary";

type DispatchMonitorPanelProps = {
  dispatchMonitor: DispatchMonitorEntry[];
  dispatchMonitorCounts: Record<string, number>;
  dispatchMonitorError: string | null;
  dispatchTaskResults: MultiTeamDispatchResult[];
  isDispatchMonitorLoading: boolean;
  onRefreshDispatchMonitor: () => void | Promise<void>;
};

export function DispatchMonitorPanel({
  dispatchMonitor,
  dispatchMonitorCounts,
  dispatchMonitorError,
  dispatchTaskResults,
  isDispatchMonitorLoading,
  onRefreshDispatchMonitor,
}: DispatchMonitorPanelProps) {
  const comparisonRows = sortDispatchComparisonRows(
    buildDispatchComparisonRows(dispatchTaskResults, dispatchMonitor),
  );
  const batchSummary = summarizeDispatchBatch(comparisonRows);

  return (
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
        <DispatchMonitorSummary
          batchSummary={batchSummary}
          dispatchMonitor={dispatchMonitor}
          dispatchMonitorCounts={dispatchMonitorCounts}
        />

        <DispatchMonitorOutcomeCompare
          batchSummary={batchSummary}
          comparisonRows={comparisonRows}
        />
      </div>

      <DispatchMonitorEntries dispatchMonitor={dispatchMonitor} />
    </div>
  );
}
