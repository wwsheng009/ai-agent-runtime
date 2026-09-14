import { describe, expect, it } from "vitest";

import { resolveSessionGroupVisibility } from "./session-grouping";
import {
  buildLineageIndex,
  isSameSessionLineage,
  lineageRowIdentity,
  readSessionLineage,
  resolveLineageOriginTitle,
  resolveRowDepth,
  stabilizeLineageOrder,
  type SessionLineageRow,
} from "./session-lineage";

/** 无谱系行（普通会话）。 */
function plainRow(
  id: string,
  extra: Partial<SessionLineageRow> = {},
): SessionLineageRow {
  return { id, sessionId: id, ...extra };
}

/** 分支子行：`forkedFrom.sessionId` 指向父会话。 */
function forkRow(
  id: string,
  parentId: string,
  extra: Partial<SessionLineageRow> = {},
): SessionLineageRow {
  return {
    id,
    sessionId: id,
    forkedFrom: { sessionId: parentId },
    ...extra,
  };
}

function ids(rows: readonly { id: string }[]): string[] {
  return rows.map((row) => row.id);
}

describe("readSessionLineage（后端谱系键的防御性读取）", () => {
  it("完整谱系键映射为来源引用", () => {
    expect(
      readSessionLineage({
        workspace_path: "D:/repo",
        fork_parent_session_id: "session-parent",
        fork_source_message_id: "msg_abc",
        fork_origin_title: "父会话",
      }),
    ).toEqual({
      sessionId: "session-parent",
      anchorMessageId: "msg_abc",
      originTitle: "父会话",
    });
  });

  it("只有父会话键时也算分支会话，可选键不伪造", () => {
    expect(
      readSessionLineage({ fork_parent_session_id: " session-parent " }),
    ).toEqual({ sessionId: "session-parent" });
  });

  it("缺父键 / 空值 / 非对象 context 一律判非分支会话", () => {
    for (const context of [
      {},
      { fork_parent_session_id: "" },
      { fork_parent_session_id: "   " },
      { fork_parent_session_id: 42 },
      { fork_source_message_id: "msg_abc" },
      null,
      undefined,
      "fork_parent_session_id=session-parent",
      ["session-parent"],
    ]) {
      expect(readSessionLineage(context)).toBeUndefined();
    }
  });

  it("行身份优先 sessionId（本地在途线程 id 只是占位）", () => {
    expect(lineageRowIdentity({ id: "local-1", sessionId: "session-1" })).toBe(
      "session-1",
    );
    expect(lineageRowIdentity({ id: "session-2" })).toBe("session-2");
    expect(lineageRowIdentity({ id: "  ", sessionId: " " })).toBe("");
  });

  it("谱系等价判定覆盖三个字段", () => {
    const base = { sessionId: "parent" };
    expect(isSameSessionLineage(base, { sessionId: "parent" })).toBe(true);
    expect(isSameSessionLineage(base, undefined)).toBe(false);
    expect(
      isSameSessionLineage(base, { sessionId: "parent", anchorMessageId: "m" }),
    ).toBe(false);
    expect(isSameSessionLineage(undefined, undefined)).toBe(true);
  });
});

describe("buildLineageIndex（只认父在本批行里的边）", () => {
  it("父在批里时建立父 → 子索引，多子保持输入顺序", () => {
    const index = buildLineageIndex([
      forkRow("child-1", "parent"),
      plainRow("parent"),
      forkRow("child-2", "parent"),
    ]);

    expect(index.get("parent")).toEqual(["child-1", "child-2"]);
  });

  it("父不在批里时不建立边（跨组 / 被过滤 / 已消失）", () => {
    const index = buildLineageIndex([forkRow("child-1", "other-group-parent")]);

    expect(index.size).toBe(0);
  });

  it("自引用与重复子行不产生脏边", () => {
    const index = buildLineageIndex([
      forkRow("self", "self"),
      forkRow("child", "parent"),
      forkRow("child", "parent"),
      plainRow("parent"),
    ]);

    expect(index.size).toBe(1);
    expect(index.get("parent")).toEqual(["child"]);
  });

  it("本地线程 id ≠ 会话 id 时按会话身份建立边", () => {
    const index = buildLineageIndex([
      plainRow("parent", { id: "local-parent" }),
      { id: "local-child", sessionId: "child", forkedFrom: { sessionId: "parent" } },
    ]);

    expect(index.get("parent")).toEqual(["child"]);
  });
});

describe("stabilizeLineageOrder（子行紧随父行）", () => {
  it("父行在子行之后时把子行拉到父行之后", () => {
    const rows = [forkRow("child", "parent"), plainRow("x"), plainRow("parent")];
    const stabilized = stabilizeLineageOrder(rows, buildLineageIndex(rows));

    expect(ids(stabilized)).toEqual(["x", "parent", "child"]);
  });

  it("多子行紧随父行且保持彼此原有相对顺序", () => {
    const rows = [
      forkRow("child-2", "parent"),
      plainRow("x"),
      forkRow("child-1", "parent"),
      plainRow("parent"),
      plainRow("y"),
    ];
    const stabilized = stabilizeLineageOrder(rows, buildLineageIndex(rows));

    expect(ids(stabilized)).toEqual([
      "x",
      "parent",
      "child-2",
      "child-1",
      "y",
    ]);
  });

  it("父行跨组（索引里有父、行集合里没有）时保持原位并返回入参引用", () => {
    const all = [plainRow("parent"), forkRow("child", "parent")];
    const index = buildLineageIndex(all);
    const groupRows = [forkRow("child", "parent"), plainRow("x")];

    const stabilized = stabilizeLineageOrder(groupRows, index);

    expect(stabilized).toBe(groupRows);
    expect(ids(stabilized)).toEqual(["child", "x"]);
  });

  it("父行缺失（索引里也没有）时保持原位", () => {
    const rows = [forkRow("child", "ghost"), plainRow("x")];
    const stabilized = stabilizeLineageOrder(rows, buildLineageIndex(rows));

    expect(stabilized).toBe(rows);
    expect(ids(stabilized)).toEqual(["child", "x"]);
  });

  it("与手动排序冲突：父行与其余行顺序不被改写，只有子行跟随父行", () => {
    const rows = [
      plainRow("manual-1"),
      plainRow("parent"),
      plainRow("manual-2"),
      forkRow("child", "parent"),
    ];
    const stabilized = stabilizeLineageOrder(rows, buildLineageIndex(rows));

    // 父行仍待在用户摆放的位置；子行被拉回父行之后，其余行相对顺序不变。
    expect(ids(stabilized)).toEqual([
      "manual-1",
      "parent",
      "child",
      "manual-2",
    ]);
  });

  it("环形谱系（脏数据）不重排也不丢行", () => {
    const rows = [
      forkRow("a", "b"),
      forkRow("b", "a"),
      plainRow("x"),
    ];
    const stabilized = stabilizeLineageOrder(rows, buildLineageIndex(rows));

    expect(stabilized).toBe(rows);
    expect(ids(stabilized)).toEqual(["a", "b", "x"]);
  });

  it("无父子边时返回入参引用", () => {
    const rows = [plainRow("a"), plainRow("b")];
    expect(stabilizeLineageOrder(rows, buildLineageIndex(rows))).toBe(rows);
  });

  it("折叠口径（约束 1）：子行计入 limit，且可见子行的父行必然可见", () => {
    const rows = [
      plainRow("a"),
      plainRow("b"),
      forkRow("child", "parent"),
      plainRow("parent"),
      plainRow("c"),
      plainRow("d"),
    ];
    const stabilized = stabilizeLineageOrder(rows, buildLineageIndex(rows));
    expect(ids(stabilized)).toEqual([
      "a",
      "b",
      "parent",
      "child",
      "c",
      "d",
    ]);

    for (const limit of [3, 4, 5]) {
      const visible = resolveSessionGroupVisibility(stabilized, {
        expanded: false,
        limit,
      }).visible;
      const parentIndex = ids(visible).indexOf("parent");
      const childIndex = ids(visible).indexOf("child");
      expect(childIndex === -1 || (parentIndex !== -1 && parentIndex < childIndex)).toBe(
        true,
      );
    }
  });
});

describe("resolveRowDepth（父在本批可见 → 1）", () => {
  it("父在索引里时缩进一级", () => {
    const rows = [plainRow("parent"), forkRow("child", "parent")];
    const index = buildLineageIndex(rows);

    expect(resolveRowDepth(rows[1], index)).toBe(1);
  });

  it("父跨组 / 缺失 / 自引用 / 非分支一律 0", () => {
    const index = buildLineageIndex([plainRow("parent"), forkRow("child", "parent")]);

    expect(resolveRowDepth(forkRow("child-2", "other-group"), index)).toBe(0);
    expect(resolveRowDepth(forkRow("child-3", "ghost"), index)).toBe(0);
    expect(resolveRowDepth(forkRow("self", "self"), index)).toBe(0);
    expect(resolveRowDepth(plainRow("parent"), index)).toBe(0);
  });

  it("行自带 forkedFrom（Thread 口径）同样生效", () => {
    const rows = [plainRow("parent"), forkRow("child", "parent")];
    const index = buildLineageIndex(rows);
    const threadRow: SessionLineageRow = {
      id: "child-thread",
      sessionId: "child",
      forkedFrom: { sessionId: "parent", anchorMessageId: "msg_1" },
    };

    expect(resolveRowDepth(threadRow, index)).toBe(1);
  });
});

describe("resolveLineageOriginTitle（徽标提示来源标题）", () => {
  it("优先后端谱系键 originTitle", () => {
    const rows = [forkRow("child", "parent", { forkedFrom: { sessionId: "parent", originTitle: "父会话旧名" } }), plainRow("parent", { title: "父会话新名" })];

    expect(resolveLineageOriginTitle(rows[0], rows)).toBe("父会话旧名");
  });

  it("originTitle 缺失时回落父行标题（title / metadata.title）", () => {
    const rows = [
      forkRow("child-1", "parent-1"),
      forkRow("child-2", "parent-2"),
      plainRow("parent-1", { title: "线程标题" }),
      plainRow("parent-2", { metadata: { title: "快照标题" } }),
    ];

    expect(resolveLineageOriginTitle(rows[0], rows)).toBe("线程标题");
    expect(resolveLineageOriginTitle(rows[1], rows)).toBe("快照标题");
  });

  it("父行不在批里且无 originTitle 时不伪造标题", () => {
    const rows = [forkRow("child", "ghost")];
    expect(resolveLineageOriginTitle(rows[0], rows)).toBeUndefined();
    expect(resolveLineageOriginTitle(plainRow("plain"), rows)).toBeUndefined();
  });
});

describe("stabilizeLineageOrder · 手动排序锚点（§5.5 约束 3）", () => {
  it("手动账目锚定的子行不被移动，未锚定的子行仍紧随父行", () => {
    const rows = [
      forkRow("child-free", "parent"),
      forkRow("child-anchored", "parent"),
      plainRow("x"),
      plainRow("parent"),
    ];

    const stabilized = stabilizeLineageOrder(rows, buildLineageIndex(rows), {
      anchoredIds: new Set(["child-anchored"]),
    });

    // 用户显式摆过的子行保持原位（显式顺序优先）；未锚定的子行被拉回父行之后。
    expect(ids(stabilized)).toEqual([
      "child-anchored",
      "x",
      "parent",
      "child-free",
    ]);
  });

  it("父行被手动移到别处时，其非锚定子簇跟随", () => {
    const rows = [
      forkRow("child", "parent"),
      plainRow("manual-1"),
      plainRow("parent"),
      plainRow("manual-2"),
    ];

    const stabilized = stabilizeLineageOrder(rows, buildLineageIndex(rows), {
      anchoredIds: new Set(["parent"]),
    });

    expect(ids(stabilized)).toEqual([
      "manual-1",
      "parent",
      "child",
      "manual-2",
    ]);
  });

  it("全量账目（整组顺序都由用户显式摆过）时不重排并返回入参引用", () => {
    const rows = [
      plainRow("a"),
      forkRow("child", "parent"),
      plainRow("parent"),
    ];

    const stabilized = stabilizeLineageOrder(rows, buildLineageIndex(rows), {
      anchoredIds: new Set(["a", "child", "parent"]),
    });

    expect(stabilized).toBe(rows);
    expect(ids(stabilized)).toEqual(["a", "child", "parent"]);
  });

  it("自动排序（不传锚点 / 空锚点集合）时与既有口径一致", () => {
    const buildRows = () => [
      forkRow("child", "parent"),
      plainRow("x"),
      plainRow("parent"),
    ];

    const withoutOptions = stabilizeLineageOrder(
      buildRows(),
      buildLineageIndex(buildRows()),
    );
    const emptyAnchors = stabilizeLineageOrder(
      buildRows(),
      buildLineageIndex(buildRows()),
      { anchoredIds: new Set() },
    );

    // 自动模式不读账目：子行照旧紧随父行。
    expect(ids(withoutOptions)).toEqual(["x", "parent", "child"]);
    expect(ids(emptyAnchors)).toEqual(ids(withoutOptions));
  });
});
