// 由 components/workspace/runtime-teams/team-details-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
import { LoaderCircleIcon } from "lucide-react";

import { detailsPanelClass } from "./format";
import { type TeamDetailsPanelProps } from "./types";

type TeamDetailsPanelFinalSummaryProps = Pick<
  TeamDetailsPanelProps,
  | "details"
  | "isDetailsLoading"
>;

export function TeamDetailsPanelFinalSummary({
  details,
  isDetailsLoading,
}: TeamDetailsPanelFinalSummaryProps) {
  return (
    <div className={detailsPanelClass}>
      <div className="flex items-center gap-2 text-xs uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
        Final Summary
        {isDetailsLoading ? (
          <LoaderCircleIcon size={14} className="animate-spin" />
        ) : null}
      </div>
      {details.finalSummary ? (
        <p className="mt-2.5 text-sm leading-6 text-[var(--foreground)]">
          {details.finalSummary}
        </p>
      ) : (
        <p className="mt-2.5 text-sm leading-6 text-[var(--muted-foreground)]">
          No final summary available yet.
        </p>
      )}
    </div>
  );
}
