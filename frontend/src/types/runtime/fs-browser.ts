// 文件浏览器与传输（P0/P1/P2）的运行时类型契约。
//
// 后端契约（backend/internal/filebrowse + backend/internal/api/skills/fs_browser_handlers.go）：
//   作用域 scope = workspace:<directory_id> | session:<session_id> | cwd
//   路径 path 一律**相对作用域根**；绝对路径由后端拒绝（path_must_be_relative）。
//
// 归一化纪律（与 api/runtime/fs-*.ts 一致）：
//   * 缺失数组字段按空数组收口；未知字段忽略；
//   * 数值字段非有限时按缺省（-1 表示后端探测失败，不用 0 伪装）；
//   * `type` 只允许白名单，未知值收口为 "unknown"（不猜测语义）。

/** 作用域根：`scope` 直接回传给后续请求；`path` 仅用于展示与「复制绝对路径」。 */
export type FsRootKind = "workspace" | "session" | "cwd";

export type FsRoot = {
  scope: string;
  kind: FsRootKind;
  name: string;
  /** 绝对路径（仅展示用；请求必须用 scope + 相对 path）。 */
  path: string;
  exists: boolean;
  isGitRepo: boolean;
  gitRoot?: string;
  /** 仓库探测失败原因（可选，失败不报错）。 */
  probeError?: string;
};

export type FsEntryType = "dir" | "file" | "symlink" | "inaccessible" | "unknown";

export type FsEntry = {
  name: string;
  /** 相对作用域根的路径（分隔符统一为 `/`）。 */
  path: string;
  type: FsEntryType;
  size: number;
  mtime: number;
  ext?: string;
  isText?: boolean;
  isSymlink?: boolean;
  /** 上传分片目录等内部项（后端标记），UI 需要隐藏或标注。 */
  internal?: boolean;
};

export type FsListingDir = {
  path: string;
  absPath: string;
  parent: string;
  isRoot: boolean;
};

export type FsSortKey =
  | "name_asc"
  | "name_desc"
  | "mtime_desc"
  | "size_desc"
  | "type_then_name";

export type FsListingResult = {
  dir: FsListingDir;
  entries: FsEntry[];
  nextCursor: string | null;
  hasMore: boolean;
  /** 服务端因单层项数上限主动截断（必须显式提示，不静默）。 */
  truncated: boolean;
  sort: FsSortKey;
};

export type FsListingRequest = {
  scope: string;
  path?: string;
  cursor?: string | null;
  limit?: number;
  sort?: FsSortKey;
  showHidden?: boolean;
  dirsFirst?: boolean;
};

export type FsPreviewKind = "text" | "image" | "binary" | "too_large" | "unknown";

export type FsPreview = {
  kind: FsPreviewKind;
  path: string;
  absPath?: string;
  size: number;
  mtime?: number;
  mime?: string;
  /** text：截断后的文本（已解码）；image：base64（不含 data: 前缀）。 */
  text?: string;
  dataBase64?: string;
  truncated: boolean;
  /** 二进制/超限的判定原因（如 nul-byte / invalid-utf8 / too_large）。 */
  reason?: string;
  /** 后端给出的上限，超限时用于展示真实大小与阈值。 */
  limitBytes?: number;
};

export type FsPreviewRequest = {
  scope: string;
  path: string;
  maxBytes?: number;
};

export type FsDownloadProbe = {
  size: number;
  etag: string;
  acceptRanges: boolean;
  contentType: string;
};

export type FsConflictPolicy = "fail" | "overwrite" | "rename";

export type FsUploadInitRequest = {
  scope: string;
  dir: string;
  name: string;
  size: number;
  sha256?: string;
  chunkSize?: number;
  conflictPolicy?: FsConflictPolicy;
};

export type FsUploadInitResult = {
  uploadId: string;
  offset: number;
  received: number;
  chunkSize: number;
  expiresAt: number;
  /** 秒传：目标已存在且内容一致。 */
  completed: boolean;
  deduplicated: boolean;
  targetPath: string;
  targetExists: boolean;
};

export type FsUploadChunkResult = {
  received: number;
  offset: number;
};

export type FsUploadStatus = {
  uploadId: string;
  received: number;
  offset: number;
  chunkSize: number;
  size: number;
  expiresAt: number;
  targetPath: string;
  conflictPolicy: FsConflictPolicy;
};

export type FsUploadCompleteResult = {
  path: string;
  size: number;
  action: "create" | "overwrite" | "rename" | "unknown";
  sha256?: string;
  elapsedMs?: number;
};

/** 传输进度（上传/下载共用；字节数来自服务端确认值，不用「已发送」充数）。 */
export type FsTransferProgress = {
  direction: "upload" | "download";
  transferred: number;
  total: number;
  speedBytesPerSecond?: number;
};

// —— P1：跨目录模糊搜索（GET /api/runtime/fs/search） ——
//
// 后端契约（backend/internal/filebrowse/search.go，规划 §4.4.1）：
//   query: scope, q(字面匹配，≤256 rune), path(相对根，可空), cursor, limit(≤50),
//          show_hidden, kinds(file|dir|both), max_depth(≤16), max_scan(≤50000), budget_ms(≤1000)
//   响应体：{ scope, query, base, items[], next_cursor?, has_more, scanned,
//            truncated, truncated_reason?, elapsed_ms, limit }
//
// 归一化纪律（与 fs-list.ts 同款）：
//   * `items` 非数组 → 抛错（不伪装成空结果）；
//   * `next_cursor` 非字符串/空串 → null；`has_more`/`truncated` 缺省 false；
//   * 缺 `type` 或未知 type → "unknown"（不猜成 file/dir）；
//   * size/mtime 非有限数 → -1；未知 `truncated_reason` 收口丢弃（不猜归因）。

/** 搜索的条目类型过滤；composer 只用 `file`，面板用 `both`。 */
export type FsSearchKind = "file" | "dir" | "both";

/** 命中位置（`start` 含、`end` 不含，单位是**显示字符串的 rune 偏移**），供 UI 高亮。 */
export type FsSearchMatch = {
  field: "name" | "path";
  start: number;
  end: number;
};

export type FsSearchItem = {
  name: string;
  /** 相对作用域根（分隔符统一 `/`）。 */
  path: string;
  type: FsEntryType;
  size: number;
  mtime: number;
  ext?: string;
  /** 相关度分值；空查询首屏为 0（不做打分）。 */
  score: number;
  match?: FsSearchMatch;
};

/** `truncated` 为真时的归因枚举（规划 §4.4.1）：区分预算耗尽与深度截断。 */
export type FsSearchTruncatedReason = "depth" | "scan" | "budget" | "dir_entries";

export type FsSearchResult = {
  scope: string;
  query: string;
  base: string;
  items: FsSearchItem[];
  nextCursor: string | null;
  /** 按相关度还有下一页可翻；与 `truncated` 正交（四象限都合法）。 */
  hasMore: boolean;
  /** 本次实际扫描的条目数（仅可观测性，不参与前端逻辑）。 */
  scanned: number;
  /** 结果可能不完备（深度/扫描量/预算/单目录枚举上限）。 */
  truncated: boolean;
  truncatedReasons: FsSearchTruncatedReason[];
  elapsedMs: number;
  limit: number;
};

export type FsSearchRequest = {
  scope: string;
  query?: string;
  /** 相对作用域根的基础目录；空/缺省 = 整个作用域。 */
  path?: string;
  cursor?: string | null;
  limit?: number;
  showHidden?: boolean;
  kinds?: FsSearchKind;
  maxDepth?: number;
  maxScan?: number;
  budgetMs?: number;
};
