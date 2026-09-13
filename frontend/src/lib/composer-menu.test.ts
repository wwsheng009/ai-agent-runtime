import { describe, expect, it } from "vitest";

import { createComposerCommandRegistry } from "./composer-commands";
import {
  COMPOSER_MENU_MAX_ITEMS_PER_GROUP,
  buildComposerMenu,
  clampComposerMenuActive,
  findComposerMenuItem,
  moveComposerMenuActive,
  resolveComposerMenuTab,
  type ComposerMenuSource,
  type ComposerReferenceGroup,
} from "./composer-menu";

const commands = createComposerCommandRegistry([
  { name: "export", kind: "action", descriptionKey: "composer.menu.commands" },
  { name: "plan" },
]).commands;

const referenceGroups: ComposerReferenceGroup[] = [
  {
    id: "artifacts",
    label: "文件",
    items: [
      { id: "a1", label: "app.tsx", insertText: "src/app.tsx" },
      { id: "a2", label: "my report.md", insertText: "my report.md" },
    ],
  },
  { id: "empty", label: "空组", items: [] },
];

function source(overrides: Partial<ComposerMenuSource> = {}): ComposerMenuSource {
  return {
    mode: "all",
    level: { kind: "root" },
    query: "",
    commands,
    referenceGroups,
    hasAttachAction: true,
    attachLabel: "添加附件",
    ...overrides,
  };
}

describe("buildComposerMenu", () => {
  it("merges commands, actions and reference launchers from a single source", () => {
    const snapshot = buildComposerMenu(source());

    expect(snapshot.groups.map((group) => group.id)).toEqual([
      "commands",
      "actions",
      "artifacts",
    ]);
    expect(snapshot.empty).toBe(false);
    expect(snapshot.activeId).toBe(snapshot.items[0].id);
    // 空分组与未请求的模式不进入菜单。
    expect(snapshot.items.some((item) => item.groupId === "empty")).toBe(false);
  });

  it("shows only the requested entry mode", () => {
    expect(buildComposerMenu(source({ mode: "commands" })).groups.map((g) => g.id)).toEqual([
      "commands",
    ]);
    expect(
      buildComposerMenu(source({ mode: "references" })).groups.map((g) => g.id),
    ).toEqual(["artifacts"]);
  });

  it("drills into a reference group instead of repeating launchers", () => {
    const snapshot = buildComposerMenu(
      source({ mode: "references", level: { kind: "group", groupId: "artifacts" } }),
    );

    expect(snapshot.groups.map((group) => group.id)).toEqual(["artifacts"]);
    expect(snapshot.items.map((item) => item.label)).toEqual([
      "app.tsx",
      "my report.md",
    ]);
    expect(snapshot.items.every((item) => item.level === "leaf")).toBe(true);
    expect(findComposerMenuItem(snapshot.items, "reference:artifacts:a1")?.action).toEqual({
      kind: "reference",
      text: "src/app.tsx",
    });
  });

  it("filters by query with prefix matches ranked first", () => {
    const snapshot = buildComposerMenu(source({ mode: "all", query: "pl" }));
    expect(snapshot.items.map((item) => item.label)).toEqual(["/plan"]);
    expect(snapshot.groups.map((group) => group.id)).toEqual(["commands"]);
  });

  it("reports an empty snapshot when nothing matches", () => {
    const snapshot = buildComposerMenu(source({ query: "zzz" }));
    expect(snapshot.empty).toBe(true);
    expect(snapshot.items).toEqual([]);
    expect(snapshot.activeId).toBeNull();
  });

  it("caps each group at the declared limit", () => {
    const many = Array.from({ length: COMPOSER_MENU_MAX_ITEMS_PER_GROUP + 4 }, (_, index) => ({
      id: `f${index}`,
      label: `file-${index}.ts`,
      insertText: `file-${index}.ts`,
    }));
    const snapshot = buildComposerMenu(
      source({
        mode: "references",
        level: { kind: "group", groupId: "artifacts" },
        referenceGroups: [{ id: "artifacts", label: "文件", items: many }],
      }),
    );
    expect(snapshot.items).toHaveLength(COMPOSER_MENU_MAX_ITEMS_PER_GROUP);
  });

  it("omits the attach action when the host does not provide it", () => {
    const snapshot = buildComposerMenu(source({ hasAttachAction: false }));
    expect(snapshot.items.some((item) => item.action.kind === "attach")).toBe(false);
  });
});

describe("composer menu highlight movement", () => {
  const items = buildComposerMenu(source({ mode: "commands" })).items;

  it("clamps a stale highlight back to the first item", () => {
    expect(clampComposerMenuActive(items, "missing")).toBe(items[0].id);
    expect(clampComposerMenuActive([], null)).toBeNull();
    expect(clampComposerMenuActive(items, items[1].id)).toBe(items[1].id);
  });

  it("moves in a ring", () => {
    expect(moveComposerMenuActive(items, items[0].id, -1)).toBe(
      items[items.length - 1].id,
    );
    expect(moveComposerMenuActive(items, items[items.length - 1].id, 1)).toBe(
      items[0].id,
    );
    expect(moveComposerMenuActive(items, null, 1)).toBe(items[0].id);
  });

  it("treats launchers as drill and leaves without a highlight as pass", () => {
    const root = buildComposerMenu(source({ mode: "references" }));
    const launcher = root.items[0];
    expect(launcher.level).toBe("launcher");
    expect(resolveComposerMenuTab(root.items, launcher.id)).toBe("drill");

    const leaf = buildComposerMenu(
      source({ mode: "references", level: { kind: "group", groupId: "artifacts" } }),
    ).items[0];
    expect(resolveComposerMenuTab([leaf], leaf.id)).toBe("pick");
    expect(resolveComposerMenuTab(root.items, null)).toBe("pass");
    expect(resolveComposerMenuTab(root.items, "missing")).toBe("pass");
  });
});
