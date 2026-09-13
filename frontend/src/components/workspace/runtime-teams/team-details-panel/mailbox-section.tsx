// 由 components/workspace/runtime-teams/team-details-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  describeMailboxRoute,
  truncateIdentifier,
} from "@/components/workspace/runtime-teams/shared";
import { cn, formatRelativeTimestamp } from "@/lib/utils";
import { LoaderCircleIcon } from "lucide-react";

import { detailsCardClass, detailsInputClass, detailsPanelClass, detailsPillClass } from "./format";
import { type TeamDetailsPanelProps } from "./types";

type TeamDetailsPanelMailboxProps = Pick<
  TeamDetailsPanelProps,
  | "ackingMessageId"
  | "details"
  | "isSendingMailbox"
  | "mailboxBodyDraft"
  | "mailboxError"
  | "mailboxFromDraft"
  | "mailboxKindDraft"
  | "mailboxTaskDraft"
  | "mailboxToDraft"
  | "onAckMailboxMessage"
  | "onMailboxBodyDraftChange"
  | "onMailboxFromDraftChange"
  | "onMailboxKindDraftChange"
  | "onMailboxTaskDraftChange"
  | "onMailboxToDraftChange"
  | "onSendMailboxMessage"
  | "visibleMailbox"
>;

export function TeamDetailsPanelMailbox({
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
  visibleMailbox,
}: TeamDetailsPanelMailboxProps) {
  return (
    <div className={detailsPanelClass}>
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-2 text-xs uppercase tracking-[0.16em] text-muted-foreground">
          Mailbox
        </div>
        <Badge>{details.mailbox.length}</Badge>
      </div>
      <div className="mt-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
        Recent team messages with broadcast included
      </div>
      <div className="mt-3 rounded-[0.75rem] border border-white/8 bg-white/4 px-3 py-2.5">
        <div className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
          Compose mailbox message
        </div>
        <div className="mt-3 grid gap-3 sm:grid-cols-2">
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
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
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
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
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
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
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
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
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
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
          <div className="text-xs text-muted-foreground">
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
        <div className="mt-3 rounded-[0.8rem] border border-accent-orange/18 bg-accent-orange/8 px-3 py-2.5 text-sm leading-6 text-muted-foreground">
          {mailboxError}
        </div>
      ) : null}
      <div className="mt-3 space-y-2">
        {visibleMailbox.length > 0 ? (
          visibleMailbox.map((message) => (
            <div key={message.id} className={detailsCardClass}>
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <div className="truncate text-sm font-semibold text-foreground">
                    {message.body || message.kind}
                  </div>
                  <div className="mt-1 truncate text-xs text-muted-foreground">
                    {describeMailboxRoute(message)}
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <span className="rounded-[0.65rem] border border-white/10 bg-white/6 px-2 py-0.5 app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
                    {message.kind || "message"}
                  </span>
                  <span
                    className={cn(
                      detailsPillClass,
                      message.acked_at
                        ? "border-accent-teal/24 bg-accent-teal/10 text-accent-teal"
                        : "border-white/10 bg-white/6 text-muted-foreground",
                    )}
                  >
                    {message.acked_at ? "acked" : "pending"}
                  </span>
                </div>
              </div>
              <div className="mt-2 flex flex-wrap gap-3 text-xs text-muted-foreground">
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
          <div className="text-sm text-muted-foreground">
            No mailbox activity available.
          </div>
        )}
      </div>
    </div>
  );
}
