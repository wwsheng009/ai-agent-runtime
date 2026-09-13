// 由 components/workspace/runtime-teams/team-details-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  isClaimActive,
  summarizeConflict,
  truncateIdentifier,
} from "@/components/workspace/runtime-teams/shared";
import { cn, formatRelativeTimestamp } from "@/lib/utils";
import { LoaderCircleIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { detailsCardClass, detailsInputClass, detailsPanelClass, detailsPillClass } from "./format";
import { type TeamDetailsPanelProps } from "./types";

type TeamDetailsPanelPathClaimsProps = Pick<
  TeamDetailsPanelProps,
  | "claimCheckError"
  | "claimCheckState"
  | "details"
  | "isCheckingClaims"
  | "onCheckPathClaims"
  | "onReadPathDraftChange"
  | "onWritePathDraftChange"
  | "readPathDraft"
  | "visiblePathClaims"
  | "writePathDraft"
>;

export function TeamDetailsPanelPathClaims({
  claimCheckError,
  claimCheckState,
  details,
  isCheckingClaims,
  onCheckPathClaims,
  onReadPathDraftChange,
  onWritePathDraftChange,
  readPathDraft,
  visiblePathClaims,
  writePathDraft,
}: TeamDetailsPanelPathClaimsProps) {
  const { t } = useTranslation("workspace");

  return (
    <div className={detailsPanelClass}>
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-2 text-xs uppercase tracking-[0.16em] text-muted-foreground">
          {t("panels.teamsPanels.details.pathClaims.title")}
        </div>
        <Badge>{details.pathClaims.length}</Badge>
      </div>
      <div className="mt-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
        {t("panels.teamsPanels.details.pathClaims.subtitle")}
      </div>
      <div className="mt-3 rounded-[0.75rem] border border-white/8 bg-white/4 px-3 py-2.5">
        <div className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
          {t("panels.teamsPanels.details.pathClaims.conflictCheck")}
        </div>
        <div className="mt-3 grid gap-3">
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              {t("panels.teamsPanels.details.pathClaims.readPaths")}
            </div>
            <textarea
              value={readPathDraft}
              onChange={(event) => onReadPathDraftChange(event.target.value)}
              placeholder={t("panels.teamsPanels.details.pathClaims.readPlaceholder")}
              className={cn(detailsInputClass, "min-h-20 resize-y leading-6")}
            />
          </div>
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              {t("panels.teamsPanels.details.pathClaims.writePaths")}
            </div>
            <textarea
              value={writePathDraft}
              onChange={(event) => onWritePathDraftChange(event.target.value)}
              placeholder={t("panels.teamsPanels.details.pathClaims.writePlaceholder")}
              className={cn(detailsInputClass, "min-h-20 resize-y leading-6")}
            />
          </div>
        </div>
        <div className="mt-3 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div className="text-xs text-muted-foreground">
            {t("panels.teamsPanels.details.pathClaims.pathsHint")}
          </div>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => onCheckPathClaims()}
            disabled={isCheckingClaims}
          >
            {isCheckingClaims ? (
              <LoaderCircleIcon size={14} className="animate-spin" />
            ) : null}
            {t("panels.teamsPanels.details.pathClaims.check")}
          </Button>
        </div>
        {claimCheckError ? (
          <div className="mt-3 rounded-card border border-accent-orange/18 bg-accent-orange/8 px-3 py-2.5 text-sm leading-6 text-muted-foreground">
            {claimCheckError}
          </div>
        ) : null}
        {claimCheckState ? (
          <div className="mt-3 rounded-card border border-white/8 bg-black/20 px-3 py-2.5">
            <div className="flex items-center justify-between gap-3">
              <div className="text-sm font-semibold text-foreground">
                {claimCheckState.ok
                  ? t("panels.teamsPanels.details.pathClaims.noConflicts")
                  : t("panels.teamsPanels.details.pathClaims.conflictsDetected")}
              </div>
              <span
                className={cn(
                  detailsPillClass,
                  claimCheckState.ok
                    ? "border-accent-teal/24 bg-accent-teal/10 text-accent-teal"
                    : "border-accent-orange/24 bg-accent-orange/10 text-accent-orange",
                )}
              >
                {t("panels.teamsPanels.details.pathClaims.conflictsCount", {
                  count: claimCheckState.conflicts.length,
                })}
              </span>
            </div>
            {!claimCheckState.ok && claimCheckState.conflicts.length > 0 ? (
              <div className="mt-3 space-y-2">
                {claimCheckState.conflicts.map((conflict, index) => (
                  <div
                    key={`${conflict.path}-${conflict.existing_path}-${index}`}
                    className="rounded-[0.75rem] border border-white/8 bg-white/4 px-3 py-2.5 text-sm leading-6 text-muted-foreground"
                  >
                    {summarizeConflict(conflict)}
                  </div>
                ))}
              </div>
            ) : (
              <div className="mt-2 text-sm text-muted-foreground">
                {t("panels.teamsPanels.details.pathClaims.canAcquire")}
              </div>
            )}
          </div>
        ) : null}
      </div>
      <div className="mt-3 space-y-2">
        {visiblePathClaims.length > 0 ? (
          visiblePathClaims.map((claim) => {
            const active = isClaimActive(claim.lease_until);
            return (
              <div key={claim.id} className={detailsCardClass}>
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <div className="break-all text-sm font-semibold text-foreground">
                      {claim.path}
                    </div>
                    <div className="mt-1 flex flex-wrap gap-3 text-xs text-muted-foreground">
                      <span>
                        {t("panels.teamsPanels.details.pathClaims.owner", {
                          id: truncateIdentifier(claim.owner_agent_id, 14),
                        })}
                      </span>
                      <span>
                        {t("panels.teamsPanels.details.pathClaims.task", {
                          id: truncateIdentifier(claim.task_id, 14),
                        })}
                      </span>
                    </div>
                  </div>
                  <div className="flex shrink-0 items-center gap-2">
                    <span className="rounded-control border border-white/10 bg-white/6 px-2 py-0.5 app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
                      {claim.mode ||
                        t("panels.teamsPanels.details.pathClaims.fallbackMode")}
                    </span>
                    <span
                      className={cn(
                        detailsPillClass,
                        active
                          ? "border-accent-teal/24 bg-accent-teal/10 text-accent-teal"
                          : "border-accent-orange/24 bg-accent-orange/10 text-accent-orange",
                      )}
                    >
                      {active
                        ? t("panels.teamsPanels.details.pathClaims.active")
                        : t("panels.teamsPanels.details.pathClaims.expired")}
                    </span>
                  </div>
                </div>
                <div className="mt-2 flex flex-wrap gap-3 text-xs text-muted-foreground">
                  {claim.lease_until ? (
                    <span>
                      {t("panels.teamsPanels.details.pathClaims.lease", {
                        time: formatRelativeTimestamp(claim.lease_until),
                      })}
                    </span>
                  ) : (
                    <span>
                      {t("panels.teamsPanels.details.pathClaims.leaseOpenEnded")}
                    </span>
                  )}
                  <span>{truncateIdentifier(claim.id, 14)}</span>
                </div>
              </div>
            );
          })
        ) : (
          <div className="text-sm text-muted-foreground">
            {t("panels.teamsPanels.details.pathClaims.empty")}
          </div>
        )}
      </div>
    </div>
  );
}
