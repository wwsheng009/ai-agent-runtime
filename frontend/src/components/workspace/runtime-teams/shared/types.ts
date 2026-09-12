// 由 components/workspace/runtime-teams/shared.ts 机械拆分而来（P0-2），仅搬迁不改语义。
// 类型定义（团队详情状态 / 派发监控与对比行 / 角色计划）

import {
  type RuntimePathClaimConflict,
  type RuntimePathClaimRecord,
  type RuntimeTaskGraphResponse,
  type RuntimeTeamEventRecord,
  type RuntimeTeamMailboxMessage,
  type RuntimeTeamTask,
  type RuntimeTeammateRecord,
} from "@/lib/runtime-api";

export type ClaimCheckState = {
  conflicts: RuntimePathClaimConflict[];
  ok: boolean;
} | null;

export type DispatchBatchSummary = {
  activeCount: number;
  attemptedCount: number;
  completedCount: number;
  createdCount: number;
  failedCount: number;
  finalSummaryCount: number;
  latestUpdatedAt?: string;
  monitorCoverageCount: number;
  pendingCount: number;
  summaryEligibleTerminalCount: number;
  statusCounts: Record<string, number>;
  terminalCount: number;
  terminalWithoutSummaryCount: number;
  terminalRows: DispatchComparisonRow[];
};

export type DispatchComparisonRow = {
  assignee?: string | null;
  created: boolean;
  detailLabel: string;
  detailText: string;
  error?: string;
  hasMonitor: boolean;
  isTerminal: boolean;
  lastEventType?: string;
  mailboxPreview: string[];
  outcomeKey: string;
  outcomeLabel: string;
  summaryLabel?: string;
  status: string;
  summary?: string;
  taskId?: string;
  teamId: string;
  updatedAt?: string;
};

export type DispatchMonitorEntry = {
  assignee?: string | null;
  error?: string;
  lastEventType?: string;
  mailboxPreview: string[];
  status: string;
  summary?: string;
  taskId: string;
  teamId: string;
  updatedAt?: string;
};

export type DispatchOutcomeNarrative = {
  label: string;
  text: string;
};

export type DispatchRolePlan = {
  deliverables: string[];
  goalInstruction?: string;
  inputHints: string[];
  key: string;
  label: string;
  strategySuffix?: string;
  teammateProfileSuffix?: string;
};

export type DispatchTeamReadiness = {
  executable: boolean;
  reason: string;
  runnableTeammates: number;
  totalTeammates: number;
};

export type DispatchTemplateMode = "mirror" | "review_implement_verify";

export type MultiTeamDispatchResult = {
  error?: string;
  status: "created" | "failed";
  taskId?: string;
  teamId: string;
};

export type TeamDetailsState = {
  events: RuntimeTeamEventRecord[];
  finalSummary: string;
  graph: RuntimeTaskGraphResponse | null;
  mailbox: RuntimeTeamMailboxMessage[];
  pathClaims: RuntimePathClaimRecord[];
  tasks: RuntimeTeamTask[];
  teammates: RuntimeTeammateRecord[];
};
