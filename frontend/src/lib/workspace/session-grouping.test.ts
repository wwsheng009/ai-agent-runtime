import { describe, expect, it } from "vitest";

import {
  flattenSessionDirectoryGroups,
  isSessionGroupingMode,
  resolveSessionGroupVisibility,
  SESSION_GROUP_VISIBLE_LIMIT,
  SESSION_GROUPING_MODES,
} from "./session-grouping";

describe("session-grouping", () => {
  it("只认两种分组视图，未知取值不算合法", () => {
    for (const mode of SESSION_GROUPING_MODES) {
      expect(isSessionGroupingMode(mode)).toBe(true);
    }
    for (const value of ["", "Directory", "grouped", null, 1, undefined]) {
      expect(isSessionGroupingMode(value)).toBe(false);
    }
  });

  it("平铺序列按分组顺序 → 组内顺序拼接", () => {
    const groups = [
      { key: "b", sessions: [{ id: "b1" }, { id: "b2" }] },
      { key: "a", sessions: [{ id: "a1" }] },
      { key: "empty", sessions: [] },
    ];

    expect(flattenSessionDirectoryGroups(groups).map((item) => item.id)).toEqual([
      "b1",
      "b2",
      "a1",
    ]);
  });

  it("空分组集合返回空序列", () => {
    expect(flattenSessionDirectoryGroups([])).toEqual([]);
  });

  describe("resolveSessionGroupVisibility（P2-6 子片 3：组内展开 / 折叠）", () => {
    const many = Array.from({ length: 7 }, (_, index) => ({
      id: `s${index + 1}`,
    }));

    it("不超出上限：整体可见、不给控件、保持入参引用", () => {
      const sessions = many.slice(0, SESSION_GROUP_VISIBLE_LIMIT);
      const result = resolveSessionGroupVisibility(sessions, {
        expanded: false,
      });

      expect(result.visible).toBe(sessions);
      expect(result.hiddenCount).toBe(0);
      expect(result.collapsible).toBe(false);
      expect(result.expanded).toBe(true);
    });

    it("折叠态只呈现前 limit 个，其余计入隐藏计数", () => {
      const result = resolveSessionGroupVisibility(many, { expanded: false });

      expect(result.visible.map((session) => session.id)).toEqual([
        "s1",
        "s2",
        "s3",
        "s4",
        "s5",
      ]);
      expect(result.hiddenCount).toBe(2);
      expect(result.collapsible).toBe(true);
      expect(result.expanded).toBe(false);
    });

    it("展开态整体可见且隐藏计数归零", () => {
      const result = resolveSessionGroupVisibility(many, { expanded: true });

      expect(result.visible).toBe(many);
      expect(result.hiddenCount).toBe(0);
      expect(result.collapsible).toBe(true);
      expect(result.expanded).toBe(true);
    });

    it("选中会话落在折叠区时自动展开，不出现「已选中却看不见」", () => {
      const result = resolveSessionGroupVisibility(many, {
        expanded: false,
        pinnedId: "s7",
      });

      expect(result.visible).toHaveLength(7);
      expect(result.expanded).toBe(true);
      expect(result.hiddenCount).toBe(0);
    });

    it("选中会话在可见区内时不触发展开，折叠计数不变", () => {
      const result = resolveSessionGroupVisibility(many, {
        expanded: false,
        pinnedId: "s2",
      });

      expect(result.visible).toHaveLength(SESSION_GROUP_VISIBLE_LIMIT);
      expect(result.hiddenCount).toBe(2);
      expect(result.expanded).toBe(false);
    });

    it("支持自定义上限，空组整体可见且不给控件", () => {
      expect(
        resolveSessionGroupVisibility(many, { expanded: false, limit: 2 }).visible.map(
          (session) => session.id,
        ),
      ).toEqual(["s1", "s2"]);

      const empty = resolveSessionGroupVisibility([], { expanded: false });
      expect(empty.visible).toEqual([]);
      expect(empty.collapsible).toBe(false);
    });
  });
});
