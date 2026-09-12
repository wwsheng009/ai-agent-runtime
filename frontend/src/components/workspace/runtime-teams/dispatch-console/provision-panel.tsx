// 由 components/workspace/runtime-teams/dispatch-console.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { LoaderCircleIcon } from "lucide-react";

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
  return (
    <div className={cn("mt-3", consolePanelClass)}>
      <div className="app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
        Provision runnable teams and dispatch
      </div>
      <div className="mt-3 grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        <div>
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            Team count
          </div>
          <input
            value={provisionTeamCountDraft}
            onChange={(event) => onProvisionTeamCountDraftChange(event.target.value)}
            placeholder="2"
            className={consoleInputClass}
          />
        </div>
        <div>
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            Workspace id
          </div>
          <input
            value={provisionWorkspaceDraft}
            onChange={(event) => onProvisionWorkspaceDraftChange(event.target.value)}
            placeholder="fanout-workspace"
            className={consoleInputClass}
          />
        </div>
        <div>
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            Strategy
          </div>
          <input
            value={provisionStrategyDraft}
            onChange={(event) => onProvisionStrategyDraftChange(event.target.value)}
            placeholder="parallel-fanout"
            className={consoleInputClass}
          />
        </div>
        <div>
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            User prefix
          </div>
          <input
            value={provisionUserPrefixDraft}
            onChange={(event) => onProvisionTaskUserPrefixDraftChange(event.target.value)}
            placeholder="fanout-user"
            className={consoleInputClass}
          />
        </div>
        <div>
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            Teammate name prefix
          </div>
          <input
            value={provisionTeammateNamePrefixDraft}
            onChange={(event) =>
              onProvisionTeammateNamePrefixDraftChange(event.target.value)
            }
            placeholder="Fanout Worker"
            className={consoleInputClass}
          />
        </div>
        <div>
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            Teammate profile
          </div>
          <input
            value={provisionTeammateProfileDraft}
            onChange={(event) => onProvisionTaskProfileDraftChange(event.target.value)}
            placeholder="parallel execution worker"
            className={consoleInputClass}
          />
        </div>
      </div>
      <div className="mt-3 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="text-xs text-[var(--muted-foreground)]">
          Each provisioned team gets a lead session, a worker session, one idle teammate, and the current next task.
        </div>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => onProvisionTeamsAndDispatch()}
          disabled={isProvisioningDispatch}
        >
          {isProvisioningDispatch ? (
            <LoaderCircleIcon size={14} className="animate-spin" />
          ) : null}
          Provision runnable teams and dispatch
        </Button>
      </div>
    </div>
  );
}
