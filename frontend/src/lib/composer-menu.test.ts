import { describe, expect, it } from "vitest";

import { createComposerCommandRegistry } from "./composer-commands";
import {
  COMPOSER_MENU_MAX_ITEMS_PER_GROUP,
  buildComposerMenu,
  clampComposerMenuActive,
  findComposerMenuItem,
  moveComposerMenuActive,
  resolveComposerMenuTab,
  type ComposerCommandOptionsSource,
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

describe("composer command option level", () => {
  const modelCommands = createComposerCommandRegistry([
    {
      name: "model",
      kind: "popupSelect",
      options: [{ value: "gpt-5", label: "gpt-5", description: "openai" }],
    },
  ]).commands;

  const optionsSource: ComposerCommandOptionsSource = {
    commandKey: "model",
    commandName: "model",
    label: "model",
    options: [
      { value: "deepseek-chat", label: "deepseek-chat", description: "deepseek" },
      { value: "gpt-5", label: "gpt-5", description: "openai" },
      { value: "gpt-5-mini", label: "gpt-5-mini", description: "openai" },
    ],
  };

  function optionSource(overrides: Partial<ComposerMenuSource> = {}): ComposerMenuSource {
    return source({
      mode: "all",
      level: { kind: "command-options", commandKey: "model" },
      commands: modelCommands,
      commandOptions: optionsSource,
      ...overrides,
    });
  }

  it("候选独占一层：不混入命令 / 引用 / 附件，动作携带命令名与参数原文", () => {
    const snapshot = buildComposerMenu(optionSource());

    expect(snapshot.groups.map((group) => group.id)).toEqual(["command-options:model"]);
    expect(snapshot.items.map((item) => item.label)).toEqual([
      "deepseek-chat",
      "gpt-5",
      "gpt-5-mini",
    ]);
    expect(snapshot.items[0].description).toBe("deepseek");
    expect(snapshot.items[0].action).toEqual({
      kind: "command-option",
      name: "model",
      value: "deepseek-chat",
    });
  });

  it("按 query 过滤候选，全落空视为空快照", () => {
    expect(
      buildComposerMenu(optionSource({ query: "gpt" })).items.map((item) => item.label),
    ).toEqual(["gpt-5", "gpt-5-mini"]);

    const empty = buildComposerMenu(optionSource({ query: "zzz" }));
    expect(empty.empty).toBe(true);
    expect(empty.items).toEqual([]);
  });

  it("缺候选来源或候选为空时按无候选处理（不伪造占位项）", () => {
    const missing = buildComposerMenu(optionSource({ commandOptions: null }));
    expect(missing.groups).toEqual([]);
    expect(missing.items).toEqual([]);
    expect(missing.empty).toBe(true);

    const blank = buildComposerMenu(
      optionSource({ commandOptions: { ...optionsSource, options: [] } }),
    );
    expect(blank.groups).toEqual([]);
    expect(blank.empty).toBe(true);
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
