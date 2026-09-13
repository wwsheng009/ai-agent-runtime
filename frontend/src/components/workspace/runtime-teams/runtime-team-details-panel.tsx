import { useState } from "react";

import { RuntimeTeamFinalSummarySection } from "./runtime-team-details-panel/final-summary-section";
import { createInitialOpenSections } from "./runtime-team-details-panel/format";
import { RuntimeTeamMailboxSection } from "./runtime-team-details-panel/mailbox-section";
import { RuntimeTeamPathClaimsSection } from "./runtime-team-details-panel/path-claims-section";
import { RuntimeTeamRosterSection } from "./runtime-team-details-panel/roster-section";
import { RuntimeTeamTaskQueueSection } from "./runtime-team-details-panel/task-queue-section";
import { RuntimeTeamSnapshot } from "./runtime-team-details-panel/team-snapshot";
import { RuntimeTeamTimelineSection } from "./runtime-team-details-panel/timeline-section";
import type {
  RuntimeTeamDetailsPanelProps,
  TeamDetailsSectionId,
  TeamDetailsSectionState,
} from "./runtime-team-details-panel/types";

function RuntimeTeamDetailsPanelBody({
  ackingMessageId,
  claimCheckError,
  claimCheckState,
  details,
  detailsError,
  graphEdgeCount,
  graphMissingCount,
  isCheckingClaims,
  isDetailsLoading,
  isSendingMailbox,
  mailboxBodyDraft,
  mailboxError,
  mailboxFromDraft,
  mailboxKindDraft,
  mailboxTaskDraft,
  mailboxToDraft,
  onAckMailboxMessage,
  onCheckPathClaims,
  onMailboxBodyDraftChange,
  onMailboxFromDraftChange,
  onMailboxKindDraftChange,
  onMailboxTaskDraftChange,
  onMailboxToDraftChange,
  onReadPathDraftChange,
  onSendMailboxMessage,
  onWritePathDraftChange,
  readPathDraft,
  selectedSummary,
  selectedTeam,
  visibleEvents,
  visibleMailbox,
  visiblePathClaims,
  visibleTasks,
  visibleTeammates,
  writePathDraft,
}: RuntimeTeamDetailsPanelProps) {
  const [openSections, setOpenSections] = useState<TeamDetailsSectionState>(
    createInitialOpenSections,
  );

  function toggleSection(section: TeamDetailsSectionId) {
    setOpenSections((current) => ({
      ...current,
      [section]: !current[section],
    }));
  }

  return (
    <div className="rounded-[0.9rem] border border-border bg-surface-solid p-3">
      <RuntimeTeamSnapshot
        details={details}
        detailsError={detailsError}
        graphEdgeCount={graphEdgeCount}
        graphMissingCount={graphMissingCount}
        isDetailsLoading={isDetailsLoading}
        selectedSummary={selectedSummary}
        selectedTeam={selectedTeam}
      />

      <RuntimeTeamRosterSection
        details={details}
        onToggle={() => toggleSection("roster")}
        open={openSections.roster}
        visibleTeammates={visibleTeammates}
      />

      <RuntimeTeamTaskQueueSection
        details={details}
        onToggle={() => toggleSection("tasks")}
        open={openSections.tasks}
        visibleTasks={visibleTasks}
      />

      <RuntimeTeamMailboxSection
        ackingMessageId={ackingMessageId}
        details={details}
        isSendingMailbox={isSendingMailbox}
        mailboxBodyDraft={mailboxBodyDraft}
        mailboxError={mailboxError}
        mailboxFromDraft={mailboxFromDraft}
        mailboxKindDraft={mailboxKindDraft}
        mailboxTaskDraft={mailboxTaskDraft}
        mailboxToDraft={mailboxToDraft}
        onAckMailboxMessage={onAckMailboxMessage}
        onMailboxBodyDraftChange={onMailboxBodyDraftChange}
        onMailboxFromDraftChange={onMailboxFromDraftChange}
        onMailboxKindDraftChange={onMailboxKindDraftChange}
        onMailboxTaskDraftChange={onMailboxTaskDraftChange}
        onMailboxToDraftChange={onMailboxToDraftChange}
        onSendMailboxMessage={onSendMailboxMessage}
        onToggle={() => toggleSection("mailbox")}
        open={openSections.mailbox}
        visibleMailbox={visibleMailbox}
      />

      <RuntimeTeamPathClaimsSection
        claimCheckError={claimCheckError}
        claimCheckState={claimCheckState}
        details={details}
        isCheckingClaims={isCheckingClaims}
        onCheckPathClaims={onCheckPathClaims}
        onReadPathDraftChange={onReadPathDraftChange}
        onToggle={() => toggleSection("claims")}
        onWritePathDraftChange={onWritePathDraftChange}
        open={openSections.claims}
        readPathDraft={readPathDraft}
        visiblePathClaims={visiblePathClaims}
        writePathDraft={writePathDraft}
      />

      <RuntimeTeamTimelineSection
        details={details}
        onToggle={() => toggleSection("timeline")}
        open={openSections.timeline}
        visibleEvents={visibleEvents}
      />

      <RuntimeTeamFinalSummarySection
        details={details}
        isDetailsLoading={isDetailsLoading}
        onToggle={() => toggleSection("summary")}
        open={openSections.summary}
      />
    </div>
  );
}

export function RuntimeTeamDetailsPanel(props: RuntimeTeamDetailsPanelProps) {
  return <RuntimeTeamDetailsPanelBody key={props.selectedTeam.id} {...props} />;
}
