import { describe, expect, it } from "vitest";

import {
  applyComposerTriggerInsertion,
  composerReferenceText,
  detectComposerTrigger,
  type ComposerTrigger,
} from "./composer-trigger";

function triggerAt(value: string, caret = value.length): ComposerTrigger | null {
  return detectComposerTrigger(value, caret);
}

describe("detectComposerTrigger", () => {
  it("detects a slash command only at line start", () => {
    expect(triggerAt("/ex")).toMatchObject({ kind: "slash", query: "ex", start: 0 });
    expect(triggerAt("run /ex")).toBeNull();
    expect(triggerAt("src/app.tsx")).toBeNull();
  });

  it("detects a slash command on a later line", () => {
    expect(triggerAt("hello\n/mod", 10)).toMatchObject({
      kind: "slash",
      query: "mod",
      start: 6,
      end: 10,
    });
  });

  it("stops matching once the token contains whitespace", () => {
    expect(triggerAt("/export now")).toBeNull();
  });

  it("detects references after start-of-line or whitespace only", () => {
    expect(triggerAt("@src")).toMatchObject({ kind: "reference", query: "src" });
    expect(triggerAt("see @src")).toMatchObject({ kind: "reference", query: "src" });
    expect(triggerAt("mail@host")).toBeNull();
    expect(triggerAt("@src ")).toBeNull();
  });

  it("uses a stable key per token so Esc can keep it closed", () => {
    expect(triggerAt("/a")?.key).toBe(triggerAt("/a")?.key);
    expect(triggerAt("/a")?.key).not.toBe(triggerAt("/ab")?.key);
  });

  it("clamps out-of-range carets instead of throwing", () => {
    expect(triggerAt("/cmd", 999)).toMatchObject({ kind: "slash", query: "cmd" });
    expect(detectComposerTrigger("/cmd", -5)).toBeNull();
    expect(triggerAt("@", 0)).toBeNull();
  });
});

describe("composerReferenceText", () => {
  it("quotes references that contain whitespace", () => {
    expect(composerReferenceText("src/app.tsx")).toBe("@src/app.tsx");
    expect(composerReferenceText("My Session")).toBe('@"My Session"');
    expect(composerReferenceText("  ")).toBe("@");
  });
});

describe("applyComposerTriggerInsertion", () => {
  it("replaces the trigger token and keeps the caret after it", () => {
    const value = "read @sr";
    const trigger = triggerAt(value);
    expect(trigger).not.toBeNull();
    expect(applyComposerTriggerInsertion(value, trigger!, "@src/app.tsx")).toEqual({
      value: "read @src/app.tsx",
      caret: "read @src/app.tsx".length,
    });
  });

  it("inserts a separating space before trailing text", () => {
    const value = "read @sr please";
    const trigger = detectComposerTrigger(value, 8);
    expect(applyComposerTriggerInsertion(value, trigger!, "@src/app.tsx")).toEqual({
      value: "read @src/app.tsx please",
      caret: "read @src/app.tsx".length,
    });
  });

  it("does not add a space before punctuation", () => {
    const value = "read @sr.";
    const trigger = triggerAt(value, 8);
    expect(applyComposerTriggerInsertion(value, trigger!, "@src/app.tsx").value).toBe(
      "read @src/app.tsx.",
    );
  });

  it("completes a slash command in place", () => {
    const value = "/ex";
    const trigger = triggerAt(value);
    expect(applyComposerTriggerInsertion(value, trigger!, "/export")).toEqual({
      value: "/export",
      caret: 7,
    });
  });
});
