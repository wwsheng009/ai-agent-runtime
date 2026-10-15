// P2-1A：后台任务面板的行内状态类型与展示辅助（纯类型/常量/纯函数，不依赖 React）。
// 由 components/workspace/jobs-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义；
// 供 ./panel.tsx、./job-section.tsx、./job-row.tsx 共享，part 之间不经过 barrel。

import type { RuntimeJobStatus } from "@/types/runtime";

export type JobOutputState = {
  text: string;
  nextOffset: number;
  hasMore: boolean;
  loading: boolean;
  error: string | null;
};

export type JobInputState = {
  value: string;
  sending: boolean;
  error: string | null;
};

export const emptyOutputState: JobOutputState = {
  text: "",
  nextOffset: 0,
  hasMore: false,
  loading: false,
  error: null,
};

export function jobStatusToneClass(status: RuntimeJobStatus): string {
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
