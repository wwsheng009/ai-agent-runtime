// 由 components/workspace/runtime-teams.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense } from "react";
import { useTranslation } from "react-i18next";

import { type RuntimeTeamRecord, type RuntimeTeamSummaryEntry } from "@/lib/runtime-api";
import { type UseRuntimeTeamDispatchReturn } from "@/components/workspace/runtime-teams/use-runtime-team-dispatch";

import { RuntimeTeamDispatchPanel, RuntimeTeamsPanelFallback } from "./lazy-surfaces";

type TeamsDispatchViewProps = Pick<
  UseRuntimeTeamDispatchReturn,
  | "dispatchMonitor"
  | "dispatchMonitorCounts"
  | "dispatchMonitorError"
  | "dispatchTaskDeliverablesDraft"
  | "dispatchTaskError"
  | "dispatchTaskGoalDraft"
  | "dispatchTaskInputsDraft"
  | "dispatchTaskPriorityDraft"
  | "dispatchTaskResults"
  | "dispatchTaskTitleDraft"
  | "dispatchTeamReadiness"
  | "dispatchTemplateMode"
  | "isDispatchMonitorLoading"
  | "isDispatchReadinessLoading"
  | "isDispatchingTask"
  | "isProvisioningDispatch"
  | "provisionStrategyDraft"
  | "provisionTeamCountDraft"
  | "provisionTeammateNamePrefixDraft"
  | "provisionTeammateProfileDraft"
  | "provisionUserPrefixDraft"
  | "provisionWorkspaceDraft"
  | "selectedDispatchTeamIds"
> & {
  handleDispatchTaskToTeams: UseRuntimeTeamDispatchReturn["onDispatchTaskToTeams"];
  handleProvisionTeamsAndDispatch: UseRuntimeTeamDispatchReturn["onProvisionTeamsAndDispatch"];
  handleRefreshDispatchMonitor: UseRuntimeTeamDispatchReturn["onRefreshDispatchMonitor"];
  setDispatchTaskDeliverablesDraft: UseRuntimeTeamDispatchReturn["onDispatchTaskDeliverablesDraftChange"];
  setDispatchTaskGoalDraft: UseRuntimeTeamDispatchReturn["onDispatchTaskGoalDraftChange"];
  setDispatchTaskInputsDraft: UseRuntimeTeamDispatchReturn["onDispatchTaskInputsDraftChange"];
  setDispatchTaskPriorityDraft: UseRuntimeTeamDispatchReturn["onDispatchTaskPriorityDraftChange"];
  setDispatchTaskTitleDraft: UseRuntimeTeamDispatchReturn["onDispatchTaskTitleDraftChange"];
  setDispatchTemplateMode: UseRuntimeTeamDispatchReturn["onDispatchTemplateModeChange"];
  setProvisionStrategyDraft: UseRuntimeTeamDispatchReturn["onProvisionStrategyDraftChange"];
  setProvisionTeamCountDraft: UseRuntimeTeamDispatchReturn["onProvisionTeamCountDraftChange"];
  setProvisionTeammateNamePrefixDraft: UseRuntimeTeamDispatchReturn["onProvisionTeammateNamePrefixDraftChange"];
  setProvisionTeammateProfileDraft: UseRuntimeTeamDispatchReturn["onProvisionTeammateProfileDraftChange"];
  setProvisionUserPrefixDraft: UseRuntimeTeamDispatchReturn["onProvisionUserPrefixDraftChange"];
  setProvisionWorkspaceDraft: UseRuntimeTeamDispatchReturn["onProvisionWorkspaceDraftChange"];
  toggleDispatchTeam: UseRuntimeTeamDispatchReturn["onToggleDispatchTeam"];
  summaryMap: Map<string, RuntimeTeamSummaryEntry>;
  teams: RuntimeTeamRecord[];
};

export function TeamsDispatchView({
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
  handleDispatchTaskToTeams,
  handleProvisionTeamsAndDispatch,
  handleRefreshDispatchMonitor,
  isDispatchMonitorLoading,
  isDispatchReadinessLoading,
  isDispatchingTask,
  isProvisioningDispatch,
  provisionStrategyDraft,
  provisionTeamCountDraft,
  provisionTeammateNamePrefixDraft,
  provisionTeammateProfileDraft,
  provisionUserPrefixDraft,
  provisionWorkspaceDraft,
  selectedDispatchTeamIds,
  setDispatchTaskDeliverablesDraft,
  setDispatchTaskGoalDraft,
  setDispatchTaskInputsDraft,
  setDispatchTaskPriorityDraft,
  setDispatchTaskTitleDraft,
  setDispatchTemplateMode,
  setProvisionStrategyDraft,
  setProvisionTeamCountDraft,
  setProvisionTeammateNamePrefixDraft,
  setProvisionTeammateProfileDraft,
  setProvisionUserPrefixDraft,
  setProvisionWorkspaceDraft,
  summaryMap,
  teams,
  toggleDispatchTeam,
}: TeamsDispatchViewProps) {
  const { t } = useTranslation("workspace");

  return (
    <Suspense
      fallback={
        <RuntimeTeamsPanelFallback
          label={t("panels.teamsDispatch.dispatchView.fallbackLabel")}
        />
      }
    >
      <RuntimeTeamDispatchPanel
        dispatchMonitor={dispatchMonitor}
        dispatchMonitorCounts={dispatchMonitorCounts}
        dispatchMonitorError={dispatchMonitorError}
        dispatchTaskDeliverablesDraft={dispatchTaskDeliverablesDraft}
        dispatchTaskError={dispatchTaskError}
        dispatchTaskGoalDraft={dispatchTaskGoalDraft}
        dispatchTaskInputsDraft={dispatchTaskInputsDraft}
        dispatchTaskPriorityDraft={dispatchTaskPriorityDraft}
        dispatchTaskResults={dispatchTaskResults}
        dispatchTaskTitleDraft={dispatchTaskTitleDraft}
        dispatchTeamReadiness={dispatchTeamReadiness}
        dispatchTemplateMode={dispatchTemplateMode}
        isDispatchMonitorLoading={isDispatchMonitorLoading}
        isDispatchReadinessLoading={isDispatchReadinessLoading}
        isDispatchingTask={isDispatchingTask}
        isProvisioningDispatch={isProvisioningDispatch}
        onDispatchTaskDeliverablesDraftChange={setDispatchTaskDeliverablesDraft}
        onDispatchTaskGoalDraftChange={setDispatchTaskGoalDraft}
        onDispatchTaskInputsDraftChange={setDispatchTaskInputsDraft}
        onDispatchTaskPriorityDraftChange={setDispatchTaskPriorityDraft}
        onDispatchTaskTitleDraftChange={setDispatchTaskTitleDraft}
        onDispatchTaskToTeams={() => void handleDispatchTaskToTeams()}
        onDispatchTemplateModeChange={setDispatchTemplateMode}
        onProvisionStrategyDraftChange={setProvisionStrategyDraft}
        onProvisionTeamCountDraftChange={setProvisionTeamCountDraft}
        onProvisionTeammateNamePrefixDraftChange={setProvisionTeammateNamePrefixDraft}
        onProvisionTeammateProfileDraftChange={setProvisionTeammateProfileDraft}
        onProvisionTeamsAndDispatch={() => void handleProvisionTeamsAndDispatch()}
        onProvisionUserPrefixDraftChange={setProvisionUserPrefixDraft}
        onProvisionWorkspaceDraftChange={setProvisionWorkspaceDraft}
        onRefreshDispatchMonitor={() => void handleRefreshDispatchMonitor()}
        onToggleDispatchTeam={toggleDispatchTeam}
        provisionStrategyDraft={provisionStrategyDraft}
        provisionTeamCountDraft={provisionTeamCountDraft}
        provisionTeammateNamePrefixDraft={provisionTeammateNamePrefixDraft}
        provisionTeammateProfileDraft={provisionTeammateProfileDraft}
        provisionUserPrefixDraft={provisionUserPrefixDraft}
        provisionWorkspaceDraft={provisionWorkspaceDraft}
        selectedDispatchTeamIds={selectedDispatchTeamIds}
        summaryMap={summaryMap}
        teams={teams}
      />
    </Suspense>
  );
}
