import { describe, expect, it } from "vitest";

import {
  applyComposerCaretInsertion,
  applyComposerSkillPrefixInsertion,
  applyComposerSkillMentionInsertion,
  applyComposerTriggerInsertion,
  composerReferenceText,
  composerSkillMentionText,
  detectComposerTrigger,
  isCompletableSkillMentionName,
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

  it("detects skill mentions at start-of-line, after whitespace, or after punctuation", () => {
    expect(triggerAt("$")).toMatchObject({ kind: "skill", query: "", start: 0 });
    expect(triggerAt("$bra")).toMatchObject({ kind: "skill", query: "bra", start: 0 });
    expect(triggerAt("use $bra")).toMatchObject({ kind: "skill", query: "bra" });
    expect(triggerAt("($bra)", 5)).toMatchObject({ kind: "skill", query: "bra", start: 1 });
    // 光标在 token 左侧的查询串中间：仍按左侧完整 token 解析。
    expect(triggerAt("$brand-x", 6)).toMatchObject({ kind: "skill", query: "brand" });
  });

  it("rejects mid-word, env-like, numeric, and spaced skill tokens", () => {
    expect(triggerAt("a$b")).toBeNull();
    expect(triggerAt("$$")).toBeNull();
    expect(triggerAt("$100")).toBeNull();
    expect(triggerAt("$HOME")).toBeNull();
    expect(triggerAt("$path")).toBeNull();
    expect(triggerAt("$env:FOO", 4)).toBeNull();
    expect(triggerAt("$brand x")).toBeNull();
  });

  it("rejects skill tokens inside inline code and fenced code blocks", () => {
    expect(triggerAt("`$bra`", 4)).toBeNull();
    expect(triggerAt("run `$bra`", 9)).toBeNull();
    expect(triggerAt("```\n$bra", 8)).toBeNull();
    expect(triggerAt("```\ncode\n```\n$bra")).toMatchObject({ kind: "skill", query: "bra" });
  });

  it("keeps the slash trigger precedence over the skill scan", () => {
    expect(triggerAt("/skill $br", 10)).toMatchObject({ kind: "skill", query: "br" });
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

describe("composerSkillMentionText / applyComposerSkillMentionInsertion", () => {
  it("formats the mention text as $name", () => {
    expect(composerSkillMentionText("brand-guidelines")).toBe("$brand-guidelines");
    expect(composerSkillMentionText("  spaced  ")).toBe("$spaced");
  });

  it("replaces the trigger with a trailing space at end of input", () => {
    const value = "use $bra";
    const trigger = detectComposerTrigger(value, value.length);
    expect(applyComposerSkillMentionInsertion(value, trigger!, "brand-guidelines")).toEqual({
      value: "use $brand-guidelines ",
      caret: "use $brand-guidelines ".length,
    });
  });

  it("does not duplicate the separator space before trailing text", () => {
    const value = "use $bra please";
    const trigger = detectComposerTrigger(value, 8);
    expect(applyComposerSkillMentionInsertion(value, trigger!, "brand-guidelines")).toEqual({
      value: "use $brand-guidelines please",
      caret: "use $brand-guidelines".length,
    });
  });

  it("adds a separator space before non-whitespace trailing text", () => {
    const value = "use $bra, then write";
    const trigger = detectComposerTrigger(value, 8);
    expect(applyComposerSkillMentionInsertion(value, trigger!, "brand-guidelines")).toEqual({
      value: "use $brand-guidelines , then write",
      caret: "use $brand-guidelines ".length,
    });
  });

  it("supports completing a second mention after an inserted first one", () => {
    const first = "use $bra";
    const firstTrigger = detectComposerTrigger(first, first.length);
    const afterFirst = applyComposerSkillMentionInsertion(first, firstTrigger!, "brand").value;
    // 用户继续输入第二个提及与提示词。
    const second = `${afterFirst}按照 $doc`;
    const secondTrigger = detectComposerTrigger(second, second.length);
    expect(secondTrigger).toMatchObject({ kind: "skill", query: "doc" });
    expect(applyComposerSkillMentionInsertion(second, secondTrigger!, "docx").value).toBe(
      "use $brand 按照 $docx ",
    );
  });

  it("extends only the common prefix on Tab without a trailing space", () => {
    const value = "use $do next";
    const trigger = detectComposerTrigger(value, 7);
    expect(trigger).toMatchObject({ kind: "skill", query: "do" });
    expect(applyComposerSkillPrefixInsertion(value, trigger!, "doc")).toEqual({
      value: "use $doc next",
      caret: "use $doc".length,
    });

    // 行尾：替换后光标落在 `$doc` 之后（无尾随空格，等待继续收窄）。
    const atEnd = "use $do";
    const endTrigger = detectComposerTrigger(atEnd, atEnd.length);
    expect(applyComposerSkillPrefixInsertion(atEnd, endTrigger!, "doc")).toEqual({
      value: "use $doc",
      caret: "use $doc".length,
    });
  });
});

describe("applyComposerCaretInsertion", () => {
  it("inserts at the caret with separator spaces only when needed", () => {
    // 行中：前接非空白 → 前置空格；后续是空白 → 不补尾随，避免双空格。
    expect(
      applyComposerCaretInsertion("hello world", 5, "$docx", { trailingSpace: true }),
    ).toEqual({ value: "hello $docx world", caret: "hello $docx".length });

    // 行尾：补尾随空格便于继续输入。
    expect(
      applyComposerCaretInsertion("hello", 5, "$docx", { trailingSpace: true }),
    ).toEqual({ value: "hello $docx ", caret: "hello $docx ".length });

    // 空草稿 / 光标越界收敛到文本两端。
    expect(applyComposerCaretInsertion("", 99, "@src/app.tsx")).toEqual({
      value: "@src/app.tsx",
      caret: "@src/app.tsx".length,
    });
  });
});

describe("isCompletableSkillMentionName", () => {
  it("accepts names made of skill-mention bytes only", () => {
    expect(isCompletableSkillMentionName("brand-guidelines")).toBe(true);
    expect(isCompletableSkillMentionName("docx_2")).toBe(true);
  });

  it("rejects empty names and names that can never be parsed as $name", () => {
    expect(isCompletableSkillMentionName("")).toBe(false);
    expect(isCompletableSkillMentionName("my skill")).toBe(false);
    expect(isCompletableSkillMentionName("技能")).toBe(false);
  });
});
