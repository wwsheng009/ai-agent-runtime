import { describe, expect, it } from "vitest";

import {
  COMPOSER_MENU_MAX_ITEMS_PER_GROUP,
  buildComposerMenu,
  findComposerMenuItem,
} from "./composer-menu";
import { source } from "./composer-menu.test-fixtures";

describe("buildComposerMenu", () => {
  it("builds category launchers at the + root instead of flattening leaves", () => {
    const snapshot = buildComposerMenu(source());

    expect(snapshot.groups.map((group) => group.id)).toEqual([
      "actions",
      "references",
      "commands",
    ]);
    expect(snapshot.empty).toBe(false);
    expect(snapshot.activeId).toBe(snapshot.items[0].id);
    expect(snapshot.items.every((item) => item.level === "launcher")).toBe(true);
    expect(snapshot.items.map((item) => item.id)).toEqual([
      "launcher:actions",
      "launcher:references",
      "launcher:commands",
    ]);
    // 空分组与未请求的模式不进入菜单。
    expect(snapshot.items.some((item) => item.groupId === "empty")).toBe(false);
  });

  it("shows only the requested entry mode", () => {
    expect(
      buildComposerMenu(source({ mode: "commands" })).groups.map((g) => g.id),
    ).toEqual(["commands"]);
    expect(
      buildComposerMenu(source({ mode: "references" })).groups.map((g) => g.id),
    ).toEqual(["artifacts"]);
  });

  it("drills into a reference group instead of repeating launchers", () => {
    const snapshot = buildComposerMenu(
      source({
        mode: "references",
        level: { kind: "group", groupId: "artifacts" },
      }),
    );

    expect(snapshot.groups.map((group) => group.id)).toEqual(["artifacts"]);
    expect(snapshot.items.map((item) => item.label)).toEqual([
      "app.tsx",
      "my report.md",
    ]);
    expect(snapshot.items.every((item) => item.level === "leaf")).toBe(true);
    expect(
      findComposerMenuItem(snapshot.items, "reference:artifacts:a1")?.action,
    ).toEqual({
      kind: "reference",
      text: "src/app.tsx",
    });
  });

  it("filters by query with prefix matches ranked first", () => {
    const snapshot = buildComposerMenu(
      source({
        mode: "all",
        level: { kind: "group", groupId: "commands" },
        query: "pl",
      }),
    );
    expect(snapshot.items.map((item) => item.label)).toEqual(["/plan"]);
    expect(snapshot.groups.map((group) => group.id)).toEqual(["commands"]);
  });

  it("reports an empty snapshot when nothing matches", () => {
    const snapshot = buildComposerMenu(
      source({ mode: "commands", query: "zzz" }),
    );
    expect(snapshot.empty).toBe(true);
    expect(snapshot.items).toEqual([]);
    expect(snapshot.activeId).toBeNull();
  });

  it("caps each group at the declared limit", () => {
    const many = Array.from(
      { length: COMPOSER_MENU_MAX_ITEMS_PER_GROUP + 4 },
      (_, index) => ({
        id: `f${index}`,
        label: `file-${index}.ts`,
        insertText: `file-${index}.ts`,
      }),
    );
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
    const root = buildComposerMenu(source({ hasAttachAction: false }));
    expect(root.groups.some((group) => group.id === "actions")).toBe(false);

    const drilled = buildComposerMenu(
      source({
        level: { kind: "group", groupId: "actions" },
        hasAttachAction: false,
      }),
    );
    expect(drilled.items).toEqual([]);
  });

  it("adds selectable workspace options only when the host provides workspace data", () => {
    const snapshot = buildComposerMenu(
      source({
        workspaceSelection: {
          label: "工作区",
          options: [
            { id: "repo", label: "项目", path: "E:/projects/demo" },
            { id: "docs", label: "文档", path: "E:/docs" },
          ],
          selectedId: "repo",
          clearLabel: "不选择工作区",
        },
      }),
    );
    const workspaceLauncher = snapshot.items.find(
      (item) => item.id === "launcher:workspace",
    );
    expect(workspaceLauncher).toMatchObject({
      level: "launcher",
      count: 3,
    });

    const drilled = buildComposerMenu(
      source({
        level: { kind: "group", groupId: "workspace" },
        workspaceSelection: {
          label: "工作区",
          options: [
            { id: "repo", label: "项目", path: "E:/projects/demo" },
            { id: "docs", label: "文档", path: "E:/docs" },
          ],
          selectedId: "repo",
          clearLabel: "不选择工作区",
        },
      }),
    );
    const workspaceItems = drilled.items.filter(
      (item) => item.action.kind === "workspace",
    );

    expect(snapshot.groups.map((group) => group.id)).toContain("workspace");
    expect(workspaceItems).toMatchObject([
      {
        id: "workspace:none",
        selected: false,
        action: { kind: "workspace", directoryId: null },
      },
      {
        id: "workspace:repo",
        selected: true,
        description: "E:/projects/demo",
        action: { kind: "workspace", directoryId: "repo" },
      },
      {
        id: "workspace:docs",
        selected: false,
        action: { kind: "workspace", directoryId: "docs" },
      },
    ]);
    expect(
      buildComposerMenu(source()).items.some(
        (item) => item.action.kind === "workspace",
      ),
    ).toBe(false);
  });

  it("keeps a workspace group visible while directory data is loading or empty", () => {
    const root = buildComposerMenu(
      source({
        workspaceSelection: {
          label: "工作区",
          options: [],
          selectedId: null,
          clearLabel: "不选择工作区",
          statusText: "正在加载…",
        },
      }),
    );
    expect(root.groups.map((group) => group.id)).toContain("workspace");

    const loading = buildComposerMenu(
      source({
        level: { kind: "group", groupId: "workspace" },
        workspaceSelection: {
          label: "工作区",
          options: [],
          selectedId: null,
          clearLabel: "不选择工作区",
          statusText: "正在加载…",
        },
      }),
    );
    expect(
      loading.groups.find((group) => group.id === "workspace"),
    ).toMatchObject({
      statusText: "正在加载…",
      items: [{ action: { kind: "workspace", directoryId: null } }],
    });

    const empty = buildComposerMenu(
      source({
        level: { kind: "group", groupId: "workspace" },
        workspaceSelection: {
          label: "工作区",
          options: [],
          selectedId: null,
          clearLabel: "不选择工作区",
          emptyText: "暂无工作区",
        },
      }),
    );
    expect(
      empty.groups.find((group) => group.id === "workspace")?.emptyText,
    ).toBe("暂无工作区");
  });
});