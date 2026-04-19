import { describe, expect, it } from "vitest";

import type { Thread } from "@/data/mock";
import {
  getCommandStateLabel,
  getThreadStatusLabel,
  getThreadTopbarSubtitle,
  getThreadTransportLabel,
} from "@/components/workspace/workspace-shell-shared";

function createThread(overrides: Partial<Thread> = {}): Thread {
  return {
    id: "thread-1",
    title: "Thread 1",
    summary: "Summary",
    updatedAt: "2026-03-31T10:20:00Z",
    status: "active",
    tags: [],
    prompts: [],
    messages: [],
    artifacts: [],
    ...overrides,
  };
}

describe("workspace shell shared helpers", () => {
  it("derives transport labels from thread transport", () => {
    expect(getThreadTransportLabel(createThread({ transport: "live" }))).toBe(
      "Live runtime",
    );
    expect(getThreadTransportLabel(createThread({ transport: "error" }))).toBe(
      "Runtime degraded",
    );
    expect(getThreadTransportLabel(createThread({ transport: "mock" }))).toBe(
      "Seeded preview",
    );
  });

  it("derives command state labels from response state and session context", () => {
    expect(getCommandStateLabel(createThread(), true)).toBe("Runtime stream active");
    expect(
      getCommandStateLabel(createThread({ sessionId: "session-1" }), false),
    ).toBe("Ready for the next turn");
    expect(
      getCommandStateLabel(
        createThread({
          messages: [
            {
              id: "m1",
              role: "user",
              author: "You",
              label: "draft",
              segments: [{ type: "text", content: "hello" }],
            },
          ],
        }),
        false,
      ),
    ).toBe("Ready to start runtime session");
    expect(getCommandStateLabel(createThread(), false)).toBe(
      "Ready to start a new session",
    );
  });

  it("derives thread status labels from session presence and content", () => {
    expect(getThreadStatusLabel(createThread({ sessionId: "session-1" }))).toBe(
      "Session attached",
    );
    expect(
      getThreadStatusLabel(
        createThread({
          messages: [
            {
              id: "m1",
              role: "assistant",
              author: "Runtime",
              label: "preview",
              segments: [{ type: "text", content: "preview" }],
            },
          ],
        }),
      ),
    ).toBe("Preview thread");
    expect(getThreadStatusLabel(createThread())).toBe("New thread");
  });

  it("uses compact runtime metadata for the topbar subtitle instead of thread summary", () => {
    expect(
      getThreadTopbarSubtitle(
        createThread({
          sessionId: "session-1",
          runtimeSource: "default",
          summary: "Long assistant summary should not appear in the topbar.",
          transport: "live",
        }),
        "Live runtime",
      ),
    ).toBe("Live runtime via default");

    expect(
      getThreadTopbarSubtitle(
        createThread({
          sessionId: "session-restore",
          transport: "error",
        }),
        "Runtime degraded",
      ),
    ).toBe("Session session-restore needs restore attention");
  });
});
