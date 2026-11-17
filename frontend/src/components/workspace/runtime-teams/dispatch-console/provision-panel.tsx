// 由 components/workspace/runtime-teams/dispatch-console.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { LoaderCircleIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

import { consoleInputClass, consolePanelClass } from "./console-styles";
import type { DispatchConsoleProps } from "./types";

type DispatchProvisionPanelProps = Pick<
  DispatchConsoleProps,
  | "isProvisioningDispatch"
  | "onProvisionStrategyDraftChange"
  | "onProvisionTaskProfileDraftChange"
  | "onProvisionTaskUserPrefixDraftChange"
  | "onProvisionTeammateNamePrefixDraftChange"
  | "onProvisionTeamCountDraftChange"
  | "onProvisionTeamsAndDispatch"
  | "onProvisionWorkspaceDraftChange"
  | "provisionStrategyDraft"
  | "provisionTeamCountDraft"
  | "provisionTeammateNamePrefixDraft"
  | "provisionTeammateProfileDraft"
  | "provisionUserPrefixDraft"
  | "provisionWorkspaceDraft"
>;

export function DispatchProvisionPanel({
  isProvisioningDispatch,
  onProvisionStrategyDraftChange,
  onProvisionTaskProfileDraftChange,
  onProvisionTaskUserPrefixDraftChange,
  onProvisionTeammateNamePrefixDraftChange,
  onProvisionTeamCountDraftChange,
  onProvisionTeamsAndDispatch,
  onProvisionWorkspaceDraftChange,
  provisionStrategyDraft,
  provisionTeamCountDraft,
  provisionTeammateNamePrefixDraft,
  provisionTeammateProfileDraft,
  provisionUserPrefixDraft,
  provisionWorkspaceDraft,
}: DispatchProvisionPanelProps) {
  const { t } = useTranslation("workspace");

  return (
    <div className={cn("mt-3", consolePanelClass)}>
      <div className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
        {t("panels.teamsDispatch.provision.heading")}
      </div>
      <div className="mt-3 grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        <div>
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
            {t("panels.teamsDispatch.provision.teamCount")}
          </div>
          <input
            aria-label={t("panels.teamsDispatch.provision.teamCount")}
            value={provisionTeamCountDraft}
            onChange={(event) => onProvisionTeamCountDraftChange(event.target.value)}
            placeholder="2"
            className={consoleInputClass}
          />
        </div>
        <div>
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
            {t("panels.teamsDispatch.provision.workspaceId")}
          </div>
          <input
            aria-label={t("panels.teamsDispatch.provision.workspaceId")}
            value={provisionWorkspaceDraft}
            onChange={(event) => onProvisionWorkspaceDraftChange(event.target.value)}
            placeholder={t("panels.teamsDispatch.provision.workspaceIdPlaceholder")}
            className={consoleInputClass}
          />
        </div>
        <div>
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
            {t("panels.teamsDispatch.provision.strategy")}
          </div>
          <input
            aria-label={t("panels.teamsDispatch.provision.strategy")}
            value={provisionStrategyDraft}
            onChange={(event) => onProvisionStrategyDraftChange(event.target.value)}
            placeholder={t("panels.teamsDispatch.provision.strategyPlaceholder")}
            className={consoleInputClass}
          />
        </div>
        <div>
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
            {t("panels.teamsDispatch.provision.userPrefix")}
          </div>
          <input
            aria-label={t("panels.teamsDispatch.provision.userPrefix")}
            value={provisionUserPrefixDraft}
            onChange={(event) => onProvisionTaskUserPrefixDraftChange(event.target.value)}
            placeholder={t("panels.teamsDispatch.provision.userPrefixPlaceholder")}
            className={consoleInputClass}
          />
        </div>
        <div>
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
            {t("panels.teamsDispatch.provision.teammateNamePrefix")}
          </div>
          <input
            aria-label={t("panels.teamsDispatch.provision.teammateNamePrefix")}
            value={provisionTeammateNamePrefixDraft}
            onChange={(event) =>
              onProvisionTeammateNamePrefixDraftChange(event.target.value)
            }
            placeholder={t("panels.teamsDispatch.provision.teammateNamePrefixPlaceholder")}
            className={consoleInputClass}
          />
        </div>
        <div>
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
            {t("panels.teamsDispatch.provision.teammateProfile")}
          </div>
          <input
            aria-label={t("panels.teamsDispatch.provision.teammateProfile")}
            value={provisionTeammateProfileDraft}
            onChange={(event) => onProvisionTaskProfileDraftChange(event.target.value)}
            placeholder={t("panels.teamsDispatch.provision.teammateProfilePlaceholder")}
            className={consoleInputClass}
          />
        </div>
      </div>
      <div className="mt-3 flex justify-end">
        <Button
          variant="secondary"
          size="sm"
          onClick={() => onProvisionTeamsAndDispatch()}
          disabled={isProvisioningDispatch}
        >
          {isProvisioningDispatch ? (
            <LoaderCircleIcon size={14} className="animate-spin" />
          ) : null}
          {t("panels.teamsDispatch.provision.submit")}
        </Button>
      </div>
    </div>
  );
}
