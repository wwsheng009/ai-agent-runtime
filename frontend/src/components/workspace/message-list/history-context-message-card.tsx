// 由 components/workspace/message-list.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { BotIcon, ChevronDownIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { projectChatView } from "@/lib/chat-view";
import { createArtifactFilePathLinkResolver } from "@/lib/tool-row/artifact-links";
import { cn } from "@/lib/utils";
import { type Artifact, type ChatMessage } from "@/data/mock";

import {
  renderMessageSegment,
  renderRelatedArtifactSection,
} from "./segment-rendering";

export function HistoryContextMessageCard({
  labelId,
  message,
  metaId,
  onSelectArtifact,
  relatedEvidence,
  statusId,
  streamingMessageId,
}: {
  labelId: string;
  message: ChatMessage;
  metaId: string;
  onSelectArtifact: (artifactId: string) => void;
  relatedEvidence: Artifact[];
  statusId: string;
  streamingMessageId: string | null;
}) {
  const { t } = useTranslation("workspace");
  const [expanded, setExpanded] = useState(false);
  const resolveFilePathLink = createArtifactFilePathLinkResolver(
    relatedEvidence,
    onSelectArtifact,
  );
  const panelId = `${message.id}-context-panel`;
  const view = projectChatView(message, { expanded });
  const toggleHint = expanded
    ? t(
        view.systemPrompt
          ? "panels.messages.systemPrompt.collapse"
          : "panels.messages.contextRow.collapse",
      )
    : t(
        view.systemPrompt
          ? "panels.messages.systemPrompt.expand"
          : "panels.messages.contextRow.expand",
      );

  return (
    <div className="relative w-full max-w-[48rem]">
      <div className="overflow-hidden rounded-[1rem] border border-accent-teal/14 bg-[linear-gradient(180deg,rgba(143,208,198,0.08),rgba(143,208,198,0.02))] px-4 py-3.5 shadow-[0_16px_40px_rgba(0,0,0,0.12)]">
        <button
          type="button"
          aria-expanded={expanded}
          aria-controls={panelId}
          onClick={() => setExpanded((v) => !v)}
          className="flex w-full items-start gap-3 text-left"
        >
          <div className="mt-0.5 inline-flex size-7 shrink-0 items-center justify-center rounded-field border border-accent-teal/20 bg-accent-teal/10 text-accent-teal">
            <BotIcon size={14} />
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <div
                className="app-text-13 font-semibold text-foreground"
                id={labelId}
              >
                {view.systemPrompt
                  ? t("panels.messages.systemPrompt.title")
                  : message.author}
              </div>
              <div
                className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground"
                id={metaId}
              >
                {message.label}
              </div>
              {message.id === streamingMessageId ? (
                <Badge className="border-transparent bg-accent-teal/12 text-accent-teal">
                  {t("panels.messages.messageCard.streamingBadge")}
                </Badge>
              ) : null}
            </div>
            <div className="mt-0.5 app-text-11 text-muted-foreground">
              {toggleHint}
            </div>
          </div>
          <ChevronDownIcon
            size={16}
            className={cn(
              "mt-2 shrink-0 text-muted-foreground transition-transform duration-200",
              expanded ? "rotate-0" : "-rotate-90",
            )}
          />
        </button>
        <div className="relative mt-3" hidden={!expanded} id={panelId}>
          <div className="pointer-events-none absolute left-0 top-4 bottom-4 w-px bg-gradient-to-b from-accent-teal/0 via-accent-teal/18 to-accent-teal/0" />

          <div className="relative space-y-4" id={statusId}>
            {view.nodes.map((node) => (
              <div key={node.key}>
                {renderMessageSegment(node.segment, {
                  interrupted: message.interrupted === true,
                  streaming: message.id === streamingMessageId,
                  onSelectArtifact,
                  resolveFilePathLink,
                })}
              </div>
            ))}
          </div>

          {renderRelatedArtifactSection(relatedEvidence, onSelectArtifact)}
        </div>
      </div>
    </div>
  );
}
