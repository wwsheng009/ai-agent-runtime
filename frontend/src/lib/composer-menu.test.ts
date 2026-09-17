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

describe("composer reference groups（工作区文件数据源扩展）", () => {
  // 子序列命中（客户端 `includes` 会误杀）：服务端过滤的组必须原样保留。
  const serverItems = [
    {
      id: "frontend/src/lib/composer-menu.ts",
      label: "composer-menu.ts",
      insertText: "frontend/src/lib/composer-menu.ts",
      description: "frontend/src/lib/composer-menu.ts",
    },
    {
      id: "frontend/src/hooks/workspace/composer/use-composer-menu.ts",
      label: "use-composer-menu.ts",
      insertText: "frontend/src/hooks/workspace/composer/use-composer-menu.ts",
      description: "frontend/src/hooks/workspace/composer/use-composer-menu.ts",
    },
  ];

  function leafSource(group: ComposerReferenceGroup, query: string): ComposerMenuSource {
    return source({
      mode: "references",
      level: { kind: "group", groupId: group.id },
      query,
      referenceGroups: [group],
    });
  }

  it("serverFiltered 组跳过客户端过滤与重排，保留服务端顺序", () => {
    const snapshot = buildComposerMenu(
      leafSource(
        { id: "workspace-files", label: "工作区文件", items: serverItems, serverFiltered: true },
        "cmpmenu",
      ),
    );

    expect(snapshot.items.map((item) => item.label)).toEqual([
      "composer-menu.ts",
      "use-composer-menu.ts",
    ]);
    expect(snapshot.items[0].action).toEqual({
      kind: "reference",
      text: "frontend/src/lib/composer-menu.ts",
    });
  });

  it("未标记 serverFiltered 的组仍按客户端子串过滤（产物组语义不变）", () => {
    const snapshot = buildComposerMenu(
      leafSource({ id: "workspace-files", label: "工作区文件", items: serverItems }, "cmpmenu"),
    );

    expect(snapshot.items).toEqual([]);
    expect(snapshot.groups).toEqual([]);
    expect(snapshot.empty).toBe(true);
  });

  it("loading 的空组进入 launcher 并携带状态文案", () => {
    const snapshot = buildComposerMenu(
      source({
        mode: "references",
        referenceGroups: [
          {
            id: "workspace-files",
            label: "工作区文件",
            items: [],
            status: "loading",
            statusText: "正在读取工作区文件…",
          },
        ],
      }),
    );

    expect(snapshot.groups.map((group) => group.id)).toEqual(["workspace-files"]);
    expect(snapshot.groups[0]).toMatchObject({
      status: "loading",
      statusText: "正在读取工作区文件…",
    });
    expect(snapshot.items.map((item) => item.label)).toEqual(["工作区文件"]);
  });

  it("ready 的空组带空态文案时保留分组（叶子层显示空态而不是整组消失）", () => {
    const snapshot = buildComposerMenu(
      leafSource(
        {
          id: "workspace-files",
          label: "工作区文件",
          items: [],
          status: "ready",
          serverFiltered: true,
          emptyText: "未找到匹配文件，可继续输入缩小范围",
        },
        "zzz",
      ),
    );

    expect(snapshot.items).toEqual([]);
    expect(snapshot.groups.map((group) => group.id)).toEqual(["workspace-files"]);
    expect(snapshot.groups[0].emptyText).toBe("未找到匹配文件，可继续输入缩小范围");
    expect(snapshot.empty).toBe(true);
  });

  it("hasMore / truncated / truncatedText 透传到分组页脚", () => {
    const snapshot = buildComposerMenu(
      leafSource(
        {
          id: "workspace-files",
          label: "工作区文件",
          items: serverItems,
          serverFiltered: true,
          status: "ready",
          hasMore: true,
          truncated: true,
          truncatedText: "结果已截断，继续输入以缩小范围",
        },
        "",
      ),
    );

    expect(snapshot.groups[0]).toMatchObject({
      hasMore: true,
      truncated: true,
      truncatedText: "结果已截断，继续输入以缩小范围",
    });
  });

  it("serverFiltered 组即使无候选也保留错误状态行（不静默消失）", () => {
    const snapshot = buildComposerMenu(
      leafSource(
        {
          id: "workspace-files",
          label: "工作区文件",
          items: [],
          status: "error",
          serverFiltered: true,
          statusText: "工作区文件不可用",
        },
        "a",
      ),
    );

    expect(snapshot.groups.map((group) => group.id)).toEqual(["workspace-files"]);
    expect(snapshot.groups[0]).toMatchObject({ status: "error", statusText: "工作区文件不可用" });
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
