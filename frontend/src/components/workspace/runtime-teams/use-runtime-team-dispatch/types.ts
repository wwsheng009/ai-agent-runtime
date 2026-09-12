// 由 components/workspace/runtime-teams/use-runtime-team-dispatch.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import type { RuntimeTeamRecord } from "@/lib/runtime-api";
import {
  type DispatchMonitorEntry,
  type DispatchTeamReadiness,
  type DispatchTemplateMode,
  type MultiTeamDispatchResult,
} from "@/components/workspace/runtime-teams/shared";

export type UseRuntimeTeamDispatchOptions = {
  onRefresh?: () => void;
  onRefreshSelectedTeamTasksAndEvents?: (teamId: string) => Promise<void>;
  selectedTeamId: string;
  selectedTeamWorkspaceId?: string;
  teams: RuntimeTeamRecord[];
};

export type DispatchTaskRequest = {
  deliverables: string[];
  goal: string;
  inputs: string[];
  priority: number;
  status: "ready";
  title: string;
};

export type DispatchTaskDraftState = {
  deliverablesDraft: string;
  goalDraft: string;
  inputsDraft: string;
  priorityDraft: string;
  titleDraft: string;
};

export type DispatchTeamIdentifier = Pick<RuntimeTeamRecord, "id">;

export type UseRuntimeTeamDispatchReturn = {
  dispatchMonitor: DispatchMonitorEntry[];
  dispatchMonitorCounts: Record<string, number>;
  dispatchMonitorError: string | null;
  dispatchTaskDeliverablesDraft: string;
  dispatchTaskError: string | null;
  dispatchTaskGoalDraft: string;
  dispatchTaskInputsDraft: string;
  dispatchTaskPriorityDraft: string;
  dispatchTaskResults: MultiTeamDispatchResult[];
  dispatchTaskTitleDraft: string;
  dispatchTeamReadiness: Record<string, DispatchTeamReadiness>;
  dispatchTemplateMode: DispatchTemplateMode;
  isDispatchMonitorLoading: boolean;
  isDispatchReadinessLoading: boolean;
  isDispatchingTask: boolean;
  isProvisioningDispatch: boolean;
  onDispatchTaskDeliverablesDraftChange: (value: string) => void;
  onDispatchTaskGoalDraftChange: (value: string) => void;
  onDispatchTaskInputsDraftChange: (value: string) => void;
  onDispatchTaskPriorityDraftChange: (value: string) => void;
  onDispatchTaskTitleDraftChange: (value: string) => void;
  onDispatchTaskToTeams: () => Promise<void>;
  onDispatchTemplateModeChange: (mode: DispatchTemplateMode) => void;
  onProvisionStrategyDraftChange: (value: string) => void;
  onProvisionTeamCountDraftChange: (value: string) => void;
  onProvisionTeammateNamePrefixDraftChange: (value: string) => void;
  onProvisionTeammateProfileDraftChange: (value: string) => void;
  onProvisionTeamsAndDispatch: () => Promise<void>;
  onProvisionUserPrefixDraftChange: (value: string) => void;
  onProvisionWorkspaceDraftChange: (value: string) => void;
  onRefreshDispatchMonitor: () => Promise<void>;
  onToggleDispatchTeam: (teamId: string) => void;
  provisionStrategyDraft: string;
  provisionTeamCountDraft: string;
  provisionTeammateNamePrefixDraft: string;
  provisionTeammateProfileDraft: string;
  provisionUserPrefixDraft: string;
  provisionWorkspaceDraft: string;
  selectedDispatchTeamIds: string[];
};
