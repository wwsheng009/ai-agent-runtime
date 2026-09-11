import { MessageMarkdown } from "@/components/workspace/message-markdown";
import type { TeamDetailsState } from "@/components/workspace/runtime-teams/shared";

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
  return (
    <TeamDetailsSection
      title="Final summary"
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
        <p className="text-sm leading-6 text-[var(--muted-foreground)]">
          No final summary available yet.
        </p>
      )}
    </TeamDetailsSection>
  );
}
