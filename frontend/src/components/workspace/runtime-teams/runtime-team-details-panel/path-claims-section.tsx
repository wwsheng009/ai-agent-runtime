import { LoaderCircleIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  type ClaimCheckState,
  isClaimActive,
  summarizeConflict,
  type TeamDetailsState,
  truncateIdentifier,
} from "@/components/workspace/runtime-teams/shared";
import { cn, formatRelativeTimestamp } from "@/lib/utils";

import {
  detailCardClass,
  detailControlClass,
  detailMetaPillClass,
  detailStatusPillClass,
} from "./format";
import { TeamDetailsSection } from "./primitives";

type RuntimeTeamPathClaimsSectionProps = {
  claimCheckError: string | null;
  claimCheckState: ClaimCheckState;
  details: TeamDetailsState;
  isCheckingClaims: boolean;
  onCheckPathClaims: () => void;
  onReadPathDraftChange: (value: string) => void;
  onToggle: () => void;
  onWritePathDraftChange: (value: string) => void;
  open: boolean;
  readPathDraft: string;
  visiblePathClaims: TeamDetailsState["pathClaims"];
  writePathDraft: string;
};

export function RuntimeTeamPathClaimsSection({
  claimCheckError,
  claimCheckState,
  details,
  isCheckingClaims,
  onCheckPathClaims,
  onReadPathDraftChange,
  onToggle,
  onWritePathDraftChange,
  open,
  readPathDraft,
  visiblePathClaims,
  writePathDraft,
}: RuntimeTeamPathClaimsSectionProps) {
  return (
    <TeamDetailsSection
      title="Path claims"
      subtitle="Active filesystem leases for runtime writers and readers"
      badge={<Badge>{details.pathClaims.length}</Badge>}
      open={open}
      onToggle={onToggle}
    >
      <div className={detailCardClass}>
        <div className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
          Conflict check
        </div>
        <div className="mt-2.5 grid gap-2.5">
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              Read paths
            </div>
            <textarea
              value={readPathDraft}
              onChange={(event) => onReadPathDraftChange(event.target.value)}
              placeholder="src/components/workspace/runtime-teams.tsx"
              className={`min-h-20 ${detailControlClass} resize-y leading-6`}
            />
          </div>
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              Write paths
            </div>
            <textarea
              value={writePathDraft}
              onChange={(event) => onWritePathDraftChange(event.target.value)}
              placeholder="frontend/src/lib/runtime-api.ts"
              className={`min-h-20 ${detailControlClass} resize-y leading-6`}
            />
          </div>
        </div>
        <div className="mt-2.5 flex flex-col gap-2.5 sm:flex-row sm:items-center sm:justify-between">
          <div className="text-xs text-muted-foreground">
            Separate multiple paths with new lines or commas.
          </div>
          <Button
            variant="secondary"
            size="sm"
            onClick={onCheckPathClaims}
            disabled={isCheckingClaims}
          >
            {isCheckingClaims ? (
              <LoaderCircleIcon size={14} className="animate-spin" />
            ) : null}
            Check conflicts
          </Button>
        </div>
        {claimCheckError ? (
          <div className="mt-2.5 rounded-[0.75rem] border border-accent-orange/18 bg-accent-orange/8 px-3 py-2.5 text-sm leading-6 text-muted-foreground">
            {claimCheckError}
          </div>
        ) : null}
        {claimCheckState ? (
          <div className="mt-2.5 rounded-[0.75rem] border border-border bg-surface-solid px-3 py-2.5">
            <div className="flex items-center justify-between gap-3">
              <div className="text-[13px] font-semibold text-foreground">
                {claimCheckState.ok ? "No conflicts detected" : "Conflicts detected"}
              </div>
              <span
                className={cn(
                  detailStatusPillClass,
                  claimCheckState.ok
                    ? "border-accent-teal/24 bg-accent-teal/10 text-accent-teal"
                    : "border-accent-orange/24 bg-accent-orange/10 text-accent-orange",
                )}
              >
                {claimCheckState.conflicts.length} conflicts
              </span>
            </div>
            {!claimCheckState.ok && claimCheckState.conflicts.length > 0 ? (
              <div className="mt-2.5 space-y-1.5">
                {claimCheckState.conflicts.map((conflict, index) => (
                  <div
                    key={`${conflict.path}-${conflict.existing_path}-${index}`}
                    className={cn(
                      detailCardClass,
                      "text-sm leading-6 text-muted-foreground",
                    )}
                  >
                    {summarizeConflict(conflict)}
                  </div>
                ))}
              </div>
            ) : (
              <div className="mt-2 text-sm text-muted-foreground">
                Requested reads and writes can be acquired at the current runtime
                snapshot.
              </div>
            )}
          </div>
        ) : null}
      </div>
      <div className="mt-2.5 space-y-1.5">
        {visiblePathClaims.length > 0 ? (
          visiblePathClaims.map((claim) => {
            const active = isClaimActive(claim.lease_until);

            return (
              <div
                key={claim.id}
                className={detailCardClass}
              >
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <div className="break-all text-[13px] font-semibold text-foreground">
                      {claim.path}
                    </div>
                    <div className="mt-0.5 flex flex-wrap gap-2.5 text-xs text-muted-foreground">
                      <span>owner {truncateIdentifier(claim.owner_agent_id, 14)}</span>
                      <span>task {truncateIdentifier(claim.task_id, 14)}</span>
                    </div>
                  </div>
                  <div className="flex shrink-0 items-center gap-2">
                    <span className={detailMetaPillClass}>
                      {claim.mode || "claim"}
                    </span>
                    <span
                      className={cn(
                        detailStatusPillClass,
                        active
                          ? "border-accent-teal/24 bg-accent-teal/10 text-accent-teal"
                          : "border-accent-orange/24 bg-accent-orange/10 text-accent-orange",
                      )}
                    >
                      {active ? "active" : "expired"}
                    </span>
                  </div>
                </div>
                <div className="mt-1.5 flex flex-wrap gap-2.5 text-xs text-muted-foreground">
                  {claim.lease_until ? (
                    <span>lease {formatRelativeTimestamp(claim.lease_until)}</span>
                  ) : (
                    <span>lease open-ended</span>
                  )}
                  <span>{truncateIdentifier(claim.id, 14)}</span>
                </div>
              </div>
            );
          })
        ) : (
          <div className="text-sm text-muted-foreground">
            No active path claims available.
          </div>
        )}
      </div>
    </TeamDetailsSection>
  );
}
