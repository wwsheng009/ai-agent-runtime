import { LoaderCircleIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { type DispatchTemplateMode } from "@/components/workspace/runtime-teams/shared";
import { cn } from "@/lib/utils";

import { dispatchControlClass } from "./format";

type DispatchProvisionSectionProps = {
  dispatchTemplateMode: DispatchTemplateMode;
  isProvisioningDispatch: boolean;
  onDispatchTemplateModeChange: (mode: DispatchTemplateMode) => void;
  onProvisionStrategyDraftChange: (value: string) => void;
  onProvisionTeamCountDraftChange: (value: string) => void;
  onProvisionTeammateNamePrefixDraftChange: (value: string) => void;
  onProvisionTeammateProfileDraftChange: (value: string) => void;
  onProvisionTeamsAndDispatch: () => void | Promise<void>;
  onProvisionUserPrefixDraftChange: (value: string) => void;
  onProvisionWorkspaceDraftChange: (value: string) => void;
  provisionStrategyDraft: string;
  provisionTeamCountDraft: string;
  provisionTeammateNamePrefixDraft: string;
  provisionTeammateProfileDraft: string;
  provisionUserPrefixDraft: string;
  provisionWorkspaceDraft: string;
};

export function DispatchProvisionSection({
  dispatchTemplateMode,
  isProvisioningDispatch,
  onDispatchTemplateModeChange,
  onProvisionStrategyDraftChange,
  onProvisionTeamCountDraftChange,
  onProvisionTeammateNamePrefixDraftChange,
  onProvisionTeammateProfileDraftChange,
  onProvisionTeamsAndDispatch,
  onProvisionUserPrefixDraftChange,
  onProvisionWorkspaceDraftChange,
  provisionStrategyDraft,
  provisionTeamCountDraft,
  provisionTeammateNamePrefixDraft,
  provisionTeammateProfileDraft,
  provisionUserPrefixDraft,
  provisionWorkspaceDraft,
}: DispatchProvisionSectionProps) {
  const { t } = useTranslation("workspace");

  return (
    <>
      <div className="mt-3 rounded-card border border-white/8 bg-white/[0.03] px-3 py-2.5">
        <div className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
          {t("panels.teamsDispatch.provision.heading")}
        </div>
        <div className="mt-2.5 grid gap-2.5 sm:grid-cols-2 xl:grid-cols-3">
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              {t("panels.teamsDispatch.provision.teamCount")}
            </div>
            <input
              value={provisionTeamCountDraft}
              onChange={(event) => onProvisionTeamCountDraftChange(event.target.value)}
              placeholder="2"
              className={dispatchControlClass}
            />
          </div>
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              {t("panels.teamsDispatch.provision.workspaceId")}
            </div>
            <input
              value={provisionWorkspaceDraft}
              onChange={(event) => onProvisionWorkspaceDraftChange(event.target.value)}
              placeholder={t("panels.teamsDispatch.provision.workspaceIdPlaceholder")}
              className={dispatchControlClass}
            />
          </div>
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              {t("panels.teamsDispatch.provision.strategy")}
            </div>
            <input
              value={provisionStrategyDraft}
              onChange={(event) => onProvisionStrategyDraftChange(event.target.value)}
              placeholder={t("panels.teamsDispatch.provision.strategyPlaceholder")}
              className={dispatchControlClass}
            />
          </div>
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              {t("panels.teamsDispatch.provision.userPrefix")}
            </div>
            <input
              value={provisionUserPrefixDraft}
              onChange={(event) => onProvisionUserPrefixDraftChange(event.target.value)}
              placeholder={t("panels.teamsDispatch.provision.userPrefixPlaceholder")}
              className={dispatchControlClass}
            />
          </div>
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              {t("panels.teamsDispatch.provision.teammateNamePrefix")}
            </div>
            <input
              value={provisionTeammateNamePrefixDraft}
              onChange={(event) =>
                onProvisionTeammateNamePrefixDraftChange(event.target.value)
              }
              placeholder={t("panels.teamsDispatch.provision.teammateNamePrefixPlaceholder")}
              className={dispatchControlClass}
            />
          </div>
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              {t("panels.teamsDispatch.provision.teammateProfile")}
            </div>
            <input
              value={provisionTeammateProfileDraft}
              onChange={(event) =>
                onProvisionTeammateProfileDraftChange(event.target.value)
              }
              placeholder={t("panels.teamsDispatch.provision.teammateProfilePlaceholder")}
              className={dispatchControlClass}
            />
          </div>
        </div>
        <div className="mt-2.5 flex flex-col gap-2.5 sm:flex-row sm:items-center sm:justify-between">
          <div className="text-xs text-muted-foreground">
            {t("panels.teamsDispatch.provision.hint")}
          </div>
          <Button
            variant="secondary"
            size="sm"
            onClick={onProvisionTeamsAndDispatch}
            disabled={isProvisioningDispatch}
          >
            {isProvisioningDispatch ? (
              <LoaderCircleIcon size={14} className="animate-spin" />
            ) : null}
            {t("panels.teamsDispatch.provision.submit")}
          </Button>
        </div>
      </div>

      <div className="mt-3 rounded-card border border-white/8 bg-white/[0.03] px-3 py-2.5">
        <div className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
          {t("panels.teamsDispatch.template.heading")}
        </div>
        <div className="mt-2.5 flex flex-wrap gap-1.5">
          <button
            type="button"
            onClick={() => onDispatchTemplateModeChange("review_implement_verify")}
            className={cn(
              "rounded-control border px-2.5 py-1.5 text-base uppercase tracking-[0.12em] transition",
              dispatchTemplateMode === "review_implement_verify"
                ? "border-accent-gold/24 bg-accent-gold/10 text-accent-gold"
                : "border-white/10 bg-white/4 text-muted-foreground hover:border-white/14 hover:bg-white/7 hover:text-foreground",
            )}
          >
            {t("panels.teamsDispatch.template.reviewImplementVerify")}
          </button>
          <button
            type="button"
            onClick={() => onDispatchTemplateModeChange("mirror")}
            className={cn(
              "rounded-control border px-2.5 py-1.5 text-base uppercase tracking-[0.12em] transition",
              dispatchTemplateMode === "mirror"
                ? "border-accent-gold/24 bg-accent-gold/10 text-accent-gold"
                : "border-white/10 bg-white/4 text-muted-foreground hover:border-white/14 hover:bg-white/7 hover:text-foreground",
            )}
          >
            {t("panels.teamsDispatch.template.mirrorSameTask")}
          </button>
        </div>
        <div className="mt-2.5 text-sm leading-6 text-muted-foreground">
          {dispatchTemplateMode === "mirror"
            ? t("panels.teamsDispatch.template.mirrorDescription")
            : t("panels.teamsDispatch.template.roleVariantsDescription")}
        </div>
      </div>
    </>
  );
}
