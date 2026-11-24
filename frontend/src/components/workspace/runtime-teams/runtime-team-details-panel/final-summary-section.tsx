import { MessageMarkdown } from "@/components/workspace/message-markdown";
import type { TeamDetailsState } from "@/components/workspace/runtime-teams/shared";
import { useTranslation } from "react-i18next";

import { TeamDetailsSection } from "./primitives";

type RuntimeTeamFinalSummarySectionProps = {
  details: TeamDetailsState;
  isDetailsLoading: boolean;
  onToggle: () => void;
  open: boolean;
};

export function RuntimeTeamFinalSummarySection({
  details,
  isDetailsLoading,
  onToggle,
  open,
}: RuntimeTeamFinalSummarySectionProps) {
  const { t } = useTranslation("workspace");
  const summaryState = {
    pending: t("panels.teamsPanels.details.finalSummary.states.pending"),
    ready: t("panels.teamsPanels.details.finalSummary.states.ready"),
    failed: t("panels.teamsPanels.details.finalSummary.states.failed"),
    not_applicable: t(
      "panels.teamsPanels.details.finalSummary.states.notApplicable",
    ),
    unknown: t("panels.teamsPanels.details.finalSummary.states.unknown"),
  }[details.summaryState];

  return (
    <TeamDetailsSection
      title={t("panels.teamsPanels.details.finalSummary.title")}
      loading={isDetailsLoading}
      open={open}
      onToggle={onToggle}
    >
      <p
        className="mb-2 text-xs text-muted-foreground"
        role="status"
        aria-live="polite"
      >
        {t("panels.teamsPanels.details.finalSummary.state", {
          state: summaryState,
        })}
      </p>
      {details.finalSummary ? (
        <MessageMarkdown
          className="app-text-13"
          content={details.finalSummary}
        />
      ) : (
        <p className="text-sm leading-6 text-muted-foreground">
          {t("panels.teamsPanels.details.finalSummary.empty")}
        </p>
      )}
    </TeamDetailsSection>
  );
}
