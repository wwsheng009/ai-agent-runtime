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
