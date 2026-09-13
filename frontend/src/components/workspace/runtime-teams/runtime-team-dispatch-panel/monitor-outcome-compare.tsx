import { MessageMarkdown } from "@/components/workspace/message-markdown";
import {
  type DispatchBatchSummary,
  type DispatchComparisonRow,
  getDispatchOutcomeNarrative,
  shouldExpectDispatchOutcomeSummary,
  statusTone,
  truncateIdentifier,
} from "@/components/workspace/runtime-teams/shared";
import { cn, formatRelativeTimestamp } from "@/lib/utils";

import { compactMarkdownClass, dispatchStatusPillClass } from "./format";

type DispatchMonitorOutcomeCompareProps = {
  batchSummary: DispatchBatchSummary;
  comparisonRows: DispatchComparisonRow[];
};

export function DispatchMonitorOutcomeCompare({
  batchSummary,
  comparisonRows,
}: DispatchMonitorOutcomeCompareProps) {
  const terminalRowsWithSummary = batchSummary.terminalRows.filter(
    (row) => shouldExpectDispatchOutcomeSummary(row) && Boolean(row.summary?.trim()),
  );
  const terminalRowsMissingSummary = batchSummary.terminalRows.filter(
    (row) => shouldExpectDispatchOutcomeSummary(row) && !row.summary?.trim(),
  );

  return (
    <div className="rounded-[0.8rem] border border-white/8 bg-black/15 px-3 py-2.5">
      <div className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
        Outcome compare
      </div>
      <div className="mt-2.5 grid gap-1.5 text-xs text-muted-foreground">
        <div className="rounded-[0.7rem] border border-white/8 bg-white/4 px-3 py-2">
          <div className="app-text-10 uppercase tracking-[0.14em]">
            Final summary coverage
          </div>
          <div className="mt-1 text-sm font-semibold text-foreground">
            {batchSummary.finalSummaryCount}/
            {batchSummary.summaryEligibleTerminalCount}
          </div>
        </div>
        <div className="rounded-[0.7rem] border border-white/8 bg-white/4 px-3 py-2">
          <div className="app-text-10 uppercase tracking-[0.14em]">
            Waiting for summary
          </div>
          <div className="mt-1 text-sm font-semibold text-foreground">
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
                <div className="truncate text-[13px] font-semibold text-foreground">
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
              <div className="mt-1.5 flex flex-wrap gap-1.5 app-text-11 uppercase tracking-[0.12em] text-muted-foreground">
                <span>{row.detailLabel}</span>
                <span>{row.detailText}</span>
              </div>
              <div className="mt-1.5">
                <div className="app-text-11 uppercase tracking-[0.12em] text-muted-foreground">
                  {narrative.label}
                </div>
                <MessageMarkdown
                  className={`mt-1 ${compactMarkdownClass}`}
                  content={narrative.text}
                />
              </div>
              <div className="mt-1.5 flex flex-wrap gap-1.5 app-text-11 uppercase tracking-[0.12em] text-muted-foreground">
                <span>{row.isTerminal ? "terminal" : "active"}</span>
                {row.taskId ? <span>task {truncateIdentifier(row.taskId, 16)}</span> : null}
                {row.assignee ? <span>assignee {row.assignee}</span> : null}
              </div>
              {row.updatedAt ? (
                <div className="mt-1.5 app-text-11 uppercase tracking-[0.12em] text-muted-foreground">
                  updated {formatRelativeTimestamp(row.updatedAt)}
                </div>
              ) : null}
            </div>
          );
        })}
      </div>
      {terminalRowsWithSummary.length > 0 ? (
        <div className="mt-3 rounded-[0.75rem] border border-white/8 bg-white/4 px-3 py-2.5">
          <div className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
            Terminal outcomes with summary
          </div>
          <div className="mt-2.5 space-y-1.5">
            {terminalRowsWithSummary.slice(0, 3).map((row) => (
              <div
                key={`dispatch-terminal-${row.teamId}-${row.taskId ?? row.outcomeKey}`}
                className="rounded-[0.7rem] border border-white/8 bg-black/15 px-3 py-2"
              >
                <div className="flex items-center justify-between gap-3">
                  <div className="text-[11px] font-semibold text-foreground">
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
          <div className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
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
                    <div className="text-[11px] font-semibold text-foreground">
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
  );
}
