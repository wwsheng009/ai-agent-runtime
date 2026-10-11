// P2-1A：后台任务（Jobs）弹层。
//
// 数据全部来自 `/api/runtime/background/jobs*`（无假数据）：live（pending/running）
// 与 settled（终态）分区展示，live 行本地走秒（elapsed tick），行内可展开读取
// 增量输出（REST `/output` 分页）；行内动作按 `jobAvailableActions` 矩阵渲染
// （pending→暂停/取消/放弃，paused→恢复/取消/放弃，running→取消，终态→重排队）。
// P2-9：数据由 shell owner 的 `useBackgroundJobs` 单例提供（弹层与顶栏状态条共用同一份，
// 保证「状态条计数 == 弹层 live 计数」），面板只渲染与转发动作。
// 交互：Esc / 遮罩点击关闭，关闭后焦点回到触发按钮（use-focus-restore）。

import {
  AlertTriangleIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  CornerDownLeftIcon,
  ListChecksIcon,
  RefreshCwIcon,
  XIcon,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";
import { JobActions } from "@/components/workspace/jobs-panel-job-actions";
import {
  formatJobDuration,
  formatJobTimestamp,
  isLiveJobStatus,
  jobStatusLabelKey,
  resolveJobElapsedMs,
  splitRuntimeJobs,
  type JobAction,
  type JobActionPending,
} from "@/components/workspace/jobs-panel-shared";
import { type BackgroundJobsController } from "@/hooks/workspace/use-background-jobs";
import { useElapsedTick } from "@/hooks/workspace/use-elapsed-tick";
import { useFocusRestore } from "@/hooks/workspace/use-focus-restore";
import { getRuntimeJobOutput } from "@/lib/runtime-api";
import { subscribeJobOutput } from "@/lib/job-output-stream/store";
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

type JobInputState = {
  value: string;
  sending: boolean;
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
  const { error, hasLiveJobs, jobs, loading, pendingAction, refresh, runAction } =
    controller;
  const [expandedJobId, setExpandedJobId] = useState("");
  const [outputs, setOutputs] = useState<Record<string, JobOutputState>>({});
  const [inputs, setInputs] = useState<Record<string, JobInputState>>({});

  // outputs 的最新值：订阅回调里读取，避免把 outputs 放进订阅依赖导致频繁重订阅。
  const outputsRef = useRef(outputs);
  useEffect(() => {
    outputsRef.current = outputs;
  }, [outputs]);

  useDialogLifecycle(open, onClose);
  useFocusRestore(open);

  const { live, settled } = useMemo(() => splitRuntimeJobs(jobs), [jobs]);
  const expandedJobLive = useMemo(() => {
    const job = jobs.find((candidate) => candidate.id === expandedJobId);
    return Boolean(job && isLiveJobStatus(job.status));
  }, [jobs, expandedJobId]);
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

  // P2：展开的 live 任务跟随 `job_output` 事件增量（事件驱动，而非纯轮询）。
  // 事件只作「有新数据」的信号；字节仍由权威 REST /output 按 offset 读取，
  // 因此不会与分页游标出现双份拼接。
  useEffect(() => {
    if (!open || !expandedJobId || !expandedJobLive) {
      return;
    }
    let timer: ReturnType<typeof setTimeout> | undefined;
    const unsubscribe = subscribeJobOutput(expandedJobId, () => {
      if (timer !== undefined) {
        return;
      }
      timer = setTimeout(() => {
        timer = undefined;
        const state = outputsRef.current[expandedJobId];
        if (!state || state.loading) {
          return;
        }
        void loadOutput(expandedJobId, state.nextOffset);
      }, 250);
    });
    return () => {
      unsubscribe();
      if (timer !== undefined) {
        clearTimeout(timer);
      }
    };
  }, [open, expandedJobId, expandedJobLive, loadOutput]);

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

  // P2：交互会话「发送输入」控件（2026-10-11）。行尾回车提交（\r 由此处统一
  // 附加，模型/用户无需关心 ConPTY 行规程）；发送后清空输入框。
  const handleSendInput = useCallback(
    async (jobId: string) => {
      const state = inputs[jobId];
      const value = (state?.value ?? "").trim();
      if (!value || state?.sending) {
        return;
      }
      setInputs((current) => ({
        ...current,
        [jobId]: { value: "", sending: true, error: null },
      }));
      const exchange = await controller.writeInput(jobId, { chars: `${value}\r` });
      setInputs((current) => ({
        ...current,
        [jobId]: {
          value: "",
          sending: false,
          error: exchange ? null : (t("panels.jobs.input.failed") as string),
        },
      }));
      if (exchange) {
        // Route the returned bytes through the same output window so the user
        // sees the reply without waiting for the next event/refresh.
        setOutputs((current) => {
          const previous = current[jobId] ?? emptyOutputState;
          return {
            ...current,
            [jobId]: {
              ...previous,
              text: previous.text + exchange.output,
              nextOffset: exchange.nextOffset,
              hasMore: true,
            },
          };
        });
      }
    },
    [controller, inputs, t],
  );

  const handleInputChange = useCallback((jobId: string, value: string) => {
    setInputs((current) => ({
      ...current,
      [jobId]: { value, sending: current[jobId]?.sending ?? false, error: null },
    }));
  }, []);

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
              onAction={runAction}
              pendingAction={pendingAction}
              now={now}
              onToggleOutput={handleToggleOutput}
              onLoadMore={handleLoadMore}
              onSendInput={handleSendInput}
              onInputChange={handleInputChange}
              inputs={inputs}
              expandedJobId={expandedJobId}
              outputs={outputs}
            />
          ) : null}

          {settled.length > 0 ? (
            <JobSection
              className={live.length > 0 ? "mt-4" : undefined}
              jobs={settled}
              label={t("panels.jobs.settled", { count: settled.length })}
              onAction={runAction}
              pendingAction={pendingAction}
              now={now}
              onToggleOutput={handleToggleOutput}
              onLoadMore={handleLoadMore}
              onSendInput={handleSendInput}
              onInputChange={handleInputChange}
              inputs={inputs}
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
  now: number;
  expandedJobId: string;
  outputs: Record<string, JobOutputState>;
  inputs: Record<string, JobInputState>;
  pendingAction: JobActionPending;
  onAction: (jobId: string, action: JobAction) => void;
  onToggleOutput: (jobId: string) => void;
  onLoadMore: (jobId: string) => void;
  onSendInput: (jobId: string) => void;
  onInputChange: (jobId: string, value: string) => void;
};

function JobSection({
  className,
  expandedJobId,
  jobs,
  label,
  now,
  pendingAction,
  onAction,
  onLoadMore,
  onToggleOutput,
  onInputChange,
  onSendInput,
  outputs,
  inputs,
}: JobSectionProps) {
  return (
    <section className={className} aria-label={label}>
      <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
        {label}
      </div>
      <ul className="mt-1.5 space-y-1.5">
        {jobs.map((job) => (
          <JobRow
            expanded={expandedJobId === job.id}
            job={job}
            key={job.id}
            now={now}
            pendingAction={pendingAction}
            onAction={onAction}
            onLoadMore={onLoadMore}
            onToggleOutput={onToggleOutput}
            onSendInput={onSendInput}
            onInputChange={onInputChange}
            output={outputs[job.id]}
            input={inputs[job.id]}
          />
        ))}
      </ul>
    </section>
  );
}

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

function JobRow({
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

function jobStatusToneClass(status: RuntimeJobStatus): string {
  switch (status) {
    case "running":
      return "text-accent-primary";
    case "pending":
    case "paused":
      return "text-analytics-warning";
    case "failed":
    case "timed_out":
    case "interrupted":
      return "text-analytics-danger";
    default:
      return "text-muted-foreground";
  }
}
