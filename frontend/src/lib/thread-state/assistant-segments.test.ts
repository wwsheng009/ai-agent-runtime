// P0-2 随源拆分：由 workspace-thread-state.test.ts 按关注点切分，断言未改动。
import { describe, expect, it } from "vitest";

import {
  applyRuntimeDeltaToThread,
  applySessionHistoryToThread,
  buildAssistantMessageSegments,
  buildGeneratedImagePlaceholderSegment,
  upsertGeneratedImageSegment,
} from "@/lib/workspace-thread-state";
import type { SessionHistoryResponse, SessionRuntimeEvent } from "@/types/runtime";
import { createThread } from "./test-fixtures";

describe("assistant segments and generated images", () => {
  it("merges assistant image progress into the live assistant message", () => {
    const event: SessionRuntimeEvent = {
      type: "assistant.image_progress",
      timestamp: "2026-03-31T00:00:10Z",
      payload: {
        trace_id: "trace-1",
        step: 1,
        image: {
          phase: "partial",
          image_id: "image:1",
          sanitized_id: "image_1",
          progress: 0.5,
          revised_prompt: "a tiny robot",
        },
      },
    };

    const nextThread = applyRuntimeDeltaToThread(
      createThread(),
      event,
    );

    expect(nextThread.runtimeEventCount).toBeUndefined();
    expect(nextThread.messages[0].segments).toEqual([
      {
        type: "text",
        content: "Merged answer",
      },
      {
        type: "code",
        language: "json",
        title: "Reasoning snapshot",
        code: '{"ok":true}',
      },
      {
        type: "image-placeholder",
        imageId: "image_1",
        phase: "partial",
        progress: 0.5,
        caption: "a tiny robot",
      },
    ]);
  });

  // 跨通道不变量：桥接帧（chat.sse.tool_*）写进消息段的工具行，在直连通道定稿
  // 重建（buildAssistantMessageSegments + existingSegments）时必须原样留下，
  // 否则回合结束时工具行会被整段覆盖掉；正文段则排在工具行之后（时间顺序：
  // 推理 → 工具 → 答复）。
  it("定稿重建保留桥接写入的工具行", () => {
    const tool = {
      type: "tool" as const,
      toolCallId: "observation_step_1_tool_0",
      name: "shell",
      status: "finished" as const,
      argsSummary: "go test ./...",
      resultSummary: "Exit code: 0",
    };

    const segments = buildAssistantMessageSegments(
      "最终答复",
      "runtime",
      "先跑测试",
      { reasoningRunning: false, existingSegments: [tool] },
    );

    expect(segments.map((segment) => segment.type)).toEqual([
      "reasoning",
      "tool",
      "text",
    ]);
    expect(segments.filter((segment) => segment.type === "tool")).toEqual([tool]);
  });

  it("正文一次落位在工具行之后，后续 flush 不跳回顶部", () => {
    const tool = {
      type: "tool" as const,
      toolCallId: "observation_step_1_tool_0",
      name: "shell",
      status: "finished" as const,
    };

    const first = buildAssistantMessageSegments("第一段答复", "runtime", "先跑测试", {
      existingSegments: [tool],
    });
    expect(first.map((segment) => segment.type)).toEqual([
      "reasoning",
      "tool",
      "text",
    ]);

    // 第二次 flush：正文段已存在，必须在原位续写——旧实现把它放回数组首位，
    // 于是最终答复一开始流式就跳到工具行上方。
    const second = buildAssistantMessageSegments("第一段答复续写", "runtime", "先跑测试", {
      existingSegments: first,
    });
    expect(second.map((segment) => segment.type)).toEqual([
      "reasoning",
      "tool",
      "text",
    ]);
    const textSegment = second.find((segment) => segment.type === "text");
    expect(
      textSegment && textSegment.type === "text" ? textSegment.content : "",
    ).toBe("第一段答复续写");
  });

  it("replaces image placeholders with generated images when building final assistant segments", () => {
    const placeholder = buildGeneratedImagePlaceholderSegment({
      phase: "partial",
      image_id: "image:1",
      sanitized_id: "image_1",
      progress: 0.5,
      revised_prompt: "a tiny robot",
    });

    expect(placeholder).not.toBeNull();

    const segments = buildAssistantMessageSegments(
      "Merged answer",
      "runtime",
      "Need a follow-up step",
      {
        existingSegments: [
          {
            type: "text",
            content: "Merged answer",
          },
          placeholder!,
        ],
        generatedImages: {
          artifacts: [],
          segments: [
            {
              type: "image",
              src: "/api/runtime/sessions/session-1/generated-images/image_1",
              alt: "a tiny robot",
              caption: "a tiny robot",
              artifactId: "generated-image:session-1:image_1",
              imageId: "image_1",
            },
          ],
        },
      },
    );

    expect(segments).toEqual([
      {
        type: "text",
        content: "Merged answer",
      },
      {
        type: "reasoning",
        content: "Need a follow-up step",
        running: false,
      },
      {
        type: "image",
        src: "/api/runtime/sessions/session-1/generated-images/image_1",
        alt: "a tiny robot",
        caption: "a tiny robot",
        artifactId: "generated-image:session-1:image_1",
        imageId: "image_1",
      },
    ]);
  });

  it("keeps a final image when a stale placeholder update arrives later", () => {
    const finalSegments = upsertGeneratedImageSegment(
      [
        {
          type: "text",
          content: "Merged answer",
        },
        {
          type: "image",
          src: "/api/runtime/sessions/session-1/generated-images/image_1",
          alt: "a tiny robot",
          caption: "a tiny robot",
          artifactId: "generated-image:session-1:image_1",
          imageId: "image_1",
        },
      ],
      {
        type: "image-placeholder",
        imageId: "image_1",
        phase: "partial",
        progress: 0.2,
      },
    );

    expect(finalSegments).toEqual([
      {
        type: "text",
        content: "Merged answer",
      },
      {
        type: "image",
        src: "/api/runtime/sessions/session-1/generated-images/image_1",
        alt: "a tiny robot",
        caption: "a tiny robot",
        artifactId: "generated-image:session-1:image_1",
        imageId: "image_1",
      },
    ]);
  });

  it("preserves image artifacts recovered from persisted history metadata", () => {
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 1,
      history: [
        {
          role: "assistant",
          content: "Recovered image",
          metadata: {
            workspace_related_artifacts: [
              {
                name: "figure.png",
                path: "runtime/figure.png",
                kind: "image",
                content: "https://example.com/figure.png",
                mime_type: "image/png",
                revised_prompt: "reference figure",
                sha256: "hash-123",
                byte_count: 77,
              },
            ],
          },
        },
      ],
    };

    const nextThread = applySessionHistoryToThread(
      {
        ...createThread(),
        messages: [],
        artifacts: [],
      },
      response,
    );

    const imageArtifact = nextThread.artifacts.find(
      (artifact) => artifact.kind === "image",
    );

    expect(imageArtifact).toEqual(
      expect.objectContaining({
        kind: "image",
        name: "figure.png",
        path: "runtime/figure.png",
        content: "https://example.com/figure.png",
        mimeType: "image/png",
        revisedPrompt: "reference figure",
        sha256: "hash-123",
        byteCount: 77,
      }),
    );
    expect(nextThread.messages[0].relatedArtifactIds).toEqual([
      "persisted-history:session-1:0:0:figure-png",
    ]);
  });

});
