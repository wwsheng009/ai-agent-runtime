import {
  ActivityIcon,
  GitBranchPlusIcon,
  LoaderCircleIcon,
  UsersRoundIcon,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  type RuntimeTeamRecord,
  type RuntimeTeamSummaryEntry,
} from "@/lib/runtime-api";
import { cn, formatRelativeTimestamp } from "@/lib/utils";

import {
  type ClaimCheckState,
  describeEventPayload,
  describeMailboxRoute,
  getSummaryCount,
  isClaimActive,
  statusTone,
  summarizeConflict,
  type TeamDetailsState,
  prettyEventType,
  truncateIdentifier,
} from "./shared";

const detailsPanelClass =
  "mt-3 rounded-[0.8rem] border border-white/8 bg-white/3 px-3 py-2.5";

const detailsCardClass =
  "rounded-[0.75rem] border border-white/8 bg-white/4 px-3 py-2.5";

const detailsInputClass =
  "w-full rounded-[0.75rem] border border-white/8 bg-black/20 px-3 py-2 text-sm text-[var(--foreground)] outline-none placeholder:text-white/28";

const detailsPillClass =
  "rounded-[0.65rem] border px-2 py-0.5 app-text-10 uppercase tracking-[0.12em]";

type TeamDetailsPanelProps = {
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
      <div className="flex items-start justify-between gap-3">
        <div>
          <div className="text-sm font-semibold text-[var(--foreground)]">
            Team snapshot
          </div>
          <div className="mt-1 text-xs text-[var(--muted-foreground)]">
            {selectedTeam.id}
          </div>
        </div>
        <Badge>{selectedTeam.status || "unknown"}</Badge>
      </div>

      <div className="mt-3 grid gap-2 sm:grid-cols-2 xl:grid-cols-1">
        <div className={detailsCardClass}>
          <div className="flex items-center gap-2 text-xs uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
            <ActivityIcon size={14} />
            Tasks
          </div>
          <div className="mt-3 grid grid-cols-4 gap-2 text-xs text-[var(--muted-foreground)]">
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em]">Ready</div>
              <div className="mt-1 text-sm font-semibold text-[var(--foreground)]">
                {getSummaryCount(selectedSummary, "tasks", "ready")}
              </div>
            </div>
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em]">Running</div>
              <div className="mt-1 text-sm font-semibold text-[var(--foreground)]">
                {getSummaryCount(selectedSummary, "tasks", "running")}
              </div>
            </div>
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em]">Done</div>
              <div className="mt-1 text-sm font-semibold text-[var(--foreground)]">
                {getSummaryCount(selectedSummary, "tasks", "done")}
              </div>
            </div>
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em]">Failed</div>
              <div className="mt-1 text-sm font-semibold text-[var(--foreground)]">
                {getSummaryCount(selectedSummary, "tasks", "failed")}
              </div>
            </div>
          </div>
        </div>

        <div className={detailsCardClass}>
          <div className="flex items-center gap-2 text-xs uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
            <UsersRoundIcon size={14} />
            Teammates
          </div>
          <div className="mt-3 flex items-end justify-between gap-3">
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                Total
              </div>
              <div className="mt-1 text-sm font-semibold text-[var(--foreground)]">
                {selectedSummary?.teammates.total ?? 0}
              </div>
            </div>
            {selectedTeam.max_teammates ? (
              <div className="text-xs text-[var(--muted-foreground)]">
                cap {selectedTeam.max_teammates}
              </div>
            ) : null}
          </div>
        </div>
      </div>

      <div className={cn("mt-3", detailsCardClass)}>
        <div className="flex items-center gap-2 text-xs uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
          <GitBranchPlusIcon size={14} />
          Task Graph
          {isDetailsLoading ? (
            <LoaderCircleIcon size={14} className="animate-spin" />
          ) : null}
        </div>
        <div className="mt-3 grid grid-cols-3 gap-2 text-xs text-[var(--muted-foreground)]">
          <div>
            <div className="app-text-10 uppercase tracking-[0.14em]">Nodes</div>
            <div className="mt-1 text-sm font-semibold text-[var(--foreground)]">
              {details.graph?.count ?? 0}
            </div>
          </div>
          <div>
            <div className="app-text-10 uppercase tracking-[0.14em]">Edges</div>
            <div className="mt-1 text-sm font-semibold text-[var(--foreground)]">
              {graphEdgeCount}
            </div>
          </div>
          <div>
            <div className="app-text-10 uppercase tracking-[0.14em]">Missing</div>
            <div className="mt-1 text-sm font-semibold text-[var(--foreground)]">
              {graphMissingCount}
            </div>
          </div>
        </div>
      </div>

      <div className="mt-3 space-y-1.5 text-sm text-[var(--muted-foreground)]">
        {selectedTeam.workspace_id ? (
          <div>workspace: {selectedTeam.workspace_id}</div>
        ) : null}
        {selectedTeam.lead_session_id ? (
          <div>lead session: {truncateIdentifier(selectedTeam.lead_session_id, 18)}</div>
        ) : null}
        {selectedTeam.strategy ? <div>strategy: {selectedTeam.strategy}</div> : null}
      </div>

      {detailsError ? (
        <div className="mt-3 rounded-[0.8rem] border border-[#f0c77b]/18 bg-[#f0c77b]/8 px-3 py-2.5 text-sm leading-6 text-[var(--muted-foreground)]">
          {detailsError}
        </div>
      ) : null}

      <div className={detailsPanelClass}>
        <div className="flex items-center gap-2 text-xs uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
          Teammate roster
        </div>
        <div className="mt-3 space-y-2">
          {visibleTeammates.length > 0 ? (
            visibleTeammates.map((teammate) => (
              <div key={teammate.id} className={detailsCardClass}>
                <div className="flex items-center justify-between gap-3">
                  <div className="min-w-0">
                    <div className="truncate text-sm font-semibold text-[var(--foreground)]">
                      {teammate.name || truncateIdentifier(teammate.id, 18)}
                    </div>
                    <div className="truncate text-xs text-[var(--muted-foreground)]">
                      {teammate.profile || teammate.id}
                    </div>
                  </div>
                  <span
                    className={cn(
                      detailsPillClass,
                      statusTone(teammate.state),
                    )}
                  >
                    {teammate.state || "unknown"}
                  </span>
                </div>
                {teammate.capabilities && teammate.capabilities.length > 0 ? (
                  <div className="mt-2 flex flex-wrap gap-1.5">
                    {teammate.capabilities.slice(0, 3).map((capability) => (
                      <span
                        key={capability}
                        className="rounded-[0.65rem] bg-white/7 px-2 py-0.5 app-text-10 uppercase tracking-[0.12em] text-[var(--muted-foreground)]"
                      >
                        {capability}
                      </span>
                    ))}
                  </div>
                ) : null}
              </div>
            ))
          ) : (
            <div className="text-sm text-[var(--muted-foreground)]">
              No teammates registered.
            </div>
          )}
        </div>
      </div>

      <div className={detailsPanelClass}>
        <div className="flex items-center gap-2 text-xs uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
          Task queue
        </div>
        <div className="mt-3 space-y-2">
          {visibleTasks.length > 0 ? (
            visibleTasks.map((task) => (
              <div key={task.id} className={detailsCardClass}>
                <div className="flex items-center justify-between gap-3">
                  <div className="min-w-0">
                    <div className="truncate text-sm font-semibold text-[var(--foreground)]">
                      {task.title || truncateIdentifier(task.id, 18)}
                    </div>
                    <div className="truncate text-xs text-[var(--muted-foreground)]">
                      {task.goal || task.id}
                    </div>
                  </div>
                  <span
                    className={cn(
                      detailsPillClass,
                      statusTone(task.status),
                    )}
                  >
                    {task.status || "unknown"}
                  </span>
                </div>
                <div className="mt-2 flex flex-wrap gap-3 text-xs text-[var(--muted-foreground)]">
                  <span>priority {task.priority ?? 0}</span>
                  {task.assignee ? <span>assignee {task.assignee}</span> : null}
                  {task.parent_task_id ? (
                    <span>parent {truncateIdentifier(task.parent_task_id, 12)}</span>
                  ) : (
                    <span>root</span>
                  )}
                </div>
              </div>
            ))
          ) : (
            <div className="text-sm text-[var(--muted-foreground)]">
              No tasks available.
            </div>
          )}
        </div>
      </div>

      <div className={detailsPanelClass}>
        <div className="flex items-center justify-between gap-3">
          <div className="flex items-center gap-2 text-xs uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
            Mailbox
          </div>
          <Badge>{details.mailbox.length}</Badge>
        </div>
        <div className="mt-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
          Recent team messages with broadcast included
        </div>
        <div className="mt-3 rounded-[0.75rem] border border-white/8 bg-white/4 px-3 py-2.5">
          <div className="app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            Compose mailbox message
          </div>
          <div className="mt-3 grid gap-3 sm:grid-cols-2">
            <div>
              <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                From agent
              </div>
              <input
                value={mailboxFromDraft}
                onChange={(event) => onMailboxFromDraftChange(event.target.value)}
                placeholder="lead"
                className={detailsInputClass}
              />
            </div>
            <div>
              <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                To agent
              </div>
              <input
                value={mailboxToDraft}
                onChange={(event) => onMailboxToDraftChange(event.target.value)}
                placeholder="*"
                className={detailsInputClass}
              />
            </div>
            <div>
              <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                Kind
              </div>
              <input
                value={mailboxKindDraft}
                onChange={(event) => onMailboxKindDraftChange(event.target.value)}
                placeholder="info"
                className={detailsInputClass}
              />
            </div>
            <div>
              <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                Task id
              </div>
              <input
                value={mailboxTaskDraft}
                onChange={(event) => onMailboxTaskDraftChange(event.target.value)}
                placeholder="optional task id"
                className={detailsInputClass}
              />
            </div>
          </div>
          <div className="mt-3">
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              Body
            </div>
            <textarea
              value={mailboxBodyDraft}
              onChange={(event) => onMailboxBodyDraftChange(event.target.value)}
              placeholder="Ask a teammate to confirm scope, deliver an artifact, or acknowledge a task boundary..."
              className={cn(detailsInputClass, "min-h-24 resize-y leading-6")}
            />
          </div>
          <div className="mt-3 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
            <div className="text-xs text-[var(--muted-foreground)]">
              Use `*` in `to agent` for broadcast delivery.
            </div>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => onSendMailboxMessage()}
              disabled={isSendingMailbox || !mailboxBodyDraft.trim()}
            >
              {isSendingMailbox ? (
                <LoaderCircleIcon size={14} className="animate-spin" />
              ) : null}
              Send mailbox message
            </Button>
          </div>
        </div>
        {mailboxError ? (
          <div className="mt-3 rounded-[0.8rem] border border-[#f59e7d]/18 bg-[#f59e7d]/8 px-3 py-2.5 text-sm leading-6 text-[var(--muted-foreground)]">
            {mailboxError}
          </div>
        ) : null}
        <div className="mt-3 space-y-2">
          {visibleMailbox.length > 0 ? (
            visibleMailbox.map((message) => (
              <div key={message.id} className={detailsCardClass}>
                <div className="flex items-center justify-between gap-3">
                  <div className="min-w-0">
                    <div className="truncate text-sm font-semibold text-[var(--foreground)]">
                      {message.body || message.kind}
                    </div>
                    <div className="mt-1 truncate text-xs text-[var(--muted-foreground)]">
                      {describeMailboxRoute(message)}
                    </div>
                  </div>
                  <div className="flex shrink-0 items-center gap-2">
                    <span className="rounded-[0.65rem] border border-white/10 bg-white/6 px-2 py-0.5 app-text-10 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
                      {message.kind || "message"}
                    </span>
                    <span
                      className={cn(
                        detailsPillClass,
                        message.acked_at
                          ? "border-[#8fd0c6]/24 bg-[#8fd0c6]/10 text-[#8fd0c6]"
                          : "border-white/10 bg-white/6 text-[var(--muted-foreground)]",
                      )}
                    >
                      {message.acked_at ? "acked" : "pending"}
                    </span>
                  </div>
                </div>
                <div className="mt-2 flex flex-wrap gap-3 text-xs text-[var(--muted-foreground)]">
                  {message.created_at ? (
                    <span>created {formatRelativeTimestamp(message.created_at)}</span>
                  ) : null}
                  {message.acked_at ? (
                    <span>acked {formatRelativeTimestamp(message.acked_at)}</span>
                  ) : null}
                  <span>{truncateIdentifier(message.id, 14)}</span>
                </div>
                {!message.acked_at ? (
                  <div className="mt-3 flex justify-end">
                    <Button
                      variant="secondary"
                      size="sm"
                      onClick={() => onAckMailboxMessage(message.id)}
                      disabled={ackingMessageId === message.id}
                    >
                      {ackingMessageId === message.id ? (
                        <LoaderCircleIcon size={14} className="animate-spin" />
                      ) : null}
                      Ack message
                    </Button>
                  </div>
                ) : null}
              </div>
            ))
          ) : (
            <div className="text-sm text-[var(--muted-foreground)]">
              No mailbox activity available.
            </div>
          )}
        </div>
      </div>

      <div className={detailsPanelClass}>
        <div className="flex items-center justify-between gap-3">
          <div className="flex items-center gap-2 text-xs uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
            Path claims
          </div>
          <Badge>{details.pathClaims.length}</Badge>
        </div>
        <div className="mt-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
          Active filesystem leases for runtime writers and readers
        </div>
        <div className="mt-3 rounded-[0.75rem] border border-white/8 bg-white/4 px-3 py-2.5">
          <div className="app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            Conflict check
          </div>
          <div className="mt-3 grid gap-3">
            <div>
              <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                Read paths
              </div>
              <textarea
                value={readPathDraft}
                onChange={(event) => onReadPathDraftChange(event.target.value)}
                placeholder="src/components/workspace/runtime-teams.tsx"
                className={cn(detailsInputClass, "min-h-20 resize-y leading-6")}
              />
            </div>
            <div>
              <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                Write paths
              </div>
              <textarea
                value={writePathDraft}
                onChange={(event) => onWritePathDraftChange(event.target.value)}
                placeholder="frontend/src/lib/runtime-api.ts"
                className={cn(detailsInputClass, "min-h-20 resize-y leading-6")}
              />
            </div>
          </div>
          <div className="mt-3 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
            <div className="text-xs text-[var(--muted-foreground)]">
              Separate multiple paths with new lines or commas.
            </div>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => onCheckPathClaims()}
              disabled={isCheckingClaims}
            >
              {isCheckingClaims ? (
                <LoaderCircleIcon size={14} className="animate-spin" />
              ) : null}
              Check conflicts
            </Button>
          </div>
          {claimCheckError ? (
            <div className="mt-3 rounded-[0.8rem] border border-[#f59e7d]/18 bg-[#f59e7d]/8 px-3 py-2.5 text-sm leading-6 text-[var(--muted-foreground)]">
              {claimCheckError}
            </div>
          ) : null}
          {claimCheckState ? (
            <div className="mt-3 rounded-[0.8rem] border border-white/8 bg-black/20 px-3 py-2.5">
              <div className="flex items-center justify-between gap-3">
                <div className="text-sm font-semibold text-[var(--foreground)]">
                  {claimCheckState.ok ? "No conflicts detected" : "Conflicts detected"}
                </div>
                <span
                  className={cn(
                    detailsPillClass,
                    claimCheckState.ok
                      ? "border-[#8fd0c6]/24 bg-[#8fd0c6]/10 text-[#8fd0c6]"
                      : "border-[#f59e7d]/24 bg-[#f59e7d]/10 text-[#f59e7d]",
                  )}
                >
                  {claimCheckState.conflicts.length} conflicts
                </span>
              </div>
              {!claimCheckState.ok && claimCheckState.conflicts.length > 0 ? (
                <div className="mt-3 space-y-2">
                  {claimCheckState.conflicts.map((conflict, index) => (
                    <div
                      key={`${conflict.path}-${conflict.existing_path}-${index}`}
                      className="rounded-[0.75rem] border border-white/8 bg-white/4 px-3 py-2.5 text-sm leading-6 text-[var(--muted-foreground)]"
                    >
                      {summarizeConflict(conflict)}
                    </div>
                  ))}
                </div>
              ) : (
                <div className="mt-2 text-sm text-[var(--muted-foreground)]">
                  Requested reads and writes can be acquired at the current runtime snapshot.
                </div>
              )}
            </div>
          ) : null}
        </div>
        <div className="mt-3 space-y-2">
          {visiblePathClaims.length > 0 ? (
            visiblePathClaims.map((claim) => {
              const active = isClaimActive(claim.lease_until);
              return (
                <div key={claim.id} className={detailsCardClass}>
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <div className="break-all text-sm font-semibold text-[var(--foreground)]">
                        {claim.path}
                      </div>
                      <div className="mt-1 flex flex-wrap gap-3 text-xs text-[var(--muted-foreground)]">
                        <span>owner {truncateIdentifier(claim.owner_agent_id, 14)}</span>
                        <span>task {truncateIdentifier(claim.task_id, 14)}</span>
                      </div>
                    </div>
                    <div className="flex shrink-0 items-center gap-2">
                      <span className="rounded-[0.65rem] border border-white/10 bg-white/6 px-2 py-0.5 app-text-10 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
                        {claim.mode || "claim"}
                      </span>
                      <span
                        className={cn(
                          detailsPillClass,
                          active
                            ? "border-[#8fd0c6]/24 bg-[#8fd0c6]/10 text-[#8fd0c6]"
                            : "border-[#f59e7d]/24 bg-[#f59e7d]/10 text-[#f59e7d]",
                        )}
                      >
                        {active ? "active" : "expired"}
                      </span>
                    </div>
                  </div>
                  <div className="mt-2 flex flex-wrap gap-3 text-xs text-[var(--muted-foreground)]">
                    {claim.lease_until ? (
                      <span>lease {formatRelativeTimestamp(claim.lease_until)}</span>
                    ) : (
                      <span>lease open-ended</span>
                    )}
                    <span>{truncateIdentifier(claim.id, 14)}</span>
                  </div>
                </div>
              );
            })
          ) : (
            <div className="text-sm text-[var(--muted-foreground)]">
              No active path claims available.
            </div>
          )}
        </div>
      </div>

      <div className={detailsPanelClass}>
        <div className="flex items-center gap-2 text-xs uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
          Team timeline
        </div>
        <div className="mt-3 space-y-2">
          {visibleEvents.length > 0 ? (
            visibleEvents.map((event) => (
              <div key={`${event.seq}-${event.type}`} className={detailsCardClass}>
                <div className="flex items-center justify-between gap-3">
                  <div className="min-w-0">
                    <div className="truncate text-sm font-semibold text-[var(--foreground)]">
                      {prettyEventType(event.type)}
                    </div>
                    <div className="mt-1 truncate text-xs text-[var(--muted-foreground)]">
                      {describeEventPayload(event)}
                    </div>
                  </div>
                  <div className="shrink-0 text-right">
                    <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                      seq {event.seq}
                    </div>
                    <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                      {formatRelativeTimestamp(event.timestamp)}
                    </div>
                  </div>
                </div>
              </div>
            ))
          ) : (
            <div className="text-sm text-[var(--muted-foreground)]">
              No team events available.
            </div>
          )}
        </div>
      </div>

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
    </div>
  );
}
