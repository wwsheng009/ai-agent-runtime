// P2-1A：后台任务单行（状态徽标/时长/退出码 + 输出展开增量读取 + 交互输入框）。
// 由 components/workspace/jobs-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  ChevronDownIcon,
  ChevronRightIcon,
  CornerDownLeftIcon,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { JobActions } from "@/components/workspace/jobs-panel-job-actions";
import {
  formatJobDuration,
  formatJobTimestamp,
  isLiveJobStatus,
  jobStatusLabelKey,
  resolveJobElapsedMs,
  type JobAction,
  type JobActionPending,
} from "@/components/workspace/jobs-panel-shared";
import { cn } from "@/lib/utils";
import type { RuntimeJob } from "@/types/runtime";

import {
  jobStatusToneClass,
  type JobInputState,
  type JobOutputState,
} from "./helpers";

type JobRowProps = {
  expanded: boolean;
  job: RuntimeJob;
  now: number;
  output?: JobOutputState;
  input?: JobInputState;
  pendingAction: JobActionPending;
  onAction: (jobId: string, action: JobAction) => void;
  onToggleOutput: (jobId: string) => void;
  onLoadMore: (jobId: string) => void;
  onSendInput: (jobId: string) => void;
  onInputChange: (jobId: string, value: string) => void;
};

export function JobRow({
  expanded,
  job,
  now,
  input,
  pendingAction,
  onAction,
  onLoadMore,
  onToggleOutput,
  onInputChange,
  onSendInput,
  output,
}: JobRowProps) {
  const { t } = useTranslation("workspace");
  const live = isLiveJobStatus(job.status);
  const elapsed = formatJobDuration(resolveJobElapsedMs(job, now));

  return (
    <li className="rounded-card border border-border bg-surface-softer px-2.5 py-2">
      <div className="flex items-start gap-2">
        <Button
          aria-expanded={expanded}
          aria-label={t("panels.jobs.output.toggle", { command: job.command })}
          className="mt-0.5 h-5 w-5 shrink-0 px-0"
          onClick={() => onToggleOutput(job.id)}
          size="icon"
          title={expanded ? t("panels.jobs.output.hide") : t("panels.jobs.output.show")}
          variant="ghost"
        >
          {expanded ? <ChevronDownIcon size={13} /> : <ChevronRightIcon size={13} />}
        </Button>
        <div className="min-w-0 flex-1">
          <div className="truncate app-text-13 font-medium" title={job.command}>
            {job.command || job.id}
          </div>
          <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 app-text-10 text-muted-foreground">
            <Badge
              className={cn("h-5 px-1.5 app-text-10", jobStatusToneClass(job.status))}
            >
              {t(jobStatusLabelKey(job.status), { defaultValue: job.status })}
            </Badge>
            {live ? (
              <span title={job.startedAt || job.createdAt}>
                {t("panels.jobs.elapsed", { duration: elapsed })}
              </span>
            ) : (
              <span title={job.finishedAt || job.createdAt}>
                {t("panels.jobs.duration", { duration: elapsed })}
              </span>
            )}
            {job.exitCode !== null ? (
              <span>{t("panels.jobs.exitCode", { code: String(job.exitCode) })}</span>
            ) : null}
            {job.cwd ? (
              <span className="truncate" title={job.cwd}>
                {job.cwd}
              </span>
            ) : null}
            {!live ? (
              <span title={formatJobTimestamp(job.finishedAt || job.createdAt)}>
                {t("panels.jobs.finishedAt", {
                  time: formatJobTimestamp(job.finishedAt || job.createdAt),
                })}
              </span>
            ) : null}
          </div>
          {job.message ? (
            <div className="mt-1 break-words app-text-11 text-muted-foreground">
              {job.message}
            </div>
          ) : null}
        </div>
        <JobActions
          jobId={job.id}
          onAction={onAction}
          pendingAction={pendingAction}
          status={job.status}
        />
      </div>

      {expanded ? (
        <div className="mt-2 border-t border-border pt-2">
          {output?.loading && !output.text ? (
            <div className="app-text-11 text-muted-foreground">
              {t("panels.jobs.output.loading")}
            </div>
          ) : null}
          {output?.error ? (
            <div className="app-text-11 text-analytics-danger">{output.error}</div>
          ) : null}
          {output && !output.error && (output.text || !output.loading) ? (
            <pre
              className="max-h-56 overflow-auto whitespace-pre-wrap break-all rounded-card bg-surface-softer px-2 py-1.5 app-text-11 text-foreground"
              data-testid="job-output"
            >
              {output.text || t("panels.jobs.output.empty")}
            </pre>
          ) : null}
          {output?.hasMore ? (
            <Button
              className="mt-1.5"
              disabled={output.loading}
              onClick={() => onLoadMore(job.id)}
              size="sm"
              variant="ghost"
            >
              {t("panels.jobs.output.loadMore")}
            </Button>
          ) : null}
        </div>
      ) : null}

      {job.interactive && live ? (
        // 输入框独立于输出展开态：用户可以不展开输出直接向会话发指令
        //（2026-10-11）。发送回执由控制器返回的 exchange 追加进输出窗口。
        <form
          className="mt-2 flex items-center gap-1.5"
          onSubmit={(event) => {
            event.preventDefault();
            void onSendInput(job.id);
          }}
        >
          <input
            aria-label={t("panels.jobs.input.label", { command: job.command })}
            className="h-7 min-w-0 flex-1 rounded-card border border-border bg-surface px-2 app-text-11 text-foreground placeholder:text-muted-foreground"
            data-testid="job-input"
            disabled={input?.sending}
            onChange={(event) => onInputChange(job.id, event.target.value)}
            placeholder={t("panels.jobs.input.placeholder")}
            type="text"
            value={input?.value ?? ""}
          />
          <Button
            aria-label={t("panels.jobs.input.send")}
            className="h-7 w-7 shrink-0 px-0"
            disabled={input?.sending || !(input?.value ?? "").trim()}
            size="icon"
            type="submit"
            variant="ghost"
          >
            <CornerDownLeftIcon size={13} />
          </Button>
          {input?.error ? (
            <span className="app-text-10 text-analytics-danger">{input.error}</span>
          ) : null}
        </form>
      ) : null}
    </li>
  );
}
