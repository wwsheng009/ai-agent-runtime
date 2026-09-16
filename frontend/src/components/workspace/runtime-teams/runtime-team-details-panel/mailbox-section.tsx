import { LoaderCircleIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

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
  const { t } = useTranslation("workspace");

  return (
    <TeamDetailsSection
      title={t("panels.teamsPanels.details.mailbox.title")}
      subtitle={t("panels.teamsPanels.details.mailbox.subtitle")}
      badge={<Badge>{details.mailbox.length}</Badge>}
      open={open}
      onToggle={onToggle}
    >
      <div className={detailCardClass}>
        <div className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
          {t("panels.teamsPanels.details.mailbox.compose")}
        </div>
        <div className="mt-2.5 grid gap-2.5 sm:grid-cols-2">
          <div>
            <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
              {t("panels.teamsPanels.details.mailbox.fromAgent")}
            </div>
            <input
              value={mailboxFromDraft}
              onChange={(event) => onMailboxFromDraftChange(event.target.value)}
              placeholder={t("panels.teamsPanels.details.mailbox.fromPlaceholder")}
              className={detailControlClass}
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
              className={detailControlClass}
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
              className={detailControlClass}
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
              className={detailControlClass}
            />
          </div>
        </div>
        <div className="mt-2.5">
          <div className="mb-2 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
            {t("panels.teamsPanels.details.mailbox.body")}
          </div>
          <textarea
            value={mailboxBodyDraft}
            onChange={(event) => onMailboxBodyDraftChange(event.target.value)}
            placeholder={t("panels.teamsPanels.details.mailbox.bodyPlaceholder")}
            className={`min-h-24 ${detailControlClass} resize-y leading-6`}
          />
        </div>
        <div className="mt-2.5 flex flex-col gap-2.5 sm:flex-row sm:items-center sm:justify-between">
          <div className="text-xs text-muted-foreground">
            {t("panels.teamsPanels.details.mailbox.broadcastHint")}
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
            {t("panels.teamsPanels.details.mailbox.send")}
          </Button>
        </div>
      </div>
      {mailboxError ? (
        <div className="mt-2.5 rounded-[0.75rem] border border-accent-orange/18 bg-accent-orange/8 px-3 py-2.5 text-sm leading-6 text-muted-foreground">
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
                  <div className="truncate app-text-13 font-semibold text-foreground">
                    {message.kind ||
                      t("panels.teamsPanels.details.mailbox.fallbackKind")}
                  </div>
                  <div className="mt-0.5 truncate text-xs text-muted-foreground">
                    {describeMailboxRoute(message)}
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <span className={detailMetaPillClass}>
                    {message.kind ||
                      t("panels.teamsPanels.details.mailbox.fallbackKind")}
                  </span>
                  <span
                    className={cn(
                      detailStatusPillClass,
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
              {message.body.trim() ? (
                <div className="mt-2 rounded-[0.75rem] border border-border bg-surface-solid px-3 py-2.5">
                  <MessageMarkdown
                    className="app-text-13"
                    content={message.body}
                  />
                </div>
              ) : null}
              <div className="mt-1.5 flex flex-wrap gap-2.5 text-xs text-muted-foreground">
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
    </TeamDetailsSection>
  );
}
