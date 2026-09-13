// 由 components/workspace/runtime-teams/dispatch-console.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Badge } from "@/components/ui/badge";
import { useTranslation } from "react-i18next";

import { DispatchMonitor } from "./dispatch-console/dispatch-monitor";
import { DispatchProvisionPanel } from "./dispatch-console/provision-panel";
import { DispatchResults } from "./dispatch-console/dispatch-results";
import { DispatchTaskComposer } from "./dispatch-console/task-composer";
import { DispatchTeamSelector } from "./dispatch-console/teams-selector";
import { DispatchTemplatePanel } from "./dispatch-console/template-panel";
import type { DispatchConsoleProps } from "./dispatch-console/types";

export function DispatchConsole({
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
  onProvisionTaskProfileDraftChange,
  onProvisionTaskUserPrefixDraftChange,
  onProvisionTeammateNamePrefixDraftChange,
  onProvisionTeamCountDraftChange,
  onProvisionTeamsAndDispatch,
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
}: DispatchConsoleProps) {
  const { t } = useTranslation("workspace");

  return (
    <div className="rounded-panel-lg border border-white/8 bg-black/20 p-3.5">
      <div className="flex items-start justify-between gap-3">
        <div>
          <div className="text-sm font-semibold text-foreground">
            {t("panels.teamsDispatch.dispatchConsole.title")}
          </div>
          <div className="mt-1 text-xs text-muted-foreground">
            {t("panels.teamsDispatch.dispatchConsole.description")}
          </div>
        </div>
        <Badge>
          {t("panels.teamsDispatch.dispatchConsole.selectedCount", {
            count: selectedDispatchTeamIds.length,
          })}
        </Badge>
      </div>

      <DispatchProvisionPanel
        isProvisioningDispatch={isProvisioningDispatch}
        onProvisionStrategyDraftChange={onProvisionStrategyDraftChange}
        onProvisionTaskProfileDraftChange={onProvisionTaskProfileDraftChange}
        onProvisionTaskUserPrefixDraftChange={onProvisionTaskUserPrefixDraftChange}
        onProvisionTeammateNamePrefixDraftChange={onProvisionTeammateNamePrefixDraftChange}
        onProvisionTeamCountDraftChange={onProvisionTeamCountDraftChange}
        onProvisionTeamsAndDispatch={onProvisionTeamsAndDispatch}
        onProvisionWorkspaceDraftChange={onProvisionWorkspaceDraftChange}
        provisionStrategyDraft={provisionStrategyDraft}
        provisionTeamCountDraft={provisionTeamCountDraft}
        provisionTeammateNamePrefixDraft={provisionTeammateNamePrefixDraft}
        provisionTeammateProfileDraft={provisionTeammateProfileDraft}
        provisionUserPrefixDraft={provisionUserPrefixDraft}
        provisionWorkspaceDraft={provisionWorkspaceDraft}
      />

      <DispatchTemplatePanel
        dispatchTemplateMode={dispatchTemplateMode}
        onDispatchTemplateModeChange={onDispatchTemplateModeChange}
      />

      <DispatchTeamSelector
        dispatchTeamReadiness={dispatchTeamReadiness}
        isDispatchReadinessLoading={isDispatchReadinessLoading}
        onToggleDispatchTeam={onToggleDispatchTeam}
        selectedDispatchTeamIds={selectedDispatchTeamIds}
        summaryMap={summaryMap}
        teams={teams}
      />

      <div className="mt-3 grid gap-3">
        <DispatchTaskComposer
          dispatchTaskDeliverablesDraft={dispatchTaskDeliverablesDraft}
          dispatchTaskError={dispatchTaskError}
          dispatchTaskGoalDraft={dispatchTaskGoalDraft}
          dispatchTaskInputsDraft={dispatchTaskInputsDraft}
          dispatchTaskPriorityDraft={dispatchTaskPriorityDraft}
          dispatchTaskTitleDraft={dispatchTaskTitleDraft}
          isDispatchingTask={isDispatchingTask}
          onDispatchTaskDeliverablesDraftChange={onDispatchTaskDeliverablesDraftChange}
          onDispatchTaskGoalDraftChange={onDispatchTaskGoalDraftChange}
          onDispatchTaskInputsDraftChange={onDispatchTaskInputsDraftChange}
          onDispatchTaskPriorityDraftChange={onDispatchTaskPriorityDraftChange}
          onDispatchTaskTitleDraftChange={onDispatchTaskTitleDraftChange}
          onDispatchTaskToTeams={onDispatchTaskToTeams}
          selectedDispatchTeamIds={selectedDispatchTeamIds}
        />

        <DispatchResults dispatchTaskResults={dispatchTaskResults} />

        <DispatchMonitor
          dispatchMonitor={dispatchMonitor}
          dispatchMonitorCounts={dispatchMonitorCounts}
          dispatchMonitorError={dispatchMonitorError}
          dispatchTaskResults={dispatchTaskResults}
          isDispatchMonitorLoading={isDispatchMonitorLoading}
          onRefreshDispatchMonitor={onRefreshDispatchMonitor}
        />
      </div>
    </div>
  );
}
