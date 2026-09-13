// 由 components/workspace/runtime-teams/team-details-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
import { LoaderCircleIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

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
  const { t } = useTranslation("workspace");

  return (
    <div className={detailsPanelClass}>
      <div className="flex items-center gap-2 text-xs uppercase tracking-[0.16em] text-muted-foreground">
        {t("panels.teamsPanels.details.finalSummary.title")}
        {isDetailsLoading ? (
          <LoaderCircleIcon size={14} className="animate-spin" />
        ) : null}
      </div>
      {details.finalSummary ? (
        <p className="mt-2.5 text-sm leading-6 text-foreground">
          {details.finalSummary}
        </p>
      ) : (
        <p className="mt-2.5 text-sm leading-6 text-muted-foreground">
          {t("panels.teamsPanels.details.finalSummary.empty")}
        </p>
      )}
    </div>
  );
}
