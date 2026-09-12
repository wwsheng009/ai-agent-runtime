// 由 components/workspace/runtime-teams/team-details-panel.tsx 机械拆分而来（P0-2 入口），仅搬迁不改语义。
import { TeamDetailsPanelFinalSummary } from "./team-details-panel/final-summary-section";
import { TeamDetailsPanelMailbox } from "./team-details-panel/mailbox-section";
import { TeamDetailsPanelPathClaims } from "./team-details-panel/path-claims-section";
import { TeamDetailsPanelRoster } from "./team-details-panel/roster-section";
import { TeamDetailsPanelSnapshot } from "./team-details-panel/snapshot-section";
import { TeamDetailsPanelTaskQueue } from "./team-details-panel/task-queue-section";
import { TeamDetailsPanelTimeline } from "./team-details-panel/timeline-section";
import { type TeamDetailsPanelProps } from "./team-details-panel/types";

export function TeamDetailsPanel({
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
}: TeamDetailsPanelProps) {
  return (
    <div className="rounded-[0.95rem] border border-white/8 bg-black/20 p-3.5">
      <TeamDetailsPanelSnapshot
        details={details}
        detailsError={detailsError}
        graphEdgeCount={graphEdgeCount}
        graphMissingCount={graphMissingCount}
        isDetailsLoading={isDetailsLoading}
        selectedSummary={selectedSummary}
        selectedTeam={selectedTeam}
      />

      <TeamDetailsPanelRoster visibleTeammates={visibleTeammates} />

      <TeamDetailsPanelTaskQueue visibleTasks={visibleTasks} />

      <TeamDetailsPanelMailbox
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
        visibleMailbox={visibleMailbox}
      />

      <TeamDetailsPanelPathClaims
        claimCheckError={claimCheckError}
        claimCheckState={claimCheckState}
        details={details}
        isCheckingClaims={isCheckingClaims}
        onCheckPathClaims={onCheckPathClaims}
        onReadPathDraftChange={onReadPathDraftChange}
        onWritePathDraftChange={onWritePathDraftChange}
        readPathDraft={readPathDraft}
        visiblePathClaims={visiblePathClaims}
        writePathDraft={writePathDraft}
      />

      <TeamDetailsPanelTimeline visibleEvents={visibleEvents} />

      <TeamDetailsPanelFinalSummary
        details={details}
        isDetailsLoading={isDetailsLoading}
      />
    </div>
  );
}
