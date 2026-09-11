// P0-2 随源拆分：由 workspace-thread-state.test.ts 按关注点切分，断言未改动。
import { describe, expect, it } from "vitest";

import {
  buildStreamingMessageSegments,
  mergeRuntimeSessionsIntoThreads,
  mergeRuntimeEvent,
} from "@/lib/workspace-thread-state";
import type { RuntimeSessionRecord, SessionRuntimeEvent } from "@/types/runtime";
import { createThread } from "./test-fixtures";

describe("runtime events and session merge", () => {
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
