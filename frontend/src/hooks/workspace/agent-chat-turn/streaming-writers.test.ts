import { describe, expect, it } from "vitest";

import { type ChatMessage } from "@/data/mock";

import { appendStreamErrorNoticeToFinalizedMessage } from "./streaming-writers";

function finalizedMessage(overrides: Partial<ChatMessage> = {}): ChatMessage {
  return {
    id: "turn-1-assistant",
    role: "assistant",
    author: "Runtime assistant",
    label: "runtime",
    streaming: false,
    segments: [{ type: "text", content: "ok" }],
    ...overrides,
  } as ChatMessage;
}

describe("appendStreamErrorNoticeToFinalizedMessage", () => {
  it("keeps the finalized reply and appends a warning callout", () => {
    const next = appendStreamErrorNoticeToFinalizedMessage(
      finalizedMessage(),
      "Runtime stream failed.",
      "连接超时",
    );

    expect(next?.segments[0]).toEqual({ type: "text", content: "ok" });
    expect(next?.segments[1]).toMatchObject({
      type: "callout",
      tone: "warning",
      title: "Runtime stream failed.",
      content: "连接超时",
    });
    expect(next?.streaming).toBe(false);
  });

  it("falls back to null while the message is still streaming", () => {
    expect(
      appendStreamErrorNoticeToFinalizedMessage(
        finalizedMessage({ streaming: true }),
        "Runtime stream failed.",
        "连接超时",
      ),
    ).toBeNull();
  });

  it("falls back to null when there is no rendered content to preserve", () => {
    expect(
      appendStreamErrorNoticeToFinalizedMessage(
        finalizedMessage({ segments: [] }),
        "Runtime stream failed.",
        "连接超时",
      ),
    ).toBeNull();
  });

  it("does not append the same notice twice", () => {
    const first = appendStreamErrorNoticeToFinalizedMessage(
      finalizedMessage(),
      "Runtime stream failed.",
      "连接超时",
    );
    const second = first
      ? appendStreamErrorNoticeToFinalizedMessage(
          first,
          "Runtime stream failed.",
          "连接超时",
        )
      : null;

    expect(second).toBe(first);
  });
});
