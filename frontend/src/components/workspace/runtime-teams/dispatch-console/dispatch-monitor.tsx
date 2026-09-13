// 由 components/workspace/runtime-teams/dispatch-console.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { LoaderCircleIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { cn, formatRelativeTimestamp } from "@/lib/utils";

import {
  isTerminalDispatchStatus,
  prettyEventType,
  statusTone,
  truncateIdentifier,
} from "../shared";

import { consolePillClass } from "./console-styles";
import type { DispatchConsoleProps } from "./types";

type DispatchMonitorProps = Pick<
  DispatchConsoleProps,
  | "dispatchMonitor"
  | "dispatchMonitorCounts"
  | "dispatchMonitorError"
  | "dispatchTaskResults"
  | "isDispatchMonitorLoading"
  | "onRefreshDispatchMonitor"
>;

export function DispatchMonitor({
  dispatchMonitor,
  dispatchMonitorCounts,
  dispatchMonitorError,
  dispatchTaskResults,
  isDispatchMonitorLoading,
  onRefreshDispatchMonitor,
}: DispatchMonitorProps) {
  const { t } = useTranslation("workspace");

  return (
    <>
      {dispatchTaskResults.length > 0 ? (
        <div className="rounded-card-lg border border-white/8 bg-white/4 px-3 py-2.5">
          <div className="flex items-center justify-between gap-3">
            <div>
              <div className="text-sm font-semibold text-foreground">
                {t("panels.teamsDispatch.monitor.title")}
              </div>
              <div className="mt-1 text-xs text-muted-foreground">
                {t("panels.teamsDispatch.monitor.description")}
              </div>
            </div>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => onRefreshDispatchMonitor()}
              disabled={isDispatchMonitorLoading}
            >
              {isDispatchMonitorLoading ? (
                <LoaderCircleIcon size={14} className="animate-spin" />
              ) : null}
              {t("panels.teamsDispatch.monitor.refresh")}
            </Button>
          </div>

          {dispatchMonitorError ? (
            <div className="mt-2.5 rounded-card border border-accent-orange/18 bg-accent-orange/8 px-3 py-2.5 text-sm leading-6 text-muted-foreground">
              {dispatchMonitorError}
            </div>
          ) : null}

          {dispatchMonitor.length > 0 ? (
            <div className="mt-2.5 flex flex-wrap gap-1.5 text-xs uppercase tracking-[0.14em] text-muted-foreground">
              {Object.entries(dispatchMonitorCounts).map(([status, count]) => (
                <span
                  key={`dispatch-count-${status}`}
                  className={cn("rounded-control border px-2 py-0.5", statusTone(status))}
                >
                  {status}: {count}
                </span>
              ))}
              <span className="rounded-control border border-white/10 bg-white/4 px-2 py-0.5">
                {t("panels.teamsDispatch.monitor.autoRefreshLabel")}{" "}
                {t(
                  dispatchMonitor.some(
                    (entry) => !isTerminalDispatchStatus(entry.status),
                  )
                    ? "panels.teamsDispatch.monitor.autoRefreshActive"
                    : "panels.teamsDispatch.monitor.autoRefreshIdle",
                )}
              </span>
            </div>
          ) : null}

          <div className="mt-2.5 space-y-1.5">
            {dispatchMonitor.length > 0 ? (
              dispatchMonitor.map((entry) => (
                <div
                  key={`dispatch-monitor-${entry.teamId}-${entry.taskId}`}
                  className="rounded-card border border-white/8 bg-black/20 px-3 py-2.5"
                >
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <div className="truncate text-sm font-semibold text-foreground">
                        {truncateIdentifier(entry.teamId, 18)}
                      </div>
                      <div className="mt-1 text-xs text-muted-foreground">
                        {t("panels.teamsDispatch.monitor.taskEntry", {
                          taskId: truncateIdentifier(entry.taskId, 18),
                        })}
                      </div>
                    </div>
                    <span
                      className={cn(
                        consolePillClass,
                        statusTone(entry.status),
                      )}
                    >
                      {entry.status}
                    </span>
                  </div>

                  <div className="mt-2.5 flex flex-wrap gap-3 text-xs text-muted-foreground">
                    {entry.assignee ? (
                      <span>
                        {t("panels.teamsDispatch.monitor.assigneeEntry", {
                          assignee: entry.assignee,
                        })}
                      </span>
                    ) : null}
                    {entry.lastEventType ? (
                      <span>
                        {t("panels.teamsDispatch.monitor.eventEntry", {
                          event: prettyEventType(entry.lastEventType),
                        })}
                      </span>
                    ) : null}
                    {entry.updatedAt ? (
                      <span>
                        {t("panels.teamsDispatch.monitor.updatedEntry", {
                          time: formatRelativeTimestamp(entry.updatedAt),
                        })}
                      </span>
                    ) : null}
                  </div>

                  {entry.summary ? (
                    <div className="mt-2.5 text-sm leading-6 text-foreground">
                      {entry.summary}
                    </div>
                  ) : null}

                  {entry.mailboxPreview.length > 0 ? (
                    <div className="mt-2.5 space-y-1.5">
                      {entry.mailboxPreview.map((line, index) => (
                        <div
                          key={`dispatch-mailbox-${entry.teamId}-${entry.taskId}-${index}`}
                          className="rounded-[0.75rem] border border-white/8 bg-white/4 px-3 py-2 text-sm leading-6 text-muted-foreground"
                        >
                          {line}
                        </div>
                      ))}
                    </div>
                  ) : null}

                  {entry.error ? (
                    <div className="mt-2.5 rounded-[0.75rem] border border-accent-orange/18 bg-accent-orange/8 px-3 py-2 text-sm leading-6 text-muted-foreground">
                      {entry.error}
                    </div>
                  ) : null}
                </div>
              ))
            ) : (
              <div className="text-sm text-muted-foreground">
                {t("panels.teamsDispatch.monitor.empty")}
              </div>
            )}
          </div>
        </div>
      ) : null}
    </>
  );
}
