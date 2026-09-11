import type { ReactNode } from "react";

import {
  type ClaimCheckState,
  sortTasks,
  type TeamDetailsState,
} from "@/components/workspace/runtime-teams/shared";
import {
  type RuntimeTeamRecord,
  type RuntimeTeamSummaryEntry,
} from "@/types/runtime";

export type RuntimeTeamDetailsPanelProps = {
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
  visibleTasks: ReturnType<typeof sortTasks>;
  visibleTeammates: TeamDetailsState["teammates"];
  writePathDraft: string;
};

export type TeamDetailsSectionId =
  | "roster"
  | "tasks"
  | "mailbox"
  | "claims"
  | "timeline"
  | "summary";

export type TeamDetailsSectionState = Record<TeamDetailsSectionId, boolean>;

export type TeamDetailsSectionProps = {
  badge?: ReactNode;
  children: ReactNode;
  loading?: boolean;
  onToggle: () => void;
  open: boolean;
  subtitle?: string;
  title: string;
};
