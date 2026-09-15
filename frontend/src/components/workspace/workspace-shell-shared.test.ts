import { describe, expect, it } from "vitest";

import type { Thread } from "@/data/mock";
import {
  getCommandStateLabel,
  getThreadStatusLabel,
  getThreadTransportKind,
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

  it("maps thread transport to the icon/tone kind used by the topbar", () => {
    expect(getThreadTransportKind(createThread({ transport: "live" }))).toBe(
      "live",
    );
    expect(getThreadTransportKind(createThread({ transport: "error" }))).toBe(
      "error",
    );
    // 未标注 / mock 一律按「预置预览」呈现，与文本标签同一口径。
    expect(getThreadTransportKind(createThread({ transport: "mock" }))).toBe(
      "seeded",
    );
    expect(getThreadTransportKind(createThread())).toBe("seeded");
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
      // 传输状态已由顶栏图标承载，副标题只留来源，避免同一状态在顶栏出现两次。
    ).toBe("via default");

    expect(
      getThreadTopbarSubtitle(
        createThread({
          sessionId: "session-restore",
          transport: "error",
        }),
        "Runtime degraded",
      ),
    ).toBe("Session session-restore needs restore attention");

    // 无会话的预览线程没有来源可讲，退回传输名兜底。
    expect(
      getThreadTopbarSubtitle(createThread({ transport: "mock" }), "Seeded preview"),
    ).toBe("Seeded preview");
  });
});
