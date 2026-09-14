// P2-1A：workspace.panels.jobs 英文文案模块；形状由 zh-CN 同名模块经 satisfies 编译期约束。
import type { DeepStringShape } from "../../shape";
import type { zhWorkspacePanelsJobs } from "../../zh-CN/workspace/panels-jobs";

export const enWorkspacePanelsJobs = {
  ariaLabel: "Background jobs panel",
  eyebrow: "Runtime · Background jobs",
  title: "Background jobs",
  description:
    "Background jobs for this session: live jobs tick locally, settled jobs show duration and exit code, output loads on demand.",
  refresh: "Refresh background jobs",
  close: "Close background jobs panel",
  loading: "Loading background jobs…",
  errorTitle: "Failed to load background jobs",
  empty: "No background jobs for this session",
  emptyHint: "Background commands and long-running tasks show up here with progress and output.",
  live: "Live ({{count}})",
  settled: "Settled ({{count}})",
  elapsed: "Running for {{duration}}",
  duration: "Duration {{duration}}",
  exitCode: "Exit code {{code}}",
  finishedAt: "Finished at {{time}}",
  cancel: "Cancel",
  cancelling: "Cancelling…",
  output: {
    toggle: "View output of {{command}}",
    show: "View output",
    hide: "Hide output",
    loading: "Loading output…",
    empty: "No output yet",
    loadMore: "Load more",
  },
  status: {
    pending: "Pending",
    running: "Running",
    completed: "Completed",
    failed: "Failed",
    timed_out: "Timed out",
    cancelled: "Cancelled",
    orphaned: "Orphaned",
  },
} satisfies DeepStringShape<typeof zhWorkspacePanelsJobs>;
