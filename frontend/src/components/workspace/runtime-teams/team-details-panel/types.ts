// 由 components/workspace/runtime-teams/team-details-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
import {
  type ClaimCheckState,
  type TeamDetailsState,
} from "@/components/workspace/runtime-teams/shared";
import { type RuntimeTeamRecord, type RuntimeTeamSummaryEntry } from "@/lib/runtime-api";

export type TeamDetailsPanelProps = {
  ackingMessageId: string;
  claimCheckError: string | null;
  claimCheckState: ClaimCheckState;
  details: TeamDetailsState;
  detailsError: string | null;
  graphEdgeCount: number;
  graphMissingCount: number;
  isCheckingClaims: boolean;
  isDetailsLoading: boolean;
  isSendingMailbox: boolean;
  mailboxBodyDraft: string;
  mailboxError: string | null;
  mailboxFromDraft: string;
  mailboxKindDraft: string;
  mailboxTaskDraft: string;
  mailboxToDraft: string;
  onAckMailboxMessage: (messageId: string) => void;
  onCheckPathClaims: () => void;
  onMailboxBodyDraftChange: (value: string) => void;
  onMailboxFromDraftChange: (value: string) => void;
  onMailboxKindDraftChange: (value: string) => void;
  onMailboxTaskDraftChange: (value: string) => void;
  onMailboxToDraftChange: (value: string) => void;
  onReadPathDraftChange: (value: string) => void;
  onSendMailboxMessage: () => void;
  onWritePathDraftChange: (value: string) => void;
  readPathDraft: string;
  selectedSummary: RuntimeTeamSummaryEntry | undefined;
  selectedTeam: RuntimeTeamRecord;
  visibleEvents: TeamDetailsState["events"];
  visibleMailbox: TeamDetailsState["mailbox"];
  visiblePathClaims: TeamDetailsState["pathClaims"];
  visibleTasks: TeamDetailsState["tasks"];
  visibleTeammates: TeamDetailsState["teammates"];
  writePathDraft: string;
};
