import { describe, expect, it } from "vitest";

import {
  flattenSessionDirectoryGroups,
  isSessionGroupingMode,
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
});
