import { LoaderCircleIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { MessageMarkdown } from "@/components/workspace/message-markdown";
import {
  describeMailboxRoute,
  type TeamDetailsState,
  truncateIdentifier,
} from "@/components/workspace/runtime-teams/shared";
import { cn, formatRelativeTimestamp } from "@/lib/utils";

import {
  detailCardClass,
  detailControlClass,
  detailMetaPillClass,
  detailStatusPillClass,
} from "./format";
import { TeamDetailsSection } from "./primitives";

type RuntimeTeamMailboxSectionProps = {
  ackingMessageId: string;
  details: TeamDetailsState;
  isSendingMailbox: boolean;
  mailboxBodyDraft: string;
  mailboxError: string | null;
  mailboxFromDraft: string;
  mailboxKindDraft: string;
  mailboxTaskDraft: string;
  mailboxToDraft: string;
  onAckMailboxMessage: (messageId: string) => void;
  onMailboxBodyDraftChange: (value: string) => void;
  onMailboxFromDraftChange: (value: string) => void;
  onMailboxKindDraftChange: (value: string) => void;
  onMailboxTaskDraftChange: (value: string) => void;
  onMailboxToDraftChange: (value: string) => void;
  onSendMailboxMessage: () => void;
  onToggle: () => void;
  open: boolean;
  visibleMailbox: TeamDetailsState["mailbox"];
};

export function RuntimeTeamMailboxSection({
  ackingMessageId,
  details,
  isSendingMailbox,
  mailboxBodyDraft,
  mailboxError,
  mailboxFromDraft,
  mailboxKindDraft,
  mailboxTaskDraft,
  mailboxToDraft,
  onAckMailboxMessage,
  onMailboxBodyDraftChange,
  onMailboxFromDraftChange,
  onMailboxKindDraftChange,
  onMailboxTaskDraftChange,
  onMailboxToDraftChange,
  onSendMailboxMessage,
  onToggle,
  open,
  visibleMailbox,
}: RuntimeTeamMailboxSectionProps) {
  return (
    <TeamDetailsSection
      title="Mailbox"
      subtitle="Recent team messages with broadcast included"
      badge={<Badge>{details.mailbox.length}</Badge>}
      open={open}
      onToggle={onToggle}
    >
      <div className={detailCardClass}>
        <div className="app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
          Compose mailbox message
        </div>
        <div className="mt-2.5 grid gap-2.5 sm:grid-cols-2">
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              From agent
            </div>
            <input
              value={mailboxFromDraft}
              onChange={(event) => onMailboxFromDraftChange(event.target.value)}
              placeholder="lead"
              className={detailControlClass}
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
              className={detailControlClass}
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
              className={detailControlClass}
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
              className={detailControlClass}
            />
          </div>
        </div>
        <div className="mt-2.5">
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            Body
          </div>
          <textarea
            value={mailboxBodyDraft}
            onChange={(event) => onMailboxBodyDraftChange(event.target.value)}
            placeholder="Ask a teammate to confirm scope, deliver an artifact, or acknowledge a task boundary..."
            className={`min-h-24 ${detailControlClass} resize-y leading-6`}
          />
        </div>
        <div className="mt-2.5 flex flex-col gap-2.5 sm:flex-row sm:items-center sm:justify-between">
          <div className="text-xs text-[var(--muted-foreground)]">
            Use `*` in `to agent` for broadcast delivery.
          </div>
          <Button
            variant="secondary"
            size="sm"
            onClick={onSendMailboxMessage}
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
        <div className="mt-2.5 rounded-[0.75rem] border border-[#f59e7d]/18 bg-[#f59e7d]/8 px-3 py-2.5 text-sm leading-6 text-[var(--muted-foreground)]">
          {mailboxError}
        </div>
      ) : null}
      <div className="mt-2.5 space-y-1.5">
        {visibleMailbox.length > 0 ? (
          visibleMailbox.map((message) => (
            <div
              key={message.id}
              className={detailCardClass}
            >
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <div className="truncate text-[13px] font-semibold text-[var(--foreground)]">
                    {message.kind || "message"}
                  </div>
                  <div className="mt-0.5 truncate text-xs text-[var(--muted-foreground)]">
                    {describeMailboxRoute(message)}
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <span className={detailMetaPillClass}>
                    {message.kind || "message"}
                  </span>
                  <span
                    className={cn(
                      detailStatusPillClass,
                      message.acked_at
                        ? "border-[#8fd0c6]/24 bg-[#8fd0c6]/10 text-[#8fd0c6]"
                        : "border-white/10 bg-white/6 text-[var(--muted-foreground)]",
                    )}
                  >
                    {message.acked_at ? "acked" : "pending"}
                  </span>
                </div>
              </div>
              {message.body.trim() ? (
                <div className="mt-2 rounded-[0.75rem] border border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2.5">
                  <MessageMarkdown
                    className="app-text-13"
                    content={message.body}
                  />
                </div>
              ) : null}
              <div className="mt-1.5 flex flex-wrap gap-2.5 text-xs text-[var(--muted-foreground)]">
                {message.created_at ? (
                  <span>created {formatRelativeTimestamp(message.created_at)}</span>
                ) : null}
                {message.acked_at ? (
                  <span>acked {formatRelativeTimestamp(message.acked_at)}</span>
                ) : null}
                <span>{truncateIdentifier(message.id, 14)}</span>
              </div>
              {!message.acked_at ? (
                <div className="mt-2 flex justify-end">
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
    </TeamDetailsSection>
  );
}
