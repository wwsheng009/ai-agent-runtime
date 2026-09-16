import { MessageMarkdown } from "@/components/workspace/message-markdown";
import {
  type DispatchMonitorEntry,
  prettyEventType,
  statusTone,
  truncateIdentifier,
} from "@/components/workspace/runtime-teams/shared";
import { cn, formatRelativeTimestamp } from "@/lib/utils";
import { useTranslation } from "react-i18next";

import { compactMarkdownClass, dispatchStatusPillClass } from "./format";

type DispatchMonitorEntriesProps = {
  dispatchMonitor: DispatchMonitorEntry[];
};

export function DispatchMonitorEntries({ dispatchMonitor }: DispatchMonitorEntriesProps) {
  const { t } = useTranslation("workspace");

  return (
    <div className="mt-2.5 space-y-1.5">
      {dispatchMonitor.length > 0 ? (
        dispatchMonitor.map((entry) => (
          <div
            key={`dispatch-monitor-${entry.teamId}-${entry.taskId}`}
            className="rounded-card border border-white/8 bg-black/15 px-3 py-2.5"
          >
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <div className="truncate app-text-13 font-semibold text-foreground">
                  {truncateIdentifier(entry.teamId, 18)}
                </div>
                <div className="mt-0.5 text-xs text-muted-foreground">
                  {t("panels.teamsDispatch.monitor.taskEntry", {
                    taskId: truncateIdentifier(entry.taskId, 18),
                  })}
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

            <div className="mt-2 flex flex-wrap gap-2.5 text-xs text-muted-foreground">
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
                    className="rounded-field border border-white/8 bg-white/4 px-3 py-2"
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
              <div className="mt-2 rounded-field border border-accent-orange/18 bg-accent-orange/8 px-3 py-2 text-sm leading-6 text-muted-foreground">
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
  );
}
