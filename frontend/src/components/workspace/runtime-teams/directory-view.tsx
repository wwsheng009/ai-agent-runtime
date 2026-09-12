// 由 components/workspace/runtime-teams.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense } from "react";
import { type Dispatch, type SetStateAction } from "react";

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import { type RuntimeTeamRecord, type RuntimeTeamSummaryEntry } from "@/lib/runtime-api";

import {
  truncateIdentifier,
  type ClaimCheckState,
  type TeamDetailsState,
} from "@/components/workspace/runtime-teams/shared";

import { RuntimeTeamDetailsPanel, RuntimeTeamsPanelFallback } from "./lazy-surfaces";

type TeamsDirectoryViewProps = {
  ackingMessageId: string;
  claimCheckError: string | null;
  claimCheckState: ClaimCheckState;
  details: TeamDetailsState;
  detailsError: string | null;
  graphEdgeCount: number;
  graphMissingCount: number;
  handleAckMailboxMessage: (messageId: string) => Promise<void>;
  handleCheckPathClaims: () => Promise<void>;
  handleSendMailboxMessage: () => Promise<void>;
  isCheckingClaims: boolean;
  isDetailsLoading: boolean;
  isSendingMailbox: boolean;
  mailboxBodyDraft: string;
  mailboxError: string | null;
  mailboxFromDraft: string;
  mailboxKindDraft: string;
  mailboxTaskDraft: string;
  mailboxToDraft: string;
  readPathDraft: string;
  selectedSummary: RuntimeTeamSummaryEntry | undefined;
  selectedTeam: RuntimeTeamRecord | null;
  selectedTeamId: string;
  setMailboxBodyDraft: Dispatch<SetStateAction<string>>;
  setMailboxFromDraft: Dispatch<SetStateAction<string>>;
  setMailboxKindDraft: Dispatch<SetStateAction<string>>;
  setMailboxTaskDraft: Dispatch<SetStateAction<string>>;
  setMailboxToDraft: Dispatch<SetStateAction<string>>;
  setReadPathDraft: Dispatch<SetStateAction<string>>;
  setSelectedTeamId: Dispatch<SetStateAction<string>>;
  setWritePathDraft: Dispatch<SetStateAction<string>>;
  summaryMap: Map<string, RuntimeTeamSummaryEntry>;
  teams: RuntimeTeamRecord[];
  visibleEvents: TeamDetailsState["events"];
  visibleMailbox: TeamDetailsState["mailbox"];
  visiblePathClaims: TeamDetailsState["pathClaims"];
  visibleTasks: TeamDetailsState["tasks"];
  visibleTeammates: TeamDetailsState["teammates"];
  writePathDraft: string;
};

export function TeamsDirectoryView({
  ackingMessageId,
  claimCheckError,
  claimCheckState,
  details,
  detailsError,
  graphEdgeCount,
  graphMissingCount,
  handleAckMailboxMessage,
  handleCheckPathClaims,
  handleSendMailboxMessage,
  isCheckingClaims,
  isDetailsLoading,
  isSendingMailbox,
  mailboxBodyDraft,
  mailboxError,
  mailboxFromDraft,
  mailboxKindDraft,
  mailboxTaskDraft,
  mailboxToDraft,
  readPathDraft,
  selectedSummary,
  selectedTeam,
  selectedTeamId,
  setMailboxBodyDraft,
  setMailboxFromDraft,
  setMailboxKindDraft,
  setMailboxTaskDraft,
  setMailboxToDraft,
  setReadPathDraft,
  setSelectedTeamId,
  setWritePathDraft,
  summaryMap,
  teams,
  visibleEvents,
  visibleMailbox,
  visiblePathClaims,
  visibleTasks,
  visibleTeammates,
  writePathDraft,
}: TeamsDirectoryViewProps) {
  return (
    <div className="grid gap-3 xl:grid-cols-[18rem_minmax(0,1fr)]">
      <aside className="rounded-[0.95rem] border border-[var(--border)] bg-[var(--surface-softer)] p-2.5">
        <div className="mb-2.5 flex items-center justify-between gap-3 px-0.5">
          <div>
            <div className="text-[10px] uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
              Team directory
            </div>
            <div className="mt-0.5 text-sm font-semibold text-[var(--foreground)]">
              Select a team
            </div>
          </div>
          <Badge>{teams.length}</Badge>
        </div>

        {teams.length > 0 ? (
          <div className="space-y-1.5 xl:max-h-[calc(100vh-18rem)] xl:overflow-y-auto xl:pr-1">
            {teams.map((team) => {
              const summary = summaryMap.get(team.id);
              const isActive = team.id === selectedTeamId;
              return (
                <button
                  key={team.id}
                  type="button"
                  onClick={() => setSelectedTeamId(team.id)}
                  className={cn(
                    "w-full rounded-[0.8rem] border px-3 py-2.5 text-left transition",
                    isActive
                      ? "border-[#8fd0c6]/30 bg-[#8fd0c6]/10 shadow-[0_0_0_1px_rgba(143,208,198,0.12)]"
                      : "border-[var(--border)] bg-[var(--surface-soft)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft-hover)]",
                  )}
                >
                  <div className="flex items-center justify-between gap-3">
                    <div className="truncate text-base font-semibold text-[var(--foreground)]">
                      {truncateIdentifier(team.id, 16)}
                    </div>
                    <span className="shrink-0 app-text-11 uppercase tracking-[0.15em] text-[var(--muted-foreground)]">
                      {team.status || "unknown"}
                    </span>
                  </div>
                  <div className="mt-1.5 flex flex-wrap gap-1.5 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                    <span>{summary?.tasks.total ?? 0} tasks</span>
                    <span>{summary?.teammates.total ?? 0} teammates</span>
                    {team.strategy ? <span>{team.strategy}</span> : null}
                  </div>
                </button>
              );
            })}
          </div>
        ) : (
          <div className="rounded-[0.8rem] border border-dashed border-[var(--border)] px-3 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
            No teams available. Switch to `Dispatch` to provision runnable teams.
          </div>
        )}
      </aside>

      <div className="min-w-0">
        {selectedTeam ? (
          <Suspense fallback={<RuntimeTeamsPanelFallback label="team details" />}>
            <RuntimeTeamDetailsPanel
              ackingMessageId={ackingMessageId}
              claimCheckError={claimCheckError}
              claimCheckState={claimCheckState}
              details={details}
              detailsError={detailsError}
              graphEdgeCount={graphEdgeCount}
              graphMissingCount={graphMissingCount}
              isCheckingClaims={isCheckingClaims}
              isDetailsLoading={isDetailsLoading}
              isSendingMailbox={isSendingMailbox}
              mailboxBodyDraft={mailboxBodyDraft}
              mailboxError={mailboxError}
              mailboxFromDraft={mailboxFromDraft}
              mailboxKindDraft={mailboxKindDraft}
              mailboxTaskDraft={mailboxTaskDraft}
              mailboxToDraft={mailboxToDraft}
              onAckMailboxMessage={(messageId) =>
                void handleAckMailboxMessage(messageId)
              }
              onCheckPathClaims={() => void handleCheckPathClaims()}
              onMailboxBodyDraftChange={setMailboxBodyDraft}
              onMailboxFromDraftChange={setMailboxFromDraft}
              onMailboxKindDraftChange={setMailboxKindDraft}
              onMailboxTaskDraftChange={setMailboxTaskDraft}
              onMailboxToDraftChange={setMailboxToDraft}
              onReadPathDraftChange={setReadPathDraft}
              onSendMailboxMessage={() => void handleSendMailboxMessage()}
              onWritePathDraftChange={setWritePathDraft}
              readPathDraft={readPathDraft}
              selectedSummary={selectedSummary}
              selectedTeam={selectedTeam}
              visibleEvents={visibleEvents}
              visibleMailbox={visibleMailbox}
              visiblePathClaims={visiblePathClaims}
              visibleTasks={visibleTasks}
              visibleTeammates={visibleTeammates}
              writePathDraft={writePathDraft}
            />
          </Suspense>
        ) : (
          <div className="rounded-[0.95rem] border border-dashed border-[var(--border)] bg-[var(--surface-softer)] px-5 py-8 text-center">
            <div className="text-sm font-semibold text-[var(--foreground)]">
              No team selected
            </div>
            <p className="mt-2 text-sm leading-6 text-[var(--muted-foreground)]">
              Pick a team from the directory to inspect its snapshot, mailbox,
              path claims, timeline, and final summary.
            </p>
          </div>
        )}
      </div>
    </div>
  );
}
