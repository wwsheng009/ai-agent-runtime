import { describe, expect, it } from "vitest";

import type { ChatMessage } from "@/data/mock";

import {
  countUserTurnIndex,
  extractUserMessagePreview,
  extractUserMessageText,
  isEditableKeyboardTarget,
  moveBacktrackNavigationSelection,
  resolveBacktrackEditPrompt,
  resolveBacktrackMessageSelector,
  resolveInitialBacktrackNavigationId,
  resolveSeededBacktrackEditPrompt,
  resolveUserTurnTargets,
} from "./use-session-backtrack";

describe("use-session-backtrack helpers", () => {
  const messages: ChatMessage[] = [
    {
      id: "u0",
      role: "user",
      author: "You",
      label: "prompt",
      segments: [{ type: "text", content: "first prompt" }],
    },
    {
      id: "a0",
      role: "assistant",
      author: "Agent",
      label: "reply",
      segments: [{ type: "text", content: "first reply" }],
    },
    {
      id: "u1",
      role: "user",
      author: "You",
      label: "prompt",
      segments: [{ type: "text", content: "second prompt with more detail" }],
    },
  ];

  it("maps user message ids to user_turn_index / message_index", () => {
    expect(countUserTurnIndex(messages, "u0")).toEqual({
      userTurnIndex: 0,
      messageIndex: 0,
    });
    expect(countUserTurnIndex(messages, "u1")).toEqual({
      userTurnIndex: 1,
      messageIndex: 2,
    });
    expect(countUserTurnIndex(messages, "a0")).toBeNull();
    expect(countUserTurnIndex(messages, "missing")).toBeNull();
  });

  it("extracts compact user previews from text segments", () => {
    expect(extractUserMessagePreview(messages[2])).toBe(
      "second prompt with more detail",
    );
    expect(
      extractUserMessagePreview({
        id: "empty",
        role: "user",
        author: "You",
        label: "prompt",
        segments: [{ type: "code", language: "ts", code: "const x = 1;" }],
      }),
    ).toBe("(empty)");
  });

  it("extracts full multi-segment user text and seeds targets", () => {
    const multi: ChatMessage = {
      id: "u2",
      role: "user",
      author: "You",
      label: "prompt",
      segments: [
        { type: "text", content: "line one" },
        { type: "text", content: "line two" },
      ],
    };
    expect(extractUserMessageText(multi)).toBe("line one\nline two");
    const targets = resolveUserTurnTargets([...messages, multi]);
    expect(targets).toHaveLength(3);
    expect(targets[2]?.fullText).toBe("line one\nline two");
    expect(targets[2]?.preview).toBe("line one line two");
  });

  it("only emits edit_prompt when the text actually changed", () => {
    expect(resolveBacktrackEditPrompt("same", "same")).toBeUndefined();
    expect(resolveBacktrackEditPrompt("  same  ", "same")).toBeUndefined();
    expect(resolveBacktrackEditPrompt("changed", "same")).toBe("changed");
    expect(resolveBacktrackEditPrompt("", "same")).toBe("");
    expect(resolveBacktrackEditPrompt("", "")).toBeUndefined();
  });

  it("seeds dialog editPrompt from inline options or original full text", () => {
    expect(resolveSeededBacktrackEditPrompt(undefined, "original")).toBe("original");
    expect(resolveSeededBacktrackEditPrompt({}, "original")).toBe("original");
    expect(resolveSeededBacktrackEditPrompt({ editPrompt: "rewritten" }, "original")).toBe(
      "rewritten",
    );
    expect(resolveSeededBacktrackEditPrompt({ editPrompt: "" }, "original")).toBe("");
  });

  it("detects editable keyboard targets for Esc navigation gating", () => {
    const textarea = document.createElement("textarea");
    const div = document.createElement("div");
    expect(isEditableKeyboardTarget(textarea)).toBe(true);
    expect(isEditableKeyboardTarget(div)).toBe(false);
    expect(isEditableKeyboardTarget(null)).toBe(false);
  });

  it("selects durable message_id selectors and skips synthetic history ids", () => {
    expect(resolveBacktrackMessageSelector("msg_abc123")).toBe("msg_abc123");
    expect(
      resolveBacktrackMessageSelector("session-1-history-2", "session-1"),
    ).toBeUndefined();
    expect(resolveBacktrackMessageSelector("legacy-stable-id")).toBe(
      "legacy-stable-id",
    );
  });

  it("starts transcript navigation on the latest user turn and moves by delta", () => {
    const targets = resolveUserTurnTargets(messages);
    expect(resolveInitialBacktrackNavigationId(targets)).toBe("u1");
    expect(resolveInitialBacktrackNavigationId(targets, "u0")).toBe("u0");
    expect(resolveInitialBacktrackNavigationId(targets, "missing")).toBe("u1");
    expect(moveBacktrackNavigationSelection(targets, "u1", -1)).toBe("u0");
    expect(moveBacktrackNavigationSelection(targets, "u0", -1)).toBe("u0");
    expect(moveBacktrackNavigationSelection(targets, "u0", 1)).toBe("u1");
    expect(moveBacktrackNavigationSelection(targets, null, 0)).toBe("u1");
    expect(moveBacktrackNavigationSelection([], null, 1)).toBeNull();
  });
});
