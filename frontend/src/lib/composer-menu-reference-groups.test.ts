import { describe, expect, it } from "vitest";

import {
  buildComposerMenu,
  type ComposerMenuSource,
  type ComposerReferenceGroup,
} from "./composer-menu";
import { source } from "./composer-menu.test-fixtures";

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

  function leafSource(
    group: ComposerReferenceGroup,
    query: string,
  ): ComposerMenuSource {
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
        {
          id: "workspace-files",
          label: "工作区文件",
          items: serverItems,
          serverFiltered: true,
        },
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
      leafSource(
        { id: "workspace-files", label: "工作区文件", items: serverItems },
        "cmpmenu",
      ),
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

    expect(snapshot.groups.map((group) => group.id)).toEqual([
      "workspace-files",
    ]);
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
    expect(snapshot.groups.map((group) => group.id)).toEqual([
      "workspace-files",
    ]);
    expect(snapshot.groups[0].emptyText).toBe(
      "未找到匹配文件，可继续输入缩小范围",
    );
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

    expect(snapshot.groups.map((group) => group.id)).toEqual([
      "workspace-files",
    ]);
    expect(snapshot.groups[0]).toMatchObject({
      status: "error",
      statusText: "工作区文件不可用",
    });
  });
});