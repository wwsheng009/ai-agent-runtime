import { describe, expect, it } from "vitest";

import {
  COMPOSER_SKILL_MENTION_GROUP_ID,
  isSkillMentionCaretEcho,
  skillMentionCatalogEntries,
  skillMentionCompletionCommonPrefix,
  skillMentionReferenceGroup,
  resolveSkillMentionTabExtension,
} from "./composer-skill-mentions";
import type { ComposerMenuItem } from "@/lib/composer-menu";
import type { ComposerTrigger } from "@/lib/composer-trigger";
import type { RuntimeSkill, RuntimeSkillCatalog } from "@/types/runtime";

function skill(overrides: Partial<RuntimeSkill>): RuntimeSkill {
  return {
    name: "",
    description: "",
    version: "",
    category: "",
    capabilities: [],
    tags: [],
    triggers: [],
    tools: [],
    systemPrompt: "",
    userPrompt: "",
    workflowSteps: [],
    contextFiles: [],
    contextEnvironment: [],
    contextSymbols: [],
    permissions: [],
    source: null,
    ...overrides,
  };
}

function catalog(skills: RuntimeSkill[]): RuntimeSkillCatalog {
  return { skills, count: skills.length };
}

describe("skillMentionCatalogEntries", () => {
  it("keeps text skills in catalog order and drops workflow skills", () => {
    const entries = skillMentionCatalogEntries(
      catalog([
        skill({ name: "brand", category: "design" }),
        skill({
          name: "pipeline",
          workflowSteps: [
            { id: "s1", name: "step", tool: "run", args: {}, dependsOn: [], condition: "" },
          ],
        }),
        skill({ name: "docx", description: "write docx" }),
      ]),
    );

    expect(entries.map((entry) => entry.name)).toEqual(["brand", "docx"]);
    expect(entries[0].description).toBe("design");
    expect(entries[1].description).toBe("write docx");
  });

  it("deduplicates same-name skills case-insensitively and skips blank names", () => {
    const entries = skillMentionCatalogEntries(
      catalog([
        skill({ name: "Brand" }),
        skill({ name: "brand", category: "later" }),
        skill({ name: "  " }),
      ]),
    );

    expect(entries.map((entry) => entry.name)).toEqual(["Brand"]);
  });

  it("returns an empty list for a missing catalog", () => {
    expect(skillMentionCatalogEntries(null)).toEqual([]);
    expect(skillMentionCatalogEntries(undefined)).toEqual([]);
  });

  it("prefers the description for display and keeps category as search keywords", () => {
    const entries = skillMentionCatalogEntries(
      catalog([
        skill({ name: "code-review", description: "Review a diff", category: "quality" }),
        skill({ name: "docx", category: "docs" }),
      ]),
    );

    expect(entries[0]).toMatchObject({ description: "Review a diff", keywords: "quality" });
    // 无描述时退回分类展示，同时分类仍可检索。
    expect(entries[1]).toMatchObject({ description: "docs", keywords: "docs" });
  });

  it("drops skills whose names can never be completed as $name", () => {
    const entries = skillMentionCatalogEntries(
      catalog([
        skill({ name: "my skill" }),
        skill({ name: "技能" }),
        skill({ name: "good-name" }),
      ]),
    );

    expect(entries.map((entry) => entry.name)).toEqual(["good-name"]);
  });
});

describe("skillMentionCompletionCommonPrefix", () => {
  it("extends to the shared prefix and preserves the first candidate's case", () => {
    expect(skillMentionCompletionCommonPrefix(["docx", "docs"])).toBe("doc");
    expect(skillMentionCompletionCommonPrefix(["Docs", "docx"])).toBe("Doc");
    expect(skillMentionCompletionCommonPrefix(["only"])).toBe("only");
    expect(skillMentionCompletionCommonPrefix(["alpha", "beta"])).toBe("");
    expect(skillMentionCompletionCommonPrefix([])).toBe("");
  });
});

describe("resolveSkillMentionTabExtension", () => {
  const item = (name: string): ComposerMenuItem => ({
    id: name,
    groupId: "skills",
    label: name,
    level: "leaf",
    action: { kind: "skill", name },
  });
  const triggerAt = (value: string, caret = value.length): ComposerTrigger => ({
    kind: "skill",
    query: value.slice(1, caret),
    start: 0,
    end: caret,
    key: `skill:0:${value.slice(1, caret)}`,
  });

  it("extends to the shared prefix of the query-prefix subset only", () => {
    // 子串命中（webhook-doc）不参与公共前缀计算，避免稀释 docx/docs 的 "doc"。
    const extension = resolveSkillMentionTabExtension("$do", triggerAt("$do"), [
      item("docx"),
      item("docs"),
      item("webhook-doc"),
    ]);
    expect(extension?.value).toBe("$doc");
    expect(extension?.trigger).toMatchObject({ query: "doc", end: 4, key: "skill:0:doc" });
  });

  it("returns null for a single prefix match or an already-extended prefix", () => {
    expect(
      resolveSkillMentionTabExtension("$do", triggerAt("$do"), [item("docx"), item("webhook-doc")]),
    ).toBeNull();
    expect(
      resolveSkillMentionTabExtension("$doc", triggerAt("$doc"), [item("docx"), item("docs")]),
    ).toBeNull();
  });
});

describe("isSkillMentionCaretEcho", () => {
  it("flags only same-token backoffs to a shorter query prefix", () => {
    const guard = { tokenStart: 4, query: "doc" };
    expect(
      isSkillMentionCaretEcho(
        { kind: "skill", query: "do", start: 4, end: 6, key: "skill:4:do" },
        guard,
      ),
    ).toBe(true);
    // 不同 token / 相同或更长 query / 非技能触发都不算回声。
    expect(
      isSkillMentionCaretEcho(
        { kind: "skill", query: "doc", start: 4, end: 7, key: "skill:4:doc" },
        guard,
      ),
    ).toBe(false);
    expect(
      isSkillMentionCaretEcho(
        { kind: "reference", query: "d", start: 9, end: 10, key: "reference:9:d" },
        guard,
      ),
    ).toBe(false);
  });
});

describe("skillMentionReferenceGroup", () => {
  const labels = { group: "技能", empty: "未找到匹配技能", loading: "加载中…", error: "不可用" };

  it("maps the catalog to reference items with raw names and insert text", () => {
    const group = skillMentionReferenceGroup(
      catalog([skill({ name: "brand", category: "design" }), skill({ name: "docx" })]),
      labels,
    );

    expect(group?.id).toBe(COMPOSER_SKILL_MENTION_GROUP_ID);
    expect(group?.label).toBe("技能");
    expect(group?.status).toBe("ready");
    expect(group?.items).toEqual([
      { id: "brand", label: "brand", insertText: "brand", description: "design", keywords: "design" },
      { id: "docx", label: "docx", insertText: "docx", description: "" },
    ]);
    expect(group?.emptyText).toBeUndefined();
  });

  it("keeps a ready empty group with empty text (explainable empty state)", () => {
    const group = skillMentionReferenceGroup(catalog([]), labels);
    expect(group).not.toBeNull();
    expect(group?.items).toEqual([]);
    expect(group?.emptyText).toBe("未找到匹配技能");
  });

  it("reports loading / error as status rows without empty text", () => {
    const loading = skillMentionReferenceGroup(null, labels, "loading");
    expect(loading?.status).toBe("loading");
    expect(loading?.statusText).toBe("加载中…");
    expect(loading?.emptyText).toBeUndefined();

    const error = skillMentionReferenceGroup(null, labels, "error");
    expect(error?.status).toBe("error");
    expect(error?.statusText).toBe("不可用");
  });

  it("returns null when there is nothing to show and no labels to explain it", () => {
    expect(skillMentionReferenceGroup(catalog([]), { group: "技能", empty: "" })).toBeNull();
  });
});
