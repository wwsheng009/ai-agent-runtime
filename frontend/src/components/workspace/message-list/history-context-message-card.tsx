// 批次 B3/F4（§8.5 context / system-prompt）：24px 单行 + 展开面板。
// 去卡片：无外层 bg/border/shadow、无头像 chip、无竖直渐变线；来源文件名/包名直接在行内可读。
// F4：`system-prompt` 与 `context` 拆为**不同渲染分支**（不再共用一行语义）。

import { BotIcon, ScrollTextIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { ChatProcessRow } from "@/components/workspace/chat-process-row";
import { contextSource, projectChatView } from "@/lib/chat-view";
import { createArtifactFilePathLinkResolver } from "@/lib/tool-row/artifact-links";
import { type Artifact, type ChatMessage } from "@/data/mock";

import {
  renderMessageSegment,
  renderRelatedArtifactSection,
} from "./segment-rendering";

export function HistoryContextMessageCard({
  labelId,
  message,
  metaId,
  onPreviewFilePath,
  onSelectArtifact,
  relatedEvidence,
  statusId,
  streamingMessageId,
}: {
  labelId: string;
  message: ChatMessage;
  metaId: string;
  /** P2-1A：关联产物未命中时的运行时文件预览兜底。 */
  onPreviewFilePath?: (path: string) => void;
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
    onPreviewFilePath,
  );
  const panelId = `${message.id}-context-panel`;
  const view = projectChatView(message, { expanded });
  const isSystemPrompt = view.systemPrompt;
  const title = isSystemPrompt
    ? t("panels.messages.systemPrompt.title")
    : t("panels.messages.contextRow.title");
  const toggleLabel = expanded
    ? t(
        isSystemPrompt
          ? "panels.messages.systemPrompt.collapse"
          : "panels.messages.contextRow.collapse",
      )
    : t(
        isSystemPrompt
          ? "panels.messages.systemPrompt.expand"
          : "panels.messages.contextRow.expand",
      );
  const streaming = message.id === streamingMessageId;
  const flowKey = `${isSystemPrompt ? "system-prompt" : "context"}:${message.id}`;

  return (
    <div className="w-full min-w-0" data-chat-anchor-key={message.id}>
      <span className="sr-only" id={labelId}>
        {title}
      </span>
      <span className="sr-only" id={metaId}>
        {message.label}
      </span>
      <div id={statusId}>
        <ChatProcessRow
          anchorKey={message.id}
          expandable
          expanded={expanded}
          flowKey={flowKey}
          icon={
            isSystemPrompt ? (
              <ScrollTextIcon className="size-4 text-accent-teal" />
            ) : (
              <BotIcon className="size-4 text-accent-teal" />
            )
          }
          onToggle={() => setExpanded((value) => !value)}
          panelId={panelId}
          rowKind={isSystemPrompt ? "system-prompt" : "context"}
          summary={isSystemPrompt ? undefined : contextSource(message)}
          title={title}
          titleClassName="text-muted-foreground"
          toggleLabel={toggleLabel}
          trailing={
            streaming ? (
              <>
                <span
                  aria-hidden="true"
                  className="size-1.5 shrink-0 animate-pulse rounded-full bg-accent-teal"
                />
                <span className="sr-only">
                  {t("panels.messages.messageCard.streamingBadge")}
                </span>
              </>
            ) : null
          }
        >
          {/* 展开面板：§8.5 代码面板形制（无边框 + 底色分隔 + max-height 141px）。 */}
          <div
            className="mt-1 max-h-[141px] overflow-auto rounded-lg bg-surface-strong px-3 py-2 font-mono app-text-11"
            data-chat-panel="context"
          >
            <div className="space-y-2">
              {view.nodes.map((node) => (
                <div key={node.key}>
                  {renderMessageSegment(node.segment, {
                    interrupted: message.interrupted === true,
                    streaming: false,
                    onSelectArtifact,
                    resolveFilePathLink,
                  })}
                </div>
              ))}
            </div>
            {renderRelatedArtifactSection(relatedEvidence, onSelectArtifact)}
          </div>
        </ChatProcessRow>
      </div>
    </div>
  );
}
