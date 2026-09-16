// 文件浏览器排序/过滤/树展开/虚拟窗口纯函数单测。
// 虚拟列表的「游离态」在此覆盖：窗口外的行不进入切片，只留上下占位高度。

import { describe, expect, it } from "vitest";

import {
  FILE_TREE_OVERSCAN,
  FILE_TREE_ROW_HEIGHT,
  buildTreeRows,
  compareEntries,
  computeVirtualWindow,
  filterEntries,
  normalizeSortKey,
  sortEntries,
} from "@/lib/file-browser/entry-sort";
import type { FsEntry } from "@/types/runtime/fs-browser";

function entry(partial: Partial<FsEntry> & { name: string; path?: string }): FsEntry {
  return {
    path: partial.path ?? partial.name,
    type: "file",
    size: 1,
    mtime: 1,
    ...partial,
  };
}

describe("normalizeSortKey", () => {
  it("白名单外（含 undefined / 数字）一律回落 type_then_name", () => {
    expect(normalizeSortKey("mtime_desc")).toBe("mtime_desc");
    expect(normalizeSortKey("size_asc")).toBe("type_then_name");
    expect(normalizeSortKey(undefined)).toBe("type_then_name");
  });
});

describe("sortEntries", () => {
  const entries = [
    entry({ name: "zeta.ts", mtime: 100 }),
    entry({ name: "Alpha.ts", mtime: 300 }),
    entry({ name: "src", type: "dir", size: -1, mtime: -1 }),
  ];

  it("目录优先 + name_asc 使用数字感知比较", () => {
    const sorted = sortEntries(
      [entry({ name: "file10.txt" }), entry({ name: "file2.txt" })],
      "name_asc",
    );
    expect(sorted.map((item) => item.name)).toEqual(["file2.txt", "file10.txt"]);
    expect(sortEntries(entries, "name_asc").map((item) => item.name)).toEqual([
      "src",
      "Alpha.ts",
      "zeta.ts",
    ]);
  });

  it("mtime/size 为 -1（探测失败）时排在有效值之后，且不修改入参顺序", () => {
    const input = [
      entry({ name: "unknown", mtime: -1 }),
      entry({ name: "fresh", mtime: 500 }),
    ];
    const sorted = sortEntries(input, "mtime_desc", false);
    expect(sorted.map((item) => item.name)).toEqual(["fresh", "unknown"]);
    expect(input.map((item) => item.name)).toEqual(["unknown", "fresh"]);
  });

  it("同键时以 name 升序兜底，保证稳定", () => {
    const sorted = sortEntries(
      [
        entry({ name: "b.txt", size: 10, path: "b" }),
        entry({ name: "a.txt", size: 10, path: "a" }),
      ],
      "size_desc",
      false,
    );
    expect(sorted.map((item) => item.name)).toEqual(["a.txt", "b.txt"]);
  });

  it("compareEntries 对相同项返回 0", () => {
    const item = entry({ name: "same" });
    expect(compareEntries(item, item, "type_then_name")).toBe(0);
  });
});

describe("filterEntries", () => {
  const entries = [
    entry({ name: ".env" }),
    entry({ name: "src", type: "dir" }),
    entry({ name: "upload.part", internal: true }),
    entry({ name: "README.md" }),
  ];

  it("默认隐藏点文件与内部项，过滤只作用于已加载层", () => {
    expect(filterEntries(entries, {}).map((item) => item.name)).toEqual(["src", "README.md"]);
    expect(filterEntries(entries, { showHidden: true }).map((item) => item.name)).toEqual([
      ".env",
      "src",
      "README.md",
    ]);
  });

  it("文本过滤大小写不敏感且不触发外部数据", () => {
    expect(
      filterEntries(entries, { text: "read" }).map((item) => item.name),
    ).toEqual(["README.md"]);
    expect(filterEntries(entries, { text: "missing" })).toEqual([]);
  });
});

describe("buildTreeRows", () => {
  const source = {
    "": [
      entry({ name: "src", path: "src", type: "dir" }),
      entry({ name: "mystery", path: "mystery", type: "unknown" }),
      entry({ name: "README.md", path: "README.md" }),
    ],
    src: [
      entry({ name: "lib", path: "src/lib", type: "dir" }),
      entry({ name: "main.ts", path: "src/main.ts" }),
    ],
  };

  it("折叠时只出顶层，展开后按深度优先插入子层", () => {
    // 排序固定为 name_asc，避免断言依赖 `type_then_name` 的类型字典序。
    const collapsed = buildTreeRows({ source, expanded: [], sortKey: "name_asc" });
    expect(collapsed.map((row) => [row.entry.path, row.depth, row.level])).toEqual([
      ["src", 0, 1],
      ["mystery", 0, 1],
      ["README.md", 0, 1],
    ]);

    const expanded = buildTreeRows({
      source,
      expanded: new Set(["src"]),
      sortKey: "name_asc",
    });
    expect(expanded.map((row) => row.entry.path)).toEqual([
      "src",
      "src/lib",
      "src/main.ts",
      "mystery",
      "README.md",
    ]);
    expect(expanded[1]?.level).toBe(2);
  });

  it("aria-setsize / aria-posinset 按同层兄弟计算", () => {
    const rows = buildTreeRows({ source, expanded: ["src"], sortKey: "name_asc" });
    expect(rows[0]?.setSize).toBe(3);
    expect(rows[0]?.posInSet).toBe(1);
    expect(rows[2]?.setSize).toBe(2);
    expect(rows[2]?.posInSet).toBe(2);
  });

  it("unknown 类型不参与展开；未加载子层也允许尝试展开", () => {
    const rows = buildTreeRows({
      source,
      expanded: new Set(["mystery"]),
      sortKey: "name_asc",
    });
    expect(rows.map((row) => row.entry.path)).toEqual(["src", "mystery", "README.md"]);
    const mystery = rows.find((row) => row.entry.path === "mystery");
    expect(mystery?.expandable).toBe(false);
    const unloaded = buildTreeRows({
      source: { "": [entry({ name: "empty", path: "empty", type: "dir" })] },
      expanded: [],
      sortKey: "name_asc",
    });
    expect(unloaded[0]?.expandable).toBe(true);
    const knownEmpty = buildTreeRows({
      source: { "": [entry({ name: "empty", path: "empty", type: "dir" })], empty: [] },
      expanded: [],
      sortKey: "name_asc",
    });
    expect(knownEmpty[0]?.expandable).toBe(false);
  });

  it("过滤只作用于已加载层：目录因已加载子树命中而保留", () => {
    const rows = buildTreeRows({
      source,
      expanded: ["src"],
      filter: { text: "lib" },
      sortKey: "name_asc",
    });
    expect(rows.map((row) => row.entry.path)).toEqual(["src", "src/lib"]);
  });

  it("过滤命中未加载目录名时保留该目录，但不请求也不猜测子层", () => {
    const rows = buildTreeRows({
      source: {
        "": [entry({ name: "node_modules", path: "node_modules", type: "dir" })],
      },
      expanded: [],
      filter: { text: "node" },
      sortKey: "name_asc",
    });
    expect(rows.map((row) => row.entry.path)).toEqual(["node_modules"]);
    expect(rows[0]?.expanded).toBe(false);
  });
});

describe("computeVirtualWindow", () => {
  it("顶部：start=0，按视口行数 + overscan 切片，底部留占位", () => {
    const window = computeVirtualWindow({
      itemCount: 1000,
      scrollTop: 0,
      viewportHeight: 280,
    });
    expect(FILE_TREE_ROW_HEIGHT).toBe(28);
    expect(window.start).toBe(0);
    expect(window.end).toBe(10 + FILE_TREE_OVERSCAN * 2);
    expect(window.topPadding).toBe(0);
    expect(window.bottomPadding).toBe((1000 - window.end) * FILE_TREE_ROW_HEIGHT);
    expect(window.totalHeight).toBe(1000 * FILE_TREE_ROW_HEIGHT);
  });

  it("中部：窗口跟随滚动位置，上下都有占位（游离行不渲染）", () => {
    const window = computeVirtualWindow({
      itemCount: 1000,
      scrollTop: 2800,
      viewportHeight: 280,
      overscan: 2,
    });
    expect(window.start).toBe(98);
    expect(window.end).toBe(98 + 10 + 4);
    expect(window.topPadding).toBe(98 * FILE_TREE_ROW_HEIGHT);
    expect(window.bottomPadding).toBe((1000 - window.end) * FILE_TREE_ROW_HEIGHT);
    const rows = Array.from({ length: 1000 }, (_, index) => index);
    expect(rows.slice(window.start, window.end)).toHaveLength(window.windowSize);
    expect(rows.slice(window.start, window.end)).not.toContain(0);
  });

  it("越界滚动被夹到最后一屏；空列表整体为 0", () => {
    const clamped = computeVirtualWindow({
      itemCount: 50,
      scrollTop: 999999,
      viewportHeight: 280,
      overscan: 0,
    });
    expect(clamped.end).toBe(50);
    expect(clamped.bottomPadding).toBe(0);
    expect(clamped.start).toBeLessThanOrEqual(49);

    const empty = computeVirtualWindow({ itemCount: 0, scrollTop: 0, viewportHeight: 280 });
    expect(empty).toEqual({
      start: 0,
      end: 0,
      windowSize: 0,
      topPadding: 0,
      bottomPadding: 0,
      totalHeight: 0,
    });
  });
});
