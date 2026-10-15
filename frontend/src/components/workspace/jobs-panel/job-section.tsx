// P2-1A：后台任务面板分区（live / settled 各一个 <section>，逐行转发给 JobRow）。
// 由 components/workspace/jobs-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  type JobAction,
  type JobActionPending,
} from "@/components/workspace/jobs-panel-shared";
import type { RuntimeJob } from "@/types/runtime";

import type { JobInputState, JobOutputState } from "./helpers";
import { JobRow } from "./job-row";

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

export function JobSection({
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
