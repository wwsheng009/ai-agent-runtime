// 批次 A2/C3/F3（§8.3/§8.4/§8.5）：助手回合 = 无外壳的扁平行序列。
// - A2：删除卡片外壳（圆角 / 边框 / 渐变 / 投影）、头像 chip、作者行、竖直渐变线；
// - F3：回合折叠由 turn-process 统计行承担，收起态过程行不渲染；
// - C3：卡内 token 用量文本行删除，改由 turn-tail 行承担（默认 hover 显现）。
// 行序列来自 E1 的扁平 flow 投影（`projectMessageFlow`），每行是同级兄弟。

import { Fragment, useState } from "react";
import { useTranslation } from "react-i18next";

import { projectChatView, projectMessageFlow } from "@/lib/chat-view";
import { type BranchUnavailableReasonKey } from "@/lib/chat-view/branch-availability";
import { createArtifactFilePathLinkResolver } from "@/lib/tool-row/artifact-links";
import { type Artifact, type ChatMessage } from "@/data/mock";

import { FlowFallbackRow } from "./flow-fallback-row";
import {
  renderMessageSegment,
  renderRelatedArtifactSection,
} from "./segment-rendering";
import { TurnProcessRow } from "./turn-process-row";
import { TurnTailRow } from "./turn-tail-row";

type AssistantMessageCardProps = {
  /** 批次 2（§5.4）：分支不可用原因 i18n 键（宿主在整条 flow 上求解后下发）。 */
  branchDisabledReason?: BranchUnavailableReasonKey;
  /** 批次 2（§5.4）：本回合的分支请求在途。 */
  branchPending?: boolean;
  /** 批次 2（§5.4）：本回合是否是唯一可分支锚点。 */
  canBranch?: boolean;
  labelId: string;
  message: ChatMessage;
  metaId: string;
  /** 批次 2（§5.4）：分支入口（宿主提供才渲染）。 */
  onBranch?: () => void;
  /** P2-1A：关联产物未命中时的运行时文件预览兜底。 */
  onPreviewFilePath?: (path: string) => void;
  /** 回合级重试入口（宿主提供才渲染）。 */
  onRetry?: () => void;
  onSelectArtifact: (artifactId: string) => void;
  relatedEvidence: Artifact[];
  statusId: string;
  streamingMessageId: string | null;
};

export function AssistantMessageCard({
  branchDisabledReason,
  branchPending,
  canBranch,
  labelId,
  message,
  metaId,
  onBranch,
  onPreviewFilePath,
  onRetry,
  onSelectArtifact,
  relatedEvidence,
  statusId,
  streamingMessageId,
}: AssistantMessageCardProps) {
  const { t } = useTranslation("workspace");
  const [processExpanded, setProcessExpanded] = useState(false);
  const resolveFilePathLink = createArtifactFilePathLinkResolver(
    relatedEvidence,
    onSelectArtifact,
    onPreviewFilePath,
  );
  const view = projectChatView(message, {
    streaming: message.id === streamingMessageId,
  });
  const items = projectMessageFlow(message, {
    expandedMessageIds: processExpanded ? [message.id] : [],
    streamingMessageId,
  });
  const streaming = message.id === streamingMessageId;

  return (
    <div className="flex w-full min-w-0 flex-col gap-2">
      <span className="sr-only" id={labelId}>
        {message.author}
      </span>
      <span className="sr-only" id={metaId}>
        {message.label}
      </span>
      <div className="flex min-w-0 flex-col gap-2" id={statusId}>
        {items.map((item) => {
          if (item.kind === "turn-process") {
            return view.summary ? (
              <TurnProcessRow
                anchorKey={item.anchorKey}
                expanded={processExpanded}
                flowKey={item.key}
                key={item.key}
                onToggle={() => setProcessExpanded((value) => !value)}
                summary={view.summary}
              />
            ) : null;
          }

          if (item.kind === "turn-tail") {
            return (
              <TurnTailRow
                anchorKey={item.anchorKey}
                branchDisabledReason={branchDisabledReason}
                branchPending={branchPending}
                canBranch={canBranch}
                flowKey={item.key}
                key={item.key}
                message={message}
                onBranch={onBranch}
                onRetry={onRetry}
                usage={view.usage}
              />
            );
          }

          if (item.kind === "fallback") {
            return (
              <FlowFallbackRow
                anchorKey={item.anchorKey}
                flowKey={item.key}
                key={item.key}
                raw={item.raw}
              />
            );
          }

          // assistant-step / tool-call / notice：统一交给 segment 渲染器，
          // 行锚点由具体行控件落在自身根节点上（e2e 可定位任意过程行）。
          if (
            item.kind === "assistant-step" ||
            item.kind === "tool-call" ||
            item.kind === "notice"
          ) {
            return (
              <Fragment key={item.key}>
                {renderMessageSegment(item.node.segment, {
                  anchorKey: item.anchorKey,
                  flowKey: item.key,
                  interrupted: message.interrupted === true,
                  streaming,
                  onSelectArtifact,
                  resolveFilePathLink,
                })}
              </Fragment>
            );
          }

          return null;
        })}

        {streaming ? (
          <span className="sr-only">
            {t("panels.messages.messageCard.streamingBadge")}
          </span>
        ) : null}
      </div>

      {renderRelatedArtifactSection(relatedEvidence, onSelectArtifact)}
    </div>
  );
}
