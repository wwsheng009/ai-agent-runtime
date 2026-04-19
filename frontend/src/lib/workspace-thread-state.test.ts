import { describe, expect, it } from "vitest";

import type { Thread } from "@/data/mock";
import {
  applySessionHistoryToThread,
  buildStreamingMessageSegments,
  mergeRuntimeSessionsIntoThreads,
  mergeRuntimeEvent,
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

  it("treats null session history as an empty message list", () => {
    const response = {
      session_id: "session-1",
      count: 0,
      history: null,
    } as unknown as SessionHistoryResponse;

    const nextThread = applySessionHistoryToThread(createThread(), response);

    expect(nextThread.sessionId).toBe("session-1");
    expect(nextThread.messages).toEqual([]);
    expect(nextThread.artifacts[0]?.id).toBe("session-history-session-1");
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
        type: "code",
        language: "json",
        title: "Reasoning snapshot",
        code: JSON.stringify(
          {
            source: "runtime",
            reasoning: "Need a follow-up step",
          },
          null,
          2,
        ),
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
