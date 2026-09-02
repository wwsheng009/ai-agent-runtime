import { describe, expect, it } from "vitest";

import type { Thread } from "@/data/mock";
import {
  applyRuntimeDeltaToThread,
  applySessionHistoryToThread,
  buildAssistantMessageSegments,
  buildGeneratedImagePlaceholderSegment,
  buildStreamingMessageSegments,
  mergeRuntimeSessionsIntoThreads,
  mergeRuntimeEvent,
  upsertGeneratedImageSegment,
} from "@/lib/workspace-thread-state";
import type {
  RuntimeSessionRecord,
  SessionHistoryResponse,
  SessionRuntimeEvent,
} from "@/types/runtime";

function createThread(): Thread {
  return {
    id: "thread-1",
    title: "Thread",
    summary: "Summary",
    updatedAt: "2026-03-31T00:00:00Z",
    status: "active",
    tags: [],
    prompts: [],
    artifacts: [],
    messages: [
      {
        id: "assistant-existing",
        role: "assistant",
        author: "Runtime stream",
        label: "streaming",
        segments: [
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
        ],
      },
    ],
  };
}

describe("thread-runtime", () => {
  it("preserves existing code segments when authoritative history matches a message", () => {
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 1,
      history: [
        {
          role: "assistant",
          content: "Merged answer",
        },
      ],
    };

    const nextThread = applySessionHistoryToThread(createThread(), response);

    expect(nextThread.sessionId).toBe("session-1");
    expect(nextThread.messages).toHaveLength(1);
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
    ]);
    expect(nextThread.artifacts[0]?.id).toBe("session-history-session-1");
  });

  it("uses durable message_id from history metadata as ChatMessage.id", () => {
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 2,
      history: [
        {
          role: "user",
          content: "rewrite this",
          metadata: {
            message_id: "msg_user_1",
            turn_id: "turn_1",
          },
        },
        {
          role: "assistant",
          content: "done",
          metadata: {
            message_id: "msg_assistant_1",
            turn_id: "turn_1",
          },
        },
      ],
    };

    const nextThread = applySessionHistoryToThread(createThread(), response);

    expect(nextThread.messages).toHaveLength(2);
    expect(nextThread.messages[0].id).toBe("msg_user_1");
    expect(nextThread.messages[0].role).toBe("user");
    expect(nextThread.messages[1].id).toBe("msg_assistant_1");
  });

  it("keeps existing streaming messages when session history is null", () => {
    const response = {
      session_id: "session-1",
      count: 0,
      history: null,
    } as unknown as SessionHistoryResponse;

    const nextThread = applySessionHistoryToThread(createThread(), response);

    expect(nextThread.sessionId).toBe("session-1");
    // null history = 无权威历史：保留当前流式消息（与 history 匹配时的
    // 合并语义一致，避免恢复流程清掉正在渲染的内容）。
    expect(nextThread.messages).toHaveLength(1);
    expect(nextThread.messages[0]?.id).toBe("assistant-existing");
    expect(nextThread.artifacts[0]?.id).toBe("session-history-session-1");
  });

  it("hides fact ledger and other internal prompt-context messages", () => {
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 4,
      history: [
        {
          role: "user",
          content: "continue",
        },
        {
          role: "developer",
          content:
            "Verified fact ledger (authoritative over compacted prose):\n- [execution] shell succeeded",
          metadata: {
            context_stage: "fact_ledger",
            context_snapshot: true,
          },
        },
        {
          role: "assistant",
          content:
            "Verified fact ledger (authoritative over compacted prose):\n- legacy assistant leak",
        },
        {
          role: "assistant",
          content: "real answer",
        },
      ],
    };

    const nextThread = applySessionHistoryToThread(createThread(), response);

    expect(nextThread.messages).toHaveLength(2);
    expect(nextThread.messages[0].role).toBe("user");
    expect(nextThread.messages[0].segments).toEqual([
      { type: "text", content: "continue" },
    ]);
    expect(nextThread.messages[1].role).toBe("assistant");
    expect(nextThread.messages[1].segments).toEqual([
      { type: "text", content: "real answer" },
    ]);
  });

  it("restores persisted related evidence artifacts from session history metadata", () => {
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 1,
      history: [
        {
          role: "assistant",
          content: "Recovered answer",
          metadata: {
            workspace_related_artifacts: [
              {
                id: "persisted-agent-chat-response",
                name: "agent-chat-response-agent-route.json",
                path: "runtime/agent-chat-response-agent-route.json",
                summary: "Final response payload persisted with the assistant history.",
                kind: "json",
                language: "json",
                content: {
                  source: "agent_route",
                  kind: "agent",
                  status: "completed",
                },
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

    expect(nextThread.messages).toHaveLength(1);
    expect(nextThread.messages[0].relatedArtifactIds).toEqual([
      "persisted-history:session-1:0:0:agent-chat-response-agent-route-json",
    ]);
    expect(nextThread.artifacts).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          id: "persisted-history:session-1:0:0:agent-chat-response-agent-route-json",
          name: "agent-chat-response-agent-route.json",
          path: "runtime/agent-chat-response-agent-route.json",
          summary: "Final response payload persisted with the assistant history.",
          kind: "json",
          language: "json",
          content: JSON.stringify(
            {
              source: "agent_route",
              kind: "agent",
              status: "completed",
            },
            null,
            2,
          ),
        }),
      ]),
    );
  });

  it("restores generated images from assistant metadata into inline segments and artifacts", () => {
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 1,
      history: [
        {
          role: "assistant",
          content: "Generated image",
          metadata: {
            generated_images: [
              {
                id: "image:1",
                status: "completed",
                revised_prompt: "a tiny robot",
                mime_type: "image/png",
                saved_path: "C:/temp/image_1.png",
                sha256: "abc123",
                byte_count: 42,
              },
            ],
            generated_images_error: "image save warning",
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

    expect(nextThread.messages).toHaveLength(1);
    expect(nextThread.messages[0].relatedArtifactIds).toEqual([
      "generated-image:session-1:image_1",
    ]);
    expect(nextThread.messages[0].segments).toEqual([
      {
        type: "text",
        content: "Generated image",
      },
      {
        type: "image",
        src: expect.stringContaining(
          "/api/runtime/sessions/session-1/generated-images/image_1",
        ),
        alt: "a tiny robot",
        caption: "a tiny robot",
        artifactId: "generated-image:session-1:image_1",
        imageId: "image_1",
      },
      {
        type: "callout",
        title: "图片保存失败",
        tone: "warning",
        content: "image save warning",
      },
    ]);

    expect(nextThread.artifacts).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          id: "generated-image:session-1:image_1",
          kind: "image",
          name: "image_1.png",
          path: "runtime/generated-images/image_1.png",
          content: expect.stringContaining(
            "/api/runtime/sessions/session-1/generated-images/image_1",
          ),
          mimeType: "image/png",
          sha256: "abc123",
          byteCount: 42,
          revisedPrompt: "a tiny robot",
        }),
      ]),
    );
  });

  it("restores reasoning from assistant metadata into the history segment", () => {
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 1,
      history: [
        {
          role: "assistant",
          content: "Final answer",
          metadata: {
            reasoning_details: {
              provider: "deepseek",
              visibility: "summary",
              summary: "Because the flag was unset",
            },
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

    expect(nextThread.messages).toHaveLength(1);
    expect(nextThread.messages[0].segments).toEqual([
      {
        type: "text",
        content: "Final answer",
      },
      {
        type: "reasoning",
        content: "Because the flag was unset",
      },
    ]);
  });

  it("prefers summary over full content when both exist (backend DisplayText parity)", () => {
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 1,
      history: [
        {
          role: "assistant",
          content: "Final answer",
          metadata: {
            reasoning_details: {
              provider: "openai",
              visibility: "full",
              summary: "Short summary",
              content: "Longer detailed reasoning body",
            },
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

    const segments = nextThread.messages[0].segments;
    expect(segments).toHaveLength(2);
    expect(segments[1]).toEqual({
      type: "reasoning",
      content: "Short summary",
    });
  });

  it("does not restore reasoning when visibility is none or opaque", () => {
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 1,
      history: [
        {
          role: "assistant",
          content: "Final answer",
          metadata: {
            reasoning_details: {
              provider: "openai",
              visibility: "none",
              content: "hidden reasoning",
            },
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

    expect(nextThread.messages[0].segments).toEqual([
      {
        type: "text",
        content: "Final answer",
      },
    ]);
  });

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

  it("deduplicates runtime events and keeps the newest 100 entries", () => {
    const seed: SessionRuntimeEvent[] = Array.from({ length: 100 }, (_, index) => ({
      type: "runtime.step",
      timestamp: `2026-03-31T00:00:${String(index).padStart(2, "0")}Z`,
      payload: {
        seq: index + 1,
      },
    }));

    const duplicate = seed[99];
    const deduped = mergeRuntimeEvent(seed, duplicate);
    expect(deduped).toHaveLength(100);
    expect(deduped).toBe(seed);

    const next = mergeRuntimeEvent(seed, {
      type: "runtime.step",
      timestamp: "2026-03-31T00:01:40Z",
      payload: {
        seq: 101,
      },
    });

    expect(next).toHaveLength(100);
    expect(next[0].payload?.seq).toBe(2);
    expect(next[99].payload?.seq).toBe(101);
  });

  it("preserves existing thread identity while attaching a restored runtime session", () => {
    const thread = createThread();
    const sessions: RuntimeSessionRecord[] = [
      {
        id: "session-1",
        state: "active",
        metadata: {
          title: "Recovered thread",
          summary: "Loaded from runtime session list.",
          lastSkill: "workspace",
        },
        updatedAt: "2026-03-31T10:10:00Z",
      },
    ];

    const nextThreads = mergeRuntimeSessionsIntoThreads(
      [{ ...thread, id: "thread-local", sessionId: "session-1" }],
      sessions,
    );

    expect(nextThreads).toHaveLength(1);
    expect(nextThreads[0]).toMatchObject({
      id: "thread-local",
      sessionId: "session-1",
      title: "Recovered thread",
      summary: "Loaded from runtime session list.",
      runtimeSource: "workspace",
    });
  });

  it("adds a stopped callout while preserving partial output and reasoning", () => {
    const segments = buildStreamingMessageSegments(
      "Partial answer",
      "runtime",
      "Need a follow-up step",
      {
        status: "stopped",
      },
    );

    expect(segments).toEqual([
      {
        type: "text",
        content: "Partial answer",
      },
      {
        type: "reasoning",
        content: "Need a follow-up step",
        running: false,
      },
      {
        type: "callout",
        title: "Response stopped",
        tone: "warning",
        content:
          "Generation was stopped locally. Partial output is preserved so the next turn can continue from this point.",
      },
    ]);
  });
});

describe("applyRuntimeDeltaToThread", () => {
  function deltaEvent(type: string, payload: Record<string, unknown>): SessionRuntimeEvent {
    return { type, timestamp: "2026-08-30T00:00:00Z", payload };
  }

  it("appends assistant_delta text to the latest assistant message", () => {
    const nextThread = applyRuntimeDeltaToThread(
      createThread(),
      deltaEvent("assistant_delta", { delta: " world", stream_id: "stream-1", sequence: 2 }),
    );

    const textSegment = nextThread.messages[0].segments.find((s) => s.type === "text");
    expect(textSegment?.type === "text" ? textSegment.content : "").toBe("Merged answer world");
  });

  it("replaces the streaming placeholder on first delta", () => {
    const thread = createThread();
    thread.messages[0].segments = [{ type: "text", content: "..." }];

    const nextThread = applyRuntimeDeltaToThread(
      thread,
      deltaEvent("assistant_delta", { content: "Hello", sequence: 1 }),
    );

    const textSegment = nextThread.messages[0].segments.find((s) => s.type === "text");
    expect(textSegment?.type === "text" ? textSegment.content : "").toBe("Hello");
  });

  it("appends assistant_reasoning delta into the reasoning segment", () => {
    const thread = createThread();
    thread.messages[0].segments.push({ type: "reasoning", content: "think", running: false });

    const nextThread = applyRuntimeDeltaToThread(
      thread,
      deltaEvent("assistant_reasoning", { reasoning: { summary: " harder" } }),
    );

    const reasoningSegment = nextThread.messages[0].segments.find((s) => s.type === "reasoning");
    expect(reasoningSegment?.type === "reasoning" ? reasoningSegment.content : "").toBe("think\n harder");
    expect(reasoningSegment?.type === "reasoning" ? reasoningSegment.running : false).toBe(true);
  });

  it("creates a reasoning segment when absent", () => {
    const nextThread = applyRuntimeDeltaToThread(
      createThread(),
      deltaEvent("assistant_reasoning", { reasoning: { summary: "cold start" } }),
    );

    const reasoningSegment = nextThread.messages[0].segments.find((s) => s.type === "reasoning");
    expect(reasoningSegment?.type === "reasoning" ? reasoningSegment.content : "").toBe("cold start");
  });

  it("ignores deltas when no assistant message exists", () => {
    const thread = createThread();
    thread.messages = [{ id: "user-1", role: "user", author: "me", label: "", segments: [{ type: "text", content: "hi" }] }];

    const nextThread = applyRuntimeDeltaToThread(
      thread,
      deltaEvent("assistant_delta", { delta: "ignored" }),
    );
    const textSegments = nextThread.messages[0].segments.filter((s) => s.type === "text");
    expect(textSegments).toHaveLength(1);
    expect(textSegments[0].type === "text" ? textSegments[0].content : "").toBe("hi");
  });

  it("keeps non-delta events untouched", () => {
    const thread = createThread();
    const nextThread = applyRuntimeDeltaToThread(
      thread,
      deltaEvent("session_start", { status: "running" }),
    );
    expect(nextThread).toBe(thread);
    const textSegment = nextThread.messages[0].segments[0];
    expect(textSegment.type === "text" ? textSegment.content : "").toBe("Merged answer");
  });
});
