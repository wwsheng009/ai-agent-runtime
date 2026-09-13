// 由 components/workspace/runtime-teams/team-details-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  describeMailboxRoute,
  truncateIdentifier,
} from "@/components/workspace/runtime-teams/shared";
import { cn, formatRelativeTimestamp } from "@/lib/utils";
import { LoaderCircleIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

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
  const { t } = useTranslation("workspace");

  return (
    <div className={detailsPanelClass}>
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-2 text-xs uppercase tracking-[0.16em] text-muted-foreground">
          {t("panels.teamsPanels.details.mailbox.title")}
        </div>
        <Badge>{details.mailbox.length}</Badge>
      </div>
      <div className="mt-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
        {t("panels.teamsPanels.details.mailbox.subtitle")}
      </div>
      <div className="mt-3 rounded-[0.75rem] border border-white/8 bg-white/4 px-3 py-2.5">
        <div className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
          {t("panels.teamsPanels.details.mailbox.compose")}
        </div>
        <div className="mt-3 grid gap-3 sm:grid-cols-2">
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              {t("panels.teamsPanels.details.mailbox.fromAgent")}
            </div>
            <input
              value={mailboxFromDraft}
              onChange={(event) => onMailboxFromDraftChange(event.target.value)}
              placeholder={t("panels.teamsPanels.details.mailbox.fromPlaceholder")}
              className={detailsInputClass}
            />
          </div>
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              {t("panels.teamsPanels.details.mailbox.toAgent")}
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
              {t("panels.teamsPanels.details.mailbox.kind")}
            </div>
            <input
              value={mailboxKindDraft}
              onChange={(event) => onMailboxKindDraftChange(event.target.value)}
              placeholder={t("panels.teamsPanels.details.mailbox.kindPlaceholder")}
              className={detailsInputClass}
            />
          </div>
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              {t("panels.teamsPanels.details.mailbox.taskId")}
            </div>
            <input
              value={mailboxTaskDraft}
              onChange={(event) => onMailboxTaskDraftChange(event.target.value)}
              placeholder={t("panels.teamsPanels.details.mailbox.taskIdPlaceholder")}
              className={detailsInputClass}
            />
          </div>
        </div>
        <div className="mt-3">
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
            {t("panels.teamsPanels.details.mailbox.body")}
          </div>
          <textarea
            value={mailboxBodyDraft}
            onChange={(event) => onMailboxBodyDraftChange(event.target.value)}
            placeholder={t("panels.teamsPanels.details.mailbox.bodyPlaceholder")}
            className={cn(detailsInputClass, "min-h-24 resize-y leading-6")}
          />
        </div>
        <div className="mt-3 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div className="text-xs text-muted-foreground">
            {t("panels.teamsPanels.details.mailbox.broadcastHint")}
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
            {t("panels.teamsPanels.details.mailbox.send")}
          </Button>
        </div>
      </div>
      {mailboxError ? (
        <div className="mt-3 rounded-card border border-accent-orange/18 bg-accent-orange/8 px-3 py-2.5 text-sm leading-6 text-muted-foreground">
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
                  <span className="rounded-control border border-white/10 bg-white/6 px-2 py-0.5 app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
                    {message.kind ||
                      t("panels.teamsPanels.details.mailbox.fallbackKind")}
                  </span>
                  <span
                    className={cn(
                      detailsPillClass,
                      message.acked_at
                        ? "border-accent-teal/24 bg-accent-teal/10 text-accent-teal"
                        : "border-white/10 bg-white/6 text-muted-foreground",
                    )}
                  >
                    {message.acked_at
                      ? t("panels.teamsPanels.details.mailbox.acked")
                      : t("panels.teamsPanels.details.mailbox.pending")}
                  </span>
                </div>
              </div>
              <div className="mt-2 flex flex-wrap gap-3 text-xs text-muted-foreground">
                {message.created_at ? (
                  <span>
                    {t("panels.teamsPanels.details.mailbox.createdAt", {
                      time: formatRelativeTimestamp(message.created_at),
                    })}
                  </span>
                ) : null}
                {message.acked_at ? (
                  <span>
                    {t("panels.teamsPanels.details.mailbox.ackedAt", {
                      time: formatRelativeTimestamp(message.acked_at),
                    })}
                  </span>
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
                    {t("panels.teamsPanels.details.mailbox.ack")}
                  </Button>
                </div>
              ) : null}
            </div>
          ))
        ) : (
          <div className="text-sm text-muted-foreground">
            {t("panels.teamsPanels.details.mailbox.empty")}
          </div>
        )}
      </div>
    </div>
  );
}
