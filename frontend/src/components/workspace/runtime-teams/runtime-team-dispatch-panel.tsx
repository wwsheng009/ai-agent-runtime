import { Badge } from "@/components/ui/badge";
import {
  type DispatchMonitorEntry,
  type DispatchTeamReadiness,
  type DispatchTemplateMode,
  type MultiTeamDispatchResult,
} from "@/components/workspace/runtime-teams/shared";
import {
  type RuntimeTeamRecord,
  type RuntimeTeamSummaryEntry,
} from "@/types/runtime";

import { DispatchMonitorPanel } from "./runtime-team-dispatch-panel/monitor-panel";
import { DispatchProvisionSection } from "./runtime-team-dispatch-panel/provision-section";
import { DispatchTaskComposer } from "./runtime-team-dispatch-panel/task-composer";
import { DispatchTeamList } from "./runtime-team-dispatch-panel/team-select-list";

type RuntimeTeamDispatchPanelProps = {
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
  onDispatchTaskToTeams: () => void | Promise<void>;
  onDispatchTemplateModeChange: (mode: DispatchTemplateMode) => void;
  onProvisionStrategyDraftChange: (value: string) => void;
  onProvisionTeamCountDraftChange: (value: string) => void;
  onProvisionTeammateNamePrefixDraftChange: (value: string) => void;
  onProvisionTeammateProfileDraftChange: (value: string) => void;
  onProvisionTeamsAndDispatch: () => void | Promise<void>;
  onProvisionUserPrefixDraftChange: (value: string) => void;
  onProvisionWorkspaceDraftChange: (value: string) => void;
  onRefreshDispatchMonitor: () => void | Promise<void>;
  onToggleDispatchTeam: (teamId: string) => void;
  provisionStrategyDraft: string;
  provisionTeamCountDraft: string;
  provisionTeammateNamePrefixDraft: string;
  provisionTeammateProfileDraft: string;
  provisionUserPrefixDraft: string;
  provisionWorkspaceDraft: string;
  selectedDispatchTeamIds: string[];
  summaryMap: Map<string, RuntimeTeamSummaryEntry>;
  teams: RuntimeTeamRecord[];
};

export function RuntimeTeamDispatchPanel({
  dispatchMonitor,
  dispatchMonitorCounts,
  dispatchMonitorError,
  dispatchTaskDeliverablesDraft,
  dispatchTaskError,
  dispatchTaskGoalDraft,
  dispatchTaskInputsDraft,
  dispatchTaskPriorityDraft,
  dispatchTaskResults,
  dispatchTaskTitleDraft,
  dispatchTeamReadiness,
  dispatchTemplateMode,
  isDispatchMonitorLoading,
  isDispatchReadinessLoading,
  isDispatchingTask,
  isProvisioningDispatch,
  onDispatchTaskDeliverablesDraftChange,
  onDispatchTaskGoalDraftChange,
  onDispatchTaskInputsDraftChange,
  onDispatchTaskPriorityDraftChange,
  onDispatchTaskTitleDraftChange,
  onDispatchTaskToTeams,
  onDispatchTemplateModeChange,
  onProvisionStrategyDraftChange,
  onProvisionTeamCountDraftChange,
  onProvisionTeammateNamePrefixDraftChange,
  onProvisionTeammateProfileDraftChange,
  onProvisionTeamsAndDispatch,
  onProvisionUserPrefixDraftChange,
  onProvisionWorkspaceDraftChange,
  onRefreshDispatchMonitor,
  onToggleDispatchTeam,
  provisionStrategyDraft,
  provisionTeamCountDraft,
  provisionTeammateNamePrefixDraft,
  provisionTeammateProfileDraft,
  provisionUserPrefixDraft,
  provisionWorkspaceDraft,
  selectedDispatchTeamIds,
  summaryMap,
  teams,
}: RuntimeTeamDispatchPanelProps) {
  return (
    <div className="rounded-[0.95rem] border border-white/8 bg-white/[0.035] p-3.5">
      <div className="flex items-start justify-between gap-3">
        <div>
          <div className="text-sm font-semibold text-[var(--foreground)]">
            Multi-team next task
          </div>
          <div className="mt-1 text-xs text-[var(--muted-foreground)]">
            Create one ready task across multiple active teams so their orchestrators can
            run in parallel.
          </div>
        </div>
        <Badge>{selectedDispatchTeamIds.length} selected</Badge>
      </div>

      <DispatchProvisionSection
        dispatchTemplateMode={dispatchTemplateMode}
        isProvisioningDispatch={isProvisioningDispatch}
        onDispatchTemplateModeChange={onDispatchTemplateModeChange}
        onProvisionStrategyDraftChange={onProvisionStrategyDraftChange}
        onProvisionTeamCountDraftChange={onProvisionTeamCountDraftChange}
        onProvisionTeammateNamePrefixDraftChange={onProvisionTeammateNamePrefixDraftChange}
        onProvisionTeammateProfileDraftChange={onProvisionTeammateProfileDraftChange}
        onProvisionTeamsAndDispatch={onProvisionTeamsAndDispatch}
        onProvisionUserPrefixDraftChange={onProvisionUserPrefixDraftChange}
        onProvisionWorkspaceDraftChange={onProvisionWorkspaceDraftChange}
        provisionStrategyDraft={provisionStrategyDraft}
        provisionTeamCountDraft={provisionTeamCountDraft}
        provisionTeammateNamePrefixDraft={provisionTeammateNamePrefixDraft}
        provisionTeammateProfileDraft={provisionTeammateProfileDraft}
        provisionUserPrefixDraft={provisionUserPrefixDraft}
        provisionWorkspaceDraft={provisionWorkspaceDraft}
      />

      <DispatchTeamList
        dispatchTeamReadiness={dispatchTeamReadiness}
        isDispatchReadinessLoading={isDispatchReadinessLoading}
        onToggleDispatchTeam={onToggleDispatchTeam}
        selectedDispatchTeamIds={selectedDispatchTeamIds}
        summaryMap={summaryMap}
        teams={teams}
      />

      <div className="mt-3 grid gap-2.5">
        <DispatchTaskComposer
          dispatchTaskDeliverablesDraft={dispatchTaskDeliverablesDraft}
          dispatchTaskError={dispatchTaskError}
          dispatchTaskGoalDraft={dispatchTaskGoalDraft}
          dispatchTaskInputsDraft={dispatchTaskInputsDraft}
          dispatchTaskPriorityDraft={dispatchTaskPriorityDraft}
          dispatchTaskResults={dispatchTaskResults}
          dispatchTaskTitleDraft={dispatchTaskTitleDraft}
          isDispatchingTask={isDispatchingTask}
          onDispatchTaskDeliverablesDraftChange={onDispatchTaskDeliverablesDraftChange}
          onDispatchTaskGoalDraftChange={onDispatchTaskGoalDraftChange}
          onDispatchTaskInputsDraftChange={onDispatchTaskInputsDraftChange}
          onDispatchTaskPriorityDraftChange={onDispatchTaskPriorityDraftChange}
          onDispatchTaskTitleDraftChange={onDispatchTaskTitleDraftChange}
          onDispatchTaskToTeams={onDispatchTaskToTeams}
          selectedTeamCount={selectedDispatchTeamIds.length}
        />

        {dispatchTaskResults.length > 0 ? (
          <DispatchMonitorPanel
            dispatchMonitor={dispatchMonitor}
            dispatchMonitorCounts={dispatchMonitorCounts}
            dispatchMonitorError={dispatchMonitorError}
            dispatchTaskResults={dispatchTaskResults}
            isDispatchMonitorLoading={isDispatchMonitorLoading}
            onRefreshDispatchMonitor={onRefreshDispatchMonitor}
          />
        ) : null}
      </div>
    </div>
  );
}
