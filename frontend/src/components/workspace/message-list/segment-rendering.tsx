// 由 components/workspace/message-list.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
// 渲染辅助模块：只导出普通函数（非组件），组件实现见 ./segment-components。
// E1：新增 flow 锚点透传与未知类型的 `fallback` 降级（§8.4 规则 9）。

import { Suspense } from "react";

import { FlowFallbackRow } from "./flow-fallback-row";
import { segmentFlowKind } from "@/lib/chat-view";
import { hasVisibleText } from "@/lib/chat-view/visible-text";
import { MessageReasoningRow } from "@/components/workspace/message-reasoning-row";
import { MessageToolRow } from "@/components/workspace/message-tool-row";
import { type Artifact, type MessageSegment } from "@/data/mock";

import {
  MessageRelatedArtifacts,
  MessageRichSegment,
  MessageSegmentFallback,
  RelatedArtifactsFallback,
  StreamingMarkdown,
} from "./segment-components";

export function renderMessageSegment(
  segment: MessageSegment,
  options?: {
    anchorKey?: string;
    flowKey?: string;
    interrupted?: boolean;
    /** live 通道键：只由「流式消息的最后一段正文/推理」这一行携带消息 id。 */
    liveStreamId?: string | null;
    streaming?: boolean;
    onSelectArtifact?: (artifactId: string) => void;
    resolveFilePathLink?: (path: string) => (() => void) | null;
  },
) {
  // 协议演进时可能出现本地未识别的 segment 类型：可读降级，不得白屏、不得抛错。
  if (!isRenderableSegment(segment)) {
    return (
      <FlowFallbackRow
        anchorKey={options?.anchorKey}
        flowKey={options?.flowKey}
        raw={segment}
      />
    );
  }

  // 文段 / 富段没有自带锚点的行控件：由本层包一层行根节点承载 flow 锚点（§8.4 规则 4）。
  const anchors = {
    "data-chat-anchor-key": options?.anchorKey,
    "data-chat-flow-key": options?.flowKey,
    "data-chat-flow-kind": segmentFlowKind(segment),
  };

  if (segment.type === "text") {
    // §12.1.4：空文本段不生成行节点——空 flex item 高度为 0，但仍会吃掉父级 gap
    // （相邻行各多一条 8px 空行），因此必须在渲染层直接不产出节点。
    if (!hasVisibleText(segment.content)) {
      return null;
    }
    return (
      <div className="min-w-0" {...anchors}>
        <StreamingMarkdown
          content={segment.content}
          interrupted={options?.interrupted}
          liveStreamId={options?.liveStreamId}
          streaming={options?.streaming}
        />
      </div>
    );
  }

  if (segment.type === "reasoning") {
    return (
      <MessageReasoningRow
        anchorKey={options?.anchorKey}
        flowKey={options?.flowKey}
        liveStreamId={options?.liveStreamId}
        segment={segment}
        streaming={options?.streaming}
      />
    );
  }

  if (segment.type === "tool") {
    return (
      <MessageToolRow
        anchorKey={options?.anchorKey}
        flowKey={options?.flowKey}
        resolveFilePathLink={options?.resolveFilePathLink}
        segment={segment}
      />
    );
  }

  return (
    <div className="min-w-0" {...anchors}>
      <Suspense fallback={<MessageSegmentFallback segment={segment} />}>
        <MessageRichSegment
          onSelectArtifact={options?.onSelectArtifact}
          segment={segment}
        />
      </Suspense>
    </div>
  );
}

/** 未知类型探测（与 `lib/chat-view` 的 `KNOWN_SEGMENT_TYPES` 同源，避免两处判定漂移）。 */
function isRenderableSegment(segment: MessageSegment): boolean {
  return segmentFlowKind(segment) !== "fallback";
}

export function renderRelatedArtifactSection(
  relatedEvidence: Artifact[],
  onSelectArtifact: (artifactId: string) => void,
) {
  if (relatedEvidence.length === 0) {
    return null;
  }

  return (
    <Suspense fallback={<RelatedArtifactsFallback count={relatedEvidence.length} />}>
      <MessageRelatedArtifacts
        onSelectArtifact={onSelectArtifact}
        relatedArtifacts={relatedEvidence}
      />
    </Suspense>
  );
}
