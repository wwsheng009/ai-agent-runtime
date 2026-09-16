// 文件树的「行投影」纯函数层（虚拟列表渲染行 = entry 行 + 合成状态行）。
//
// 与 entry-sort.ts 的分工：那一层产出 **entry 行**（展开集合 + 已加载层 → 树行）；
// 这一层在其结果上插入 **合成状态行**（加载更多 / 游离态重载），只做投影、不发请求、不读时钟。
//
// 独立成模块的原因：组件文件（tree-list.tsx）只应导出组件，非组件的导出会被
// `react-refresh/only-export-components` 拦下；这里的投影逻辑本身可单测（见 tree-list.test.tsx）。

import type { FileBrowserListing } from "@/hooks/workspace/use-file-browser";
import type { TreeRow } from "@/lib/file-browser/entry-sort";
import { parentRelativePath } from "@/lib/file-browser/path-utils";
import type { FsEntry } from "@/types/runtime/fs-browser";

/** 合成行：不来自后端，只表达「本层还能加载更多 / 这层需要重新加载」。 */
export type FileTreeStatusRow = {
  kind: "status";
  status: "more" | "orphan" | "loading";
  path: string;
  depth: number;
  level: number;
  posInSet: number;
  setSize: number;
};

export type FileTreeRenderRow = { kind: "entry"; row: TreeRow } | FileTreeStatusRow;

export type BuildRenderRowsOptions = {
  rows: readonly TreeRow[];
  expanded: ReadonlySet<string>;
  listings: Readonly<Record<string, FileBrowserListing>>;
};

/**
 * 行投影的第二步：在纯函数产出的 entry 行之间插入合成状态行。
 *
 * 合成行语义（都排在所属目录子树的最后，等价于「最后一个兄弟」）：
 *   * `more` / `loading`：该层还有下一页（`has_more`），入口是「加载更多」；
 *   * `orphan`：目录处于展开态但列表已失效（刷新丢缓存 / 读取失败 / 被移动），入口是「重新加载该层」。
 *
 * 计数修订：合成行也是同层兄弟，因此该层已渲染行的 aria-setsize 一并 +1，避免读屏报出「3 / 2」这类矛盾数字。
 */
export function buildRenderRows(options: BuildRenderRowsOptions): FileTreeRenderRow[] {
  const { expanded, listings, rows } = options;
  const source: Record<string, readonly FsEntry[]> = {};
  for (const [path, listing] of Object.entries(listings)) {
    if (listing.status === "ready" || listing.status === "loading_more") {
      source[path] = listing.entries;
    }
  }
  const childrenCount = new Map<string, number>();
  const syntheticStatus = new Map<string, FileTreeStatusRow["status"]>();
  // 根层（path === ""）没有自己的行，但它的分页同样必须可见：先登记（供同层计数 +1），循环结束再补一行。
  const rootListing = listings[""];
  if (rootListing?.hasMore === true) {
    syntheticStatus.set("", rootListing.status === "loading_more" ? "loading" : "more");
  }
  for (const row of rows) {
    const parent = parentRelativePath(row.entry.path);
    childrenCount.set(parent, (childrenCount.get(parent) ?? 0) + 1);
    const listing = listings[row.entry.path];
    const orphan = row.entry.type === "dir" && expanded.has(row.entry.path) && source[row.entry.path] === undefined;
    if (orphan) {
      syntheticStatus.set(row.entry.path, "orphan");
    } else if (listing?.hasMore === true) {
      syntheticStatus.set(row.entry.path, listing.status === "loading_more" ? "loading" : "more");
    }
  }

  const out: FileTreeRenderRow[] = [];
  // 开放目录栈：栈顶是当前行的目录 frame（其子行 depth = frame.depth + 1）。
  const open: { depth: number; synthetic: FileTreeStatusRow | null }[] = [];
  const flushClosed = (depth: number) => {
    while (open.length > 0 && open[open.length - 1].depth >= depth) {
      const frame = open.pop();
      if (frame?.synthetic) {
        out.push(frame.synthetic);
      }
    }
  };
  for (const row of rows) {
    flushClosed(row.depth);
    const parent = parentRelativePath(row.entry.path);
    out.push({ kind: "entry", row: syntheticStatus.has(parent) ? { ...row, setSize: row.setSize + 1 } : row });
    const siblings = (childrenCount.get(row.entry.path) ?? 0) + 1;
    const status = syntheticStatus.get(row.entry.path);
    open.push({
      depth: row.depth,
      synthetic: status
        ? { kind: "status", status, path: row.entry.path, depth: row.depth + 1, level: row.level + 1, posInSet: siblings, setSize: siblings }
        : null,
    });
  }
  flushClosed(0);
  const rootStatus = syntheticStatus.get("");
  if (rootStatus) {
    const siblings = (childrenCount.get("") ?? 0) + 1;
    out.push({ kind: "status", status: rootStatus, path: "", depth: 0, level: 1, posInSet: siblings, setSize: siblings });
  }
  return out;
}
