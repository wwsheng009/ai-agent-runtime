// 批次 B4/B5 补充（§8.5 tool-call）：历史工具回执行。
//
// 历史里 `role="tool"` 回执先被 history-mapping 还原成 tool segment，这里逐条按
// MessageToolRow 的 24px 单行呈现（图标 + 工具名 + 目标摘要 + 状态），点击展开详情；
// 不再落到通用「上下文注入」行，从而能看出读的是哪个文件、改了哪个文件、跑的是什么命令。

import { useTranslation } from "react-i18next";

import { type Artifact, type ChatMessage } from "@/data/mock";
import { projectChatView } from "@/lib/chat-view";
import { createArtifactFilePathLinkResolver } from "@/lib/tool-row/artifact-links";

import { HistoryContextMessageCard } from "./history-context-message-card";
import {
  renderMessageSegment,
  renderRelatedArtifactSection,
} from "./segment-rendering";

export function HistoryToolMessageRow({
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
  const resolveFilePathLink = createArtifactFilePathLinkResolver(
    relatedEvidence,
    onSelectArtifact,
    onPreviewFilePath,
  );
  const nodes = projectChatView(message, { expanded: true }).nodes;
  if (!nodes.some((node) => node.kind === "tool")) {
    // 无 tool 段的旧数据：与 flow 的降级分支保持一致，回落上下文行，不裸渲染正文。
    return (
      <HistoryContextMessageCard
        labelId={labelId}
        message={message}
        metaId={metaId}
        onPreviewFilePath={onPreviewFilePath}
        onSelectArtifact={onSelectArtifact}
        relatedEvidence={relatedEvidence}
        statusId={statusId}
        streamingMessageId={streamingMessageId}
      />
    );
  }

  return (
    <div
      className="flex w-full min-w-0 flex-col gap-1"
      data-chat-anchor-key={message.id}
    >
      <span className="sr-only" id={labelId}>
        {t("panels.messages.toolReceipt.title")}
      </span>
      <span className="sr-only" id={metaId}>
        {message.label}
      </span>
      <div className="flex w-full min-w-0 flex-col gap-1" id={statusId}>
        {nodes.map((node) => {
          const row = renderMessageSegment(node.segment, {
            anchorKey: message.id,
            flowKey: `tool-call:${node.key}`,
            interrupted: message.interrupted === true,
            streaming: false,
            onSelectArtifact,
            resolveFilePathLink,
          });
          // §12.1.4：空行节点不落 DOM——空包裹 div 会吃掉父级 gap，撑出空行。
          return row ? <div key={node.key}>{row}</div> : null;
        })}
      </div>
      {renderRelatedArtifactSection(relatedEvidence, onSelectArtifact)}
    </div>
  );
}
