// 由 components/workspace/message-list.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
// 渲染辅助模块：只导出普通函数（非组件），组件实现见 ./segment-components。

import { Suspense } from "react";

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
    interrupted?: boolean;
    streaming?: boolean;
    onSelectArtifact?: (artifactId: string) => void;
  },
) {
  if (segment.type === "text") {
    return (
      <StreamingMarkdown
        content={segment.content}
        interrupted={options?.interrupted}
        streaming={options?.streaming}
      />
    );
  }

  if (segment.type === "reasoning") {
    return (
      <MessageReasoningRow
        segment={segment}
        streaming={options?.streaming}
      />
    );
  }

  if (segment.type === "tool") {
    return <MessageToolRow segment={segment} />;
  }

  return (
    <Suspense fallback={<MessageSegmentFallback segment={segment} />}>
      <MessageRichSegment
        onSelectArtifact={options?.onSelectArtifact}
        segment={segment}
      />
    </Suspense>
  );
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
