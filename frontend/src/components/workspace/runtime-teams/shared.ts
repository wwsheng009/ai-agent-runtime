// 由 components/workspace/runtime-teams/shared.ts 机械拆分而来（P0-2），仅搬迁不改语义。
// 对外导出面保持不变（40 个符号），消费方 import 路径零改动；实现见 ./shared/ 各域模块。

export {
  createEmptyDetails,
  getSummaryCount,
  isClaimActive,
  normalizeTaskTitle,
  parsePathLines,
  uniqueStrings,
} from "@/components/workspace/runtime-teams/shared/details";

export {
  describeEventPayload,
  describeMailboxRoute,
  prettyEventType,
  statusTone,
  summarizeConflict,
  truncateIdentifier,
} from "@/components/workspace/runtime-teams/shared/format";

export {
  sortDispatchMonitor,
  sortEvents,
  sortMailbox,
  sortPathClaims,
  sortTasks,
  sortTeammates,
} from "@/components/workspace/runtime-teams/shared/sorting";

export {
  buildDispatchComparisonRows,
  countDispatchMonitorStatuses,
  formatDispatchOutcomeLabel,
  getDispatchOutcomeNarrative,
  hasCreatedDispatchResults,
  isTerminalDispatchStatus,
  normalizeDispatchOutcomeKey,
  resolveDispatchRolePlan,
  shouldExpectDispatchOutcomeSummary,
  shouldPollDispatchMonitor,
  sortDispatchComparisonRows,
  summarizeDispatchBatch,
} from "@/components/workspace/runtime-teams/shared/dispatch";

export type {
  ClaimCheckState,
  DispatchBatchSummary,
  DispatchComparisonRow,
  DispatchMonitorEntry,
  DispatchOutcomeNarrative,
  DispatchRolePlan,
  DispatchTeamReadiness,
  DispatchTemplateMode,
  MultiTeamDispatchResult,
  TeamDetailsState,
} from "@/components/workspace/runtime-teams/shared/types";
