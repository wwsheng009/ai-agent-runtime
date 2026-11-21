import { describe, expect, it } from "vitest";

import {
  buildComposerMenu,
  findComposerMenuItem,
  type ComposerReferenceGroup,
} from "./composer-menu";
import { source } from "./composer-menu.test-fixtures";

describe("composer skill groups（`$` 技能提及）", () => {
  const skillGroups: ComposerReferenceGroup[] = [
    {
      id: "skills",
      label: "技能",
      items: [
        {
          id: "brand",
          label: "brand",
          insertText: "brand",
          description: "design",
        },
        { id: "docx", label: "docx", insertText: "docx", description: "docs" },
      ],
    },
  ];

  it("renders skill leaves directly for the `$` mode（不做 launcher 下钻）", () => {
    const snapshot = buildComposerMenu(source({ mode: "skills", skillGroups }));

    expect(snapshot.groups.map((group) => group.id)).toEqual(["skills"]);
    expect(snapshot.items.map((item) => item.label)).toEqual(["brand", "docx"]);
    expect(snapshot.items.every((item) => item.level === "leaf")).toBe(true);
    expect(
      findComposerMenuItem(snapshot.items, "skill:skills:brand")?.action,
    ).toEqual({
      kind: "skill",
      name: "brand",
    });
    // `$` 模式不混入命令/引用/附件。
    expect(snapshot.groups.some((group) => group.id === "commands")).toBe(
      false,
    );
  });

  it("filters skills by name / description with prefix matches ranked first", () => {
    const byName = buildComposerMenu(
      source({ mode: "skills", skillGroups, query: "do" }),
    );
    expect(byName.items.map((item) => item.label)).toEqual(["docx"]);

    const byDescription = buildComposerMenu(
      source({ mode: "skills", skillGroups, query: "design" }),
    );
    expect(byDescription.items.map((item) => item.label)).toEqual(["brand"]);

    const none = buildComposerMenu(
      source({ mode: "skills", skillGroups, query: "zzz" }),
    );
    expect(none.empty).toBe(true);
    expect(none.groups).toEqual([]);
  });

  it("keeps a ready empty group with empty text, and a loading group with status text", () => {
    const empty = buildComposerMenu(
      source({
        mode: "skills",
        skillGroups: [
          {
            id: "skills",
            label: "技能",
            items: [],
            emptyText: "未找到匹配技能",
          },
        ],
      }),
    );
    expect(empty.groups.map((group) => group.id)).toEqual(["skills"]);
    expect(empty.groups[0].emptyText).toBe("未找到匹配技能");
    expect(empty.empty).toBe(true);

    const loading = buildComposerMenu(
      source({
        mode: "skills",
        skillGroups: [
          {
            id: "skills",
            label: "技能",
            items: [],
            status: "loading",
            statusText: "加载中…",
          },
        ],
      }),
    );
    expect(loading.groups[0].statusText).toBe("加载中…");
  });

  it("surfaces skills in the `+` menu as a launcher then drills into leaves", () => {
    const root = buildComposerMenu(source({ mode: "all", skillGroups }));
    const launcherGroup = root.groups.find((group) => group.id === "skills");
    expect(launcherGroup?.items).toHaveLength(1);
    expect(launcherGroup?.items[0]).toMatchObject({
      id: "launcher:skills",
      level: "launcher",
      count: skillGroups[0].items.length,
    });

    const drilled = buildComposerMenu(
      source({
        mode: "all",
        level: { kind: "group", groupId: "skills" },
        skillGroups,
      }),
    );
    expect(drilled.groups.map((group) => group.id)).toEqual(["skills"]);
    expect(drilled.items.every((item) => item.level === "leaf")).toBe(true);
    expect(drilled.items[0]?.action).toMatchObject({ kind: "skill" });
  });

  it("filters skill leaves after entering the skills category from `+`", () => {
    const snapshot = buildComposerMenu(
      source({
        mode: "all",
        level: { kind: "group", groupId: "skills" },
        query: "doc",
        skillGroups,
      }),
    );
    expect(snapshot.groups.some((group) => group.id === "skills")).toBe(true);
    expect(snapshot.items.map((item) => item.label)).toEqual(["docx"]);
    expect(snapshot.items.every((item) => item.level === "leaf")).toBe(true);
  });

  it("ignores skill groups outside the `$` mode", () => {
    const references = buildComposerMenu(
      source({ mode: "references", skillGroups }),
    );
    expect(references.items.some((item) => item.action.kind === "skill")).toBe(
      false,
    );
    const commands = buildComposerMenu(
      source({ mode: "commands", skillGroups }),
    );
    expect(commands.items.some((item) => item.action.kind === "skill")).toBe(
      false,
    );
  });
});