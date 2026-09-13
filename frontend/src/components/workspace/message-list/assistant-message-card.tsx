// 由 components/workspace/message-list.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { BotIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { type Artifact, type ChatMessage } from "@/data/mock";
import { getToolSegmentKey } from "@/lib/workspace-thread-state";

import {
  renderMessageSegment,
  renderRelatedArtifactSection,
} from "./segment-rendering";

type AssistantMessageCardProps = {
  labelId: string;
  message: ChatMessage;
  metaId: string;
  onSelectArtifact: (artifactId: string) => void;
  relatedEvidence: Artifact[];
  statusId: string;
  streamingMessageId: string | null;
};

export function AssistantMessageCard({
  labelId,
  message,
  metaId,
  onSelectArtifact,
  relatedEvidence,
  statusId,
  streamingMessageId,
}: AssistantMessageCardProps) {
  return (
                <div className="relative w-full max-w-[48rem]">
                  <div className="overflow-hidden rounded-[1rem] border border-accent-teal/14 bg-[linear-gradient(180deg,rgba(143,208,198,0.08),rgba(143,208,198,0.02))] px-4 py-3.5 shadow-[0_16px_40px_rgba(0,0,0,0.12)]">
                    <div className="flex items-start gap-3">
                      <div className="mt-0.5 inline-flex size-7 shrink-0 items-center justify-center rounded-field border border-accent-teal/20 bg-accent-teal/10 text-accent-teal">
                        <BotIcon size={14} />
                      </div>
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <div
                            className="app-text-13 font-semibold text-foreground"
                            id={labelId}
                          >
                            {message.author}
                          </div>
                          <div
                            className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground"
                            id={metaId}
                          >
                            {message.label}
                          </div>
                          {message.id === streamingMessageId ? (
                            <Badge className="border-transparent bg-accent-teal/12 text-accent-teal">
                              Streaming response in progress
                            </Badge>
                          ) : null}
                        </div>

                        <div className="relative mt-3" id={statusId}>
                          <div className="pointer-events-none absolute left-0 top-4 bottom-4 w-px bg-gradient-to-b from-accent-teal/0 via-accent-teal/18 to-accent-teal/0" />

                          <div className="relative space-y-4">
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

                          {renderRelatedArtifactSection(
                            relatedEvidence,
                            onSelectArtifact,
                          )}
                        </div>
                      </div>
                    </div>
                  </div>
                </div>
  );
}
