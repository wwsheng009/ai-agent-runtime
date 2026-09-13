import { LoaderCircleIcon } from "lucide-react";

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
  return (
    <>
      <div className="mt-3 rounded-[0.8rem] border border-white/8 bg-white/[0.03] px-3 py-2.5">
        <div className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
          Provision runnable teams and dispatch
        </div>
        <div className="mt-2.5 grid gap-2.5 sm:grid-cols-2 xl:grid-cols-3">
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              Team count
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
              Workspace id
            </div>
            <input
              value={provisionWorkspaceDraft}
              onChange={(event) => onProvisionWorkspaceDraftChange(event.target.value)}
              placeholder="fanout-workspace"
              className={dispatchControlClass}
            />
          </div>
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              Strategy
            </div>
            <input
              value={provisionStrategyDraft}
              onChange={(event) => onProvisionStrategyDraftChange(event.target.value)}
              placeholder="parallel-fanout"
              className={dispatchControlClass}
            />
          </div>
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              User prefix
            </div>
            <input
              value={provisionUserPrefixDraft}
              onChange={(event) => onProvisionUserPrefixDraftChange(event.target.value)}
              placeholder="fanout-user"
              className={dispatchControlClass}
            />
          </div>
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              Teammate name prefix
            </div>
            <input
              value={provisionTeammateNamePrefixDraft}
              onChange={(event) =>
                onProvisionTeammateNamePrefixDraftChange(event.target.value)
              }
              placeholder="Fanout Worker"
              className={dispatchControlClass}
            />
          </div>
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              Teammate profile
            </div>
            <input
              value={provisionTeammateProfileDraft}
              onChange={(event) =>
                onProvisionTeammateProfileDraftChange(event.target.value)
              }
              placeholder="parallel execution worker"
              className={dispatchControlClass}
            />
          </div>
        </div>
        <div className="mt-2.5 flex flex-col gap-2.5 sm:flex-row sm:items-center sm:justify-between">
          <div className="text-xs text-muted-foreground">
            Each provisioned team gets a lead session, a worker session, one idle
            teammate, and the current next task.
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
            Provision runnable teams and dispatch
          </Button>
        </div>
      </div>

      <div className="mt-3 rounded-[0.8rem] border border-white/8 bg-white/[0.03] px-3 py-2.5">
        <div className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
          Fan-out template
        </div>
        <div className="mt-2.5 flex flex-wrap gap-1.5">
          <button
            type="button"
            onClick={() => onDispatchTemplateModeChange("review_implement_verify")}
            className={cn(
              "rounded-[0.65rem] border px-2.5 py-1.5 text-base uppercase tracking-[0.12em] transition",
              dispatchTemplateMode === "review_implement_verify"
                ? "border-[#f0c77b]/24 bg-[#f0c77b]/10 text-[#f0c77b]"
                : "border-white/10 bg-white/4 text-muted-foreground hover:border-white/14 hover:bg-white/7 hover:text-foreground",
            )}
          >
            Review / Implement / Verify
          </button>
          <button
            type="button"
            onClick={() => onDispatchTemplateModeChange("mirror")}
            className={cn(
              "rounded-[0.65rem] border px-2.5 py-1.5 text-base uppercase tracking-[0.12em] transition",
              dispatchTemplateMode === "mirror"
                ? "border-[#f0c77b]/24 bg-[#f0c77b]/10 text-[#f0c77b]"
                : "border-white/10 bg-white/4 text-muted-foreground hover:border-white/14 hover:bg-white/7 hover:text-foreground",
            )}
          >
            Mirror Same Task
          </button>
        </div>
        <div className="mt-2.5 text-sm leading-6 text-muted-foreground">
          {dispatchTemplateMode === "mirror"
            ? "Every selected team receives the same task payload."
            : "Teams receive role-specific variants of the same next task so they execute from different angles."}
        </div>
      </div>
    </>
  );
}
