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

  return (
    <TeamDetailsSection
      title={t("panels.teamsPanels.details.finalSummary.title")}
      loading={isDetailsLoading}
      open={open}
      onToggle={onToggle}
    >
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
