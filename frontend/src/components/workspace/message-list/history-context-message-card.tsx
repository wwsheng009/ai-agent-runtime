// 由 components/workspace/message-list.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { BotIcon, ChevronDownIcon } from "lucide-react";
import { useState } from "react";

import { Badge } from "@/components/ui/badge";
import { getToolSegmentKey } from "@/lib/workspace-thread-state";
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
  const [expanded, setExpanded] = useState(false);
  const panelId = `${message.id}-context-panel`;

  return (
    <div className="relative w-full max-w-[48rem]">
      <div className="overflow-hidden rounded-[1rem] border border-[#8fd0c6]/14 bg-[linear-gradient(180deg,rgba(143,208,198,0.08),rgba(143,208,198,0.02))] px-4 py-3.5 shadow-[0_16px_40px_rgba(0,0,0,0.12)]">
        <button
          type="button"
          aria-expanded={expanded}
          aria-controls={panelId}
          onClick={() => setExpanded((v) => !v)}
          className="flex w-full items-start gap-3 text-left"
        >
          <div className="mt-0.5 inline-flex size-7 shrink-0 items-center justify-center rounded-[0.7rem] border border-[#8fd0c6]/20 bg-[#8fd0c6]/10 text-[#8fd0c6]">
            <BotIcon size={14} />
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <div
                className="app-text-13 font-semibold text-[var(--foreground)]"
                id={labelId}
              >
                {message.author}
              </div>
              <div
                className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]"
                id={metaId}
              >
                {message.label}
              </div>
              {message.id === streamingMessageId ? (
                <Badge className="border-transparent bg-[#8fd0c6]/12 text-[#8fd0c6]">
                  Streaming response in progress
                </Badge>
              ) : null}
            </div>
            <div className="mt-0.5 app-text-11 text-[var(--muted-foreground)]">
              {expanded
                ? "Content visible — click to collapse"
                : "Content hidden — click to expand"}
            </div>
          </div>
          <ChevronDownIcon
            size={16}
            className={cn(
              "mt-2 shrink-0 text-[var(--muted-foreground)] transition-transform duration-200",
              expanded ? "rotate-0" : "-rotate-90",
            )}
          />
        </button>
        <div className="relative mt-3" hidden={!expanded} id={panelId}>
          <div className="pointer-events-none absolute left-0 top-4 bottom-4 w-px bg-gradient-to-b from-[#8fd0c6]/0 via-[#8fd0c6]/18 to-[#8fd0c6]/0" />

          <div className="relative space-y-4" id={statusId}>
            {message.segments.map((segment, index) => (
              <div
                key={
                  segment.type === "tool"
                    ? `${message.id}-tool-${getToolSegmentKey(segment)}`
                    : `${message.id}-${segment.type}-${index}`
                }
              >
                {renderMessageSegment(segment, {
                  interrupted: message.interrupted === true,
                  streaming: message.id === streamingMessageId,
                  onSelectArtifact,
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
