// 文件浏览器纯函数（二）：目录项排序 / 过滤、树展开成行、虚拟列表窗口计算。
//
// 后端契约（types/runtime/fs-browser.ts）：
//   * `sort` 由服务端定义（FsSortKey），客户端排序只用于「同一层已加载项」的本地重排；
//   * 分页语义：`next_cursor` + `has_more` 由服务端给出，缺省按「无更多」处理；
//   * `truncated=true` 表示服务端单层截断，UI 必须显式提示，不能当成完整列表排序展示。
//
// 归一化纪律：
//   * 排序**稳定**：同键一律以 name 升序兜底，避免每次渲染顺序抖动；
//   * `size`/`mtime` 为 -1 表示后端探测失败 → 排在有效值之后，不当作 0 参与比较；
//   * 过滤只作用于已加载项（不做全树搜索，也不触发新请求）；
//   * `internal === true` 的项（上传分片临时目录等）在行集合中直接剔除。
//
// 降级判据：
//   * 未知 sort key → 回落 `type_then_name`；
//   * 只有 `type === "dir"` 参与展开递归，`unknown` / `symlink` 不进入子层。

import type { FsEntry, FsSortKey } from "@/types/runtime/fs-browser";

/** 固定行高（px）：虚拟滚动切片与占位块都以此为准（§4.3）。 */
export const FILE_TREE_ROW_HEIGHT = 28;
/** 上下各多渲染的缓冲行数（减少快速滚动白屏）。 */
export const FILE_TREE_OVERSCAN = 6;

const SORT_KEYS: readonly FsSortKey[] = [
  "name_asc",
  "name_desc",
  "mtime_desc",
  "size_desc",
  "type_then_name",
];

export const DEFAULT_SORT_KEY: FsSortKey = "type_then_name";

export function normalizeSortKey(value: unknown): FsSortKey {
  return typeof value === "string" && SORT_KEYS.includes(value as FsSortKey)
    ? (value as FsSortKey)
    : DEFAULT_SORT_KEY;
}

export const FS_SORT_KEY_OPTIONS: readonly FsSortKey[] = SORT_KEYS;

function compareName(left: string, right: string): number {
  return left.localeCompare(right, "en", { numeric: true, sensitivity: "base" });
}

/** `-1` 是「探测失败」哨兵，参与比较时排在有效值之后。 */
function compareMetricDesc(left: number, right: number): number {
  const leftValid = Number.isFinite(left) && left > 0;
  const rightValid = Number.isFinite(right) && right > 0;
  if (!leftValid && !rightValid) {
    return 0;
  }
  if (!leftValid) {
    return 1;
  }
  if (!rightValid) {
    return -1;
  }
  return right - left;
}

export function compareEntries(
  left: FsEntry,
  right: FsEntry,
  sortKey: FsSortKey,
  dirsFirst = true,
): number {
  if (dirsFirst) {
    const leftDir = left.type === "dir" ? 0 : 1;
    const rightDir = right.type === "dir" ? 0 : 1;
    if (leftDir !== rightDir) {
      return leftDir - rightDir;
    }
  }
  switch (sortKey) {
    case "name_desc": {
      const byName = compareName(right.name, left.name);
      return byName !== 0 ? byName : compareMetricDesc(left.mtime, right.mtime);
    }
    case "mtime_desc": {
      const byTime = compareMetricDesc(left.mtime, right.mtime);
      return byTime !== 0 ? byTime : compareName(left.name, right.name);
    }
    case "size_desc": {
      const bySize = compareMetricDesc(left.size, right.size);
      return bySize !== 0 ? bySize : compareName(left.name, right.name);
    }
    case "type_then_name": {
      const byType = compareName(left.type, right.type);
      return byType !== 0 ? byType : compareName(left.name, right.name);
    }
    default: {
      const byName = compareName(left.name, right.name);
      return byName !== 0 ? byName : compareMetricDesc(left.mtime, right.mtime);
    }
  }
}

export function sortEntries(
  entries: readonly FsEntry[],
  sortKey: FsSortKey,
  dirsFirst = true,
): FsEntry[] {
  return [...entries].sort((left, right) =>
    compareEntries(left, right, sortKey, dirsFirst),
  );
}

export type EntryFilter = {
  /** 前缀/包含匹配，大小写不敏感；空串表示不过滤。 */
  text?: string;
  showHidden?: boolean;
};

export function filterEntries(
  entries: readonly FsEntry[],
  filter: EntryFilter,
): FsEntry[] {
  const needle = (filter.text ?? "").trim().toLowerCase();
  return entries.filter((entry) => {
    if (entry.internal === true) {
      return false;
    }
    if (!filter.showHidden && entry.name.startsWith(".")) {
      return false;
    }
    if (!needle) {
      return true;
    }
    return entry.name.toLowerCase().includes(needle);
  });
}

export type TreeListingSource = Readonly<Record<string, readonly FsEntry[]>>;

export type TreeRow = {
  entry: FsEntry;
  /** 0 起算的缩进深度。 */
  depth: number;
  /** a11y 用的 1 起算层级（aria-level）。 */
  level: number;
  /** 同层兄弟总数（aria-setsize，虚拟滚动下必须提供）。 */
  setSize: number;
  /** 同层内 1 起算位置（aria-posinset）。 */
  posInSet: number;
  expanded: boolean;
  /** 展开箭头是否可点：未知层（尚未加载）也允许尝试展开。 */
  expandable: boolean;
};

export type BuildTreeRowsOptions = {
  /** 目录相对路径 → 原始项（含 internal / 隐藏项，过滤在行构建内进行）。 */
  source: TreeListingSource;
  /** 根层的相对路径，默认 `""`（作用域根）。 */
  rootPath?: string;
  expanded: ReadonlySet<string> | readonly string[];
  sortKey?: FsSortKey;
  dirsFirst?: boolean;
  filter?: EntryFilter;
};

function expandableDir(source: TreeListingSource, path: string): boolean {
  const children = source[path];
  return children === undefined || children.length > 0;
}

/**
 * 已加载层 + 展开集合 → 行集合（深度优先，父行在子行之前）。
 *
 * 过滤语义：文本过滤**只作用于已加载层**，但目录只要「自身命中或其已加载子树命中」
 * 就会被保留，并在过滤激活时临时展开已加载的子层（只读缓存，不发请求）；未加载的子层
 * 不会被猜成命中，也不会被自动请求。
 */
export function buildTreeRows(options: BuildTreeRowsOptions): TreeRow[] {
  const expanded =
    options.expanded instanceof Set
      ? options.expanded
      : new Set(options.expanded as readonly string[]);
  const sortKey = options.sortKey ?? DEFAULT_SORT_KEY;
  const dirsFirst = options.dirsFirst !== false;
  const needle = (options.filter?.text ?? "").trim().toLowerCase();
  const filterActive = needle.length > 0;

  const visit = (dirPath: string, depth: number): TreeRow[] => {
    const raw = options.source[dirPath];
    if (!raw) {
      return [];
    }
    const candidates = sortEntries(
      filterEntries(raw, { showHidden: options.filter?.showHidden }),
      sortKey,
      dirsFirst,
    );
    const prepared = candidates.map((entry) => {
      const canExpand = entry.type === "dir";
      const childrenLoaded = options.source[entry.path] !== undefined;
      const shouldExpand =
        canExpand && (expanded.has(entry.path) || (filterActive && childrenLoaded));
      return {
        entry,
        children: shouldExpand ? visit(entry.path, depth + 1) : [],
        keepsByFilter: !filterActive || entry.name.toLowerCase().includes(needle),
      };
    });
    const kept = prepared.filter(
      (item) => item.keepsByFilter || item.children.length > 0,
    );
    const rows: TreeRow[] = [];
    kept.forEach((item, index) => {
      rows.push({
        entry: item.entry,
        depth,
        level: depth + 1,
        setSize: kept.length,
        posInSet: index + 1,
        expanded: item.children.length > 0,
        expandable:
          item.entry.type === "dir" && expandableDir(options.source, item.entry.path),
      });
      rows.push(...item.children);
    });
    return rows;
  };

  return visit(options.rootPath ?? "", 0);
}

export type VirtualWindowInput = {
  itemCount: number;
  scrollTop: number;
  viewportHeight: number;
  rowHeight?: number;
  overscan?: number;
};

export type VirtualWindow = {
  start: number;
  /** 开区间末端（用于 `rows.slice(start, end)`）。 */
  end: number;
  /** 窗口内应渲染的行数（含 overscan）。 */
  windowSize: number;
  topPadding: number;
  bottomPadding: number;
  totalHeight: number;
};

/** 固定行高 + 可视窗口切片：窗口外的行不渲染，只保留上下占位高度。 */
export function computeVirtualWindow(input: VirtualWindowInput): VirtualWindow {
  const rowHeight = input.rowHeight ?? FILE_TREE_ROW_HEIGHT;
  const overscan = input.overscan ?? FILE_TREE_OVERSCAN;
  const itemCount = Math.max(0, Math.floor(input.itemCount));
  const totalHeight = itemCount * rowHeight;
  if (itemCount === 0 || rowHeight <= 0) {
    return {
      start: 0,
      end: 0,
      windowSize: 0,
      topPadding: 0,
      bottomPadding: 0,
      totalHeight,
    };
  }
  const scrollTop = Math.max(0, Math.min(input.scrollTop, Math.max(0, totalHeight - rowHeight)));
  const firstVisible = Math.floor(scrollTop / rowHeight);
  const visibleCount = Math.max(1, Math.ceil(Math.max(0, input.viewportHeight) / rowHeight));
  const start = Math.max(0, Math.min(firstVisible - overscan, Math.max(0, itemCount - 1)));
  const end = Math.min(itemCount, start + visibleCount + overscan * 2);
  return {
    start,
    end,
    windowSize: end - start,
    topPadding: start * rowHeight,
    bottomPadding: (itemCount - end) * rowHeight,
    totalHeight,
  };
}
