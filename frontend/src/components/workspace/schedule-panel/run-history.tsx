// P3：单任务运行历史（J3：按需展开；展开即取一次，折叠不丢缓存——缓存由父级持有）。
// 由 components/workspace/schedule-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { HistoryIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { formatScheduleTimestamp } from "@/components/workspace/schedule-panel-shared";
import type { RuntimeScheduleJobRun } from "@/types/runtime";

export type ScheduleRunHistoryProps = {
  expanded: boolean;
  runs: RuntimeScheduleJobRun[];
  status?: "loading" | "error";
  onToggle: () => void;
};

export function ScheduleRunHistory({
  expanded,
  onToggle,
  runs,
  status,
}: ScheduleRunHistoryProps) {
  const { t } = useTranslation("workspace");
  return (
    <div className="mt-1">
      {/* J3：运行历史（按需展开）。 */}
      <Button
        className="h-6 px-2 text-xs"
        onClick={onToggle}
        size="sm"
        type="button"
        variant="ghost"
      >
        <HistoryIcon className="mr-1 h-3 w-3" />
        {t("panels.schedule.runHistory")}
      </Button>
      {expanded ? (
        <div className="mt-1 space-y-1 text-xs">
          {status === "loading" ? (
            <div className="text-muted-foreground">{t("panels.schedule.refresh")}…</div>
          ) : status === "error" ? (
            <div className="text-destructive">{t("panels.schedule.runError")}</div>
          ) : runs.length === 0 ? (
            <div className="text-muted-foreground">{t("panels.schedule.runEmpty")}</div>
          ) : (
            <ul className="space-y-1">
              {runs.map((run) => (
                <li key={run.id} className="rounded border border-border px-2 py-1">
                  <div className="flex items-center gap-2">
                    <Badge
                      className={
                        run.status === "failed"
                          ? "border-destructive/40 text-destructive"
                          : "border-emerald-500/40 text-emerald-600 dark:text-emerald-400"
                      }
                    >
                      {run.status === "failed"
                        ? t("panels.schedule.runStatus.failed")
                        : t("panels.schedule.runStatus.done")}
                    </Badge>
                    <span className="text-muted-foreground">
                      {formatScheduleTimestamp(run.finishedAt)}
                    </span>
                  </div>
                  {run.output ? (
                    <div className="mt-1 whitespace-pre-wrap">
                      {t("panels.schedule.runOutput")}: {run.output}
                    </div>
                  ) : null}
                  {run.error ? (
                    <div className="mt-1 text-destructive">
                      {t("panels.schedule.runError")}: {run.error}
                    </div>
                  ) : null}
                </li>
              ))}
            </ul>
          )}
        </div>
      ) : null}
    </div>
  );
}
