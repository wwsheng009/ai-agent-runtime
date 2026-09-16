// P2-1A：后台任务（Jobs）弹层。
//
// 数据全部来自 `/api/runtime/background/jobs*`（无假数据）：live（pending/running）
// 与 settled（终态）分区展示，live 行本地走秒（elapsed tick），行内可展开读取
// 增量输出（REST `/output` 分页），live 行可取消。
// P2-9：数据由 shell owner 的 `useBackgroundJobs` 单例提供（弹层与顶栏状态条共用同一份，
// 保证「状态条计数 == 弹层 live 计数」），面板只渲染与转发动作。
// 交互：Esc / 遮罩点击关闭，关闭后焦点回到触发按钮（use-focus-restore）。

import {
  AlertTriangleIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  ListChecksIcon,
  RefreshCwIcon,
  XIcon,
} from "lucide-react";
import { useCallback, useMemo, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";
import {
  formatJobDuration,
  formatJobTimestamp,
  isLiveJobStatus,
  jobStatusLabelKey,
  resolveJobElapsedMs,
  splitRuntimeJobs,
} from "@/components/workspace/jobs-panel-shared";
import { type BackgroundJobsController } from "@/hooks/workspace/use-background-jobs";
import { useElapsedTick } from "@/hooks/workspace/use-elapsed-tick";
import { useFocusRestore } from "@/hooks/workspace/use-focus-restore";
import { getRuntimeJobOutput } from "@/lib/runtime-api";
import type { RuntimeJob, RuntimeJobStatus } from "@/types/runtime";
import { cn } from "@/lib/utils";

// 单次输出读取上限（后端 ReadOutput 的 limit 参数）。
const outputChunkLimit = 8192;

type JobOutputState = {
  text: string;
  nextOffset: number;
  hasMore: boolean;
  loading: boolean;
  error: string | null;
};

const emptyOutputState: JobOutputState = {
  text: "",
  nextOffset: 0,
  hasMore: false,
  loading: false,
  error: null,
};

export type JobsPanelProps = {
  /** P2-9：由 shell owner 创建一次的数据控制器（与顶栏状态条同源）。 */
  controller: BackgroundJobsController;
  onClose: () => void;
  open: boolean;
};

export function JobsPanel({
  controller,
  onClose,
  open,
}: JobsPanelProps) {
  const { t } = useTranslation("workspace");
  const { cancel, cancellingId, error, hasLiveJobs, jobs, loading, refresh } =
    controller;
  const [expandedJobId, setExpandedJobId] = useState("");
  const [outputs, setOutputs] = useState<Record<string, JobOutputState>>({});

  useDialogLifecycle(open, onClose);
  useFocusRestore(open);

  const { live, settled } = useMemo(() => splitRuntimeJobs(jobs), [jobs]);
  const now = useElapsedTick(open && hasLiveJobs);

  const loadOutput = useCallback(
    async (jobId: string, offset: number) => {
      setOutputs((current) => ({
        ...current,
        [jobId]: { ...(current[jobId] ?? emptyOutputState), loading: true, error: null },
      }));
      try {
        const chunk = await getRuntimeJobOutput(jobId, {
          offset,
          limit: outputChunkLimit,
        });
        setOutputs((current) => {
          const previous = current[jobId] ?? emptyOutputState;
          const text = offset === 0 ? chunk.output : previous.text + chunk.output;
          return {
            ...current,
            [jobId]: {
              text,
              nextOffset: chunk.nextOffset,
              hasMore: chunk.output.length > 0,
              loading: false,
              error: null,
            },
          };
        });
      } catch (caught) {
        setOutputs((current) => ({
          ...current,
          [jobId]: {
            ...(current[jobId] ?? emptyOutputState),
            loading: false,
            error: caught instanceof Error ? caught.message : String(caught),
          },
        }));
      }
    },
    [],
  );

  const handleToggleOutput = useCallback(
    (jobId: string) => {
      setExpandedJobId((current) => (current === jobId ? "" : jobId));
      if (!outputs[jobId]) {
        void loadOutput(jobId, 0);
      }
    },
    [loadOutput, outputs],
  );

  const handleLoadMore = useCallback(
    (jobId: string) => {
      const state = outputs[jobId];
      if (!state || state.loading) {
        return;
      }
      void loadOutput(jobId, state.nextOffset);
    },
    [loadOutput, outputs],
  );

  if (!open) {
    return null;
  }

  if (typeof document === "undefined") {
    return null;
  }

  return createPortal(
    <DialogOverlay className="z-[120] backdrop-blur-sm" onDismiss={onClose}>
      <DialogPanel
        aria-label={t("panels.jobs.ariaLabel")}
        className="max-w-3xl"
        data-testid="jobs-panel"
        role="dialog"
      >
        <div className="flex items-start justify-between gap-3 border-b border-border px-3.5 py-3 sm:px-4">
          <div className="min-w-0">
            <div className="app-text-11 uppercase tracking-[0.16em] text-accent-secondary">
              {t("panels.jobs.eyebrow")}
            </div>
            <h2 className="mt-1 flex items-center gap-2 text-lg font-semibold tracking-[-0.03em] text-foreground">
              <ListChecksIcon size={17} className="text-accent-primary" />
              {t("panels.jobs.title")}
            </h2>
            <p className="mt-1 max-w-2xl text-sm leading-6 text-muted-foreground">
              {t("panels.jobs.description")}
            </p>
          </div>
          <div className="flex shrink-0 items-center gap-1">
            <Button
              aria-label={t("panels.jobs.refresh")}
              disabled={loading}
              onClick={refresh}
              size="icon"
              title={t("panels.jobs.refresh")}
              variant="ghost"
            >
              <RefreshCwIcon size={15} className={cn(loading && "animate-spin")} />
            </Button>
            <Button
              aria-label={t("panels.jobs.close")}
              onClick={onClose}
              size="icon"
              title={t("panels.jobs.close")}
              variant="ghost"
            >
              <XIcon size={16} />
            </Button>
          </div>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto px-3.5 py-3.5 sm:px-4">
          {error ? (
            <div className="mb-3 flex items-start gap-2 rounded-card border border-border bg-surface-softer px-2.5 py-2 text-xs text-analytics-danger">
              <AlertTriangleIcon size={14} className="mt-0.5 shrink-0" />
              <div className="min-w-0">
                <div className="font-medium">{t("panels.jobs.errorTitle")}</div>
                <div className="mt-0.5 break-words text-muted-foreground">{error}</div>
              </div>
            </div>
          ) : null}

          {loading && jobs.length === 0 && !error ? (
            <div className="flex items-center gap-2 rounded-card border border-border bg-surface-softer px-2.5 py-2 text-xs text-muted-foreground">
              <RefreshCwIcon size={13} className="animate-spin" />
              {t("panels.jobs.loading")}
            </div>
          ) : null}

          {!error && !loading && jobs.length === 0 ? (
            <div className="rounded-card border border-border bg-surface-softer px-3 py-3 text-sm">
              <div className="font-medium text-foreground">{t("panels.jobs.empty")}</div>
              <p className="mt-1 text-xs leading-5 text-muted-foreground">
                {t("panels.jobs.emptyHint")}
              </p>
            </div>
          ) : null}

          {live.length > 0 ? (
            <JobSection
              jobs={live}
              label={t("panels.jobs.live", { count: live.length })}
              onCancel={cancel}
              cancellingId={cancellingId}
              now={now}
              onToggleOutput={handleToggleOutput}
              onLoadMore={handleLoadMore}
              expandedJobId={expandedJobId}
              outputs={outputs}
            />
          ) : null}

          {settled.length > 0 ? (
            <JobSection
              className={live.length > 0 ? "mt-4" : undefined}
              jobs={settled}
              label={t("panels.jobs.settled", { count: settled.length })}
              onCancel={cancel}
              cancellingId={cancellingId}
              now={now}
              onToggleOutput={handleToggleOutput}
              onLoadMore={handleLoadMore}
              expandedJobId={expandedJobId}
              outputs={outputs}
            />
          ) : null}
        </div>
      </DialogPanel>
    </DialogOverlay>,
    document.body,
  );
}

type JobSectionProps = {
  className?: string;
  jobs: RuntimeJob[];
  label: string;
  cancellingId: string;
  now: number;
  expandedJobId: string;
  outputs: Record<string, JobOutputState>;
  onCancel: (jobId: string) => void;
  onToggleOutput: (jobId: string) => void;
  onLoadMore: (jobId: string) => void;
};

function JobSection({
  cancellingId,
  className,
  expandedJobId,
  jobs,
  label,
  now,
  onCancel,
  onLoadMore,
  onToggleOutput,
  outputs,
}: JobSectionProps) {
  return (
    <section className={className} aria-label={label}>
      <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
        {label}
      </div>
      <ul className="mt-1.5 space-y-1.5">
        {jobs.map((job) => (
          <JobRow
            cancelling={cancellingId === job.id}
            expanded={expandedJobId === job.id}
            job={job}
            key={job.id}
            now={now}
            onCancel={onCancel}
            onLoadMore={onLoadMore}
            onToggleOutput={onToggleOutput}
            output={outputs[job.id]}
          />
        ))}
      </ul>
    </section>
  );
}

type JobRowProps = {
  cancelling: boolean;
  expanded: boolean;
  job: RuntimeJob;
  now: number;
  output?: JobOutputState;
  onCancel: (jobId: string) => void;
  onToggleOutput: (jobId: string) => void;
  onLoadMore: (jobId: string) => void;
};

function JobRow({
  cancelling,
  expanded,
  job,
  now,
  onCancel,
  onLoadMore,
  onToggleOutput,
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
        {live ? (
          <Button
            className="shrink-0"
            disabled={cancelling}
            onClick={() => onCancel(job.id)}
            size="sm"
            variant="ghost"
          >
            {cancelling ? t("panels.jobs.cancelling") : t("panels.jobs.cancel")}
          </Button>
        ) : null}
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
    </li>
  );
}

function jobStatusToneClass(status: RuntimeJobStatus): string {
  switch (status) {
    case "running":
      return "text-accent-primary";
    case "pending":
      return "text-analytics-warning";
    case "failed":
    case "timed_out":
      return "text-analytics-danger";
    default:
      return "text-muted-foreground";
  }
}
