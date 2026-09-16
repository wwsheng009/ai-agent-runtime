// 文件浏览器纯函数（一）：作用域相对路径归一化、面包屑、类型/语言判定、时间展示。
//
// 后端契约（types/runtime/fs-browser.ts）：
//   * `path` 一律相对作用域根，分隔符统一 `/`，绝对路径由后端拒绝（path_must_be_relative）；
//   * `ext` 可能缺省，因此预览分流所需的扩展名在本地从 `name` 兜底推导（仅用于选择渲染器）；
//   * 绝对路径只用于展示与「复制绝对路径」，请求永远用 `scope` + 相对 `path`。
//
// 归一化纪律：
//   * 入参先做 `\` → `/`、合并重复 `/`、去首尾 `/`、丢弃 `.` 段；
//   * **不**解析 `..`（越界判定属后端职责，前端不猜测合法化）；
//   * 空字符串 = 作用域根，是合法值而不是错误。
//
// 降级判据：
//   * 扩展名未知 → 语言回落 `text`（纯文本，不高亮、不猜语法）；
//   * 只有 `type === "dir"` 可展开；`symlink` / `unknown` 一律不当目录（不臆造语义）。

import type { FsEntry, FsRoot } from "@/types/runtime/fs-browser";

/** 归一化相对路径：`/` 分隔、无首尾 `/`、无 `.` 段；根目录为 `""`。 */
export function normalizeRelativePath(path: string): string {
  return path
    .replace(/\\/g, "/")
    .split("/")
    .map((segment) => segment.trim())
    .filter((segment) => segment.length > 0 && segment !== ".")
    .join("/");
}

export function splitRelativeSegments(path: string): string[] {
  const normalized = normalizeRelativePath(path);
  return normalized ? normalized.split("/") : [];
}

export function parentRelativePath(path: string): string {
  const segments = splitRelativeSegments(path);
  segments.pop();
  return segments.join("/");
}

/** 目录 + 项名 → 相对路径；`dir` 为空表示作用域根。 */
export function joinRelativePath(dir: string, name: string): string {
  const base = normalizeRelativePath(dir);
  const child = normalizeRelativePath(name);
  if (!child) {
    return base;
  }
  return base ? `${base}/${child}` : child;
}

export function entryNameFromPath(path: string): string {
  const segments = splitRelativeSegments(path);
  return segments[segments.length - 1] ?? "";
}

export type BreadcrumbSegment = {
  /** 展示名（根为 `FsRoot.name` 或作用域标签）。 */
  name: string;
  /** 该段对应的相对路径；根为 `""`。 */
  path: string;
};

/** 由根名 + 当前相对路径切分面包屑；不额外请求后端。 */
export function buildBreadcrumb(
  rootName: string,
  currentPath: string,
): BreadcrumbSegment[] {
  const segments: BreadcrumbSegment[] = [
    { name: rootName || "", path: "" },
  ];
  let accumulated = "";
  for (const segment of splitRelativeSegments(currentPath)) {
    accumulated = accumulated ? `${accumulated}/${segment}` : segment;
    segments.push({ name: segment, path: accumulated });
  }
  return segments;
}

/**
 * 展示用绝对路径：以后端给出的绝对根路径（`FsRoot.path`）为前缀。
 * 只用于「复制绝对路径」与提示；根路径缺失时返回空串（不伪造盘符）。
 */
export function toAbsoluteDisplayPath(
  rootPath: string,
  relativePath: string,
): string {
  const root = rootPath.trim();
  if (!root) {
    return "";
  }
  const segments = splitRelativeSegments(relativePath);
  const trimmedRoot = root.replace(/[\\/]+$/, "");
  if (segments.length === 0) {
    return trimmedRoot || root;
  }
  const separator = root.includes("\\") ? "\\" : "/";
  return `${trimmedRoot}${separator}${segments.join(separator)}`;
}

/** 扩展名（小写，含前导点；无扩展名返回空串）。 */
export function fileExtension(path: string): string {
  const name = entryNameFromPath(path);
  const dot = name.lastIndexOf(".");
  if (dot <= 0 || dot === name.length - 1) {
    return "";
  }
  return name.slice(dot).toLowerCase();
}

const MARKDOWN_EXTENSIONS = new Set([".md", ".markdown", ".mdx"]);

export function isMarkdownPath(path: string): boolean {
  return MARKDOWN_EXTENSIONS.has(fileExtension(path));
}

const IMAGE_EXTENSIONS = new Set([".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp"]);

export function isImagePath(path: string): boolean {
  return IMAGE_EXTENSIONS.has(fileExtension(path));
}

/** SVG 只允许 <img src="data:..."> 载入（不内联注入 DOM，避免脚本面）。 */
export function isSvgPath(path: string): boolean {
  return fileExtension(path) === ".svg";
}

const PRISM_LANGUAGE_BY_EXTENSION: Record<string, string> = {
  ".ts": "typescript",
  ".tsx": "tsx",
  ".js": "javascript",
  ".jsx": "jsx",
  ".mjs": "javascript",
  ".cjs": "javascript",
  ".json": "json",
  ".md": "markdown",
  ".markdown": "markdown",
  ".go": "go",
  ".py": "python",
  ".ps1": "powershell",
  ".psm1": "powershell",
  ".sh": "bash",
  ".bash": "bash",
  ".sql": "sql",
  ".yml": "yaml",
  ".yaml": "yaml",
  ".css": "css",
  ".html": "markup",
  ".htm": "markup",
  ".xml": "markup",
  ".svg": "markup",
  ".diff": "diff",
  ".patch": "diff",
};

/** 扩展名 → Prism 语言 id；未知回落 `text`（纯文本，不猜语法）。 */
export function guessPrismLanguage(path: string): string {
  return PRISM_LANGUAGE_BY_EXTENSION[fileExtension(path)] ?? "text";
}

/** 目录判定只看 `type`；`symlink` / `unknown` 不得当成目录进入（不臆造）。 */
export function isEnterableDirectory(entry: FsEntry): boolean {
  return entry.type === "dir";
}

export function isHiddenEntryName(name: string): boolean {
  return name.startsWith(".");
}

/** `/fs/roots` 不可用时的合成根：只用当前会话工作目录（不猜工作区目录列表）。
 *
 * 降级判据：404/405/501/503（isFsRootsUnavailable）才走这里；`exists` 以「会话声明了工作目录」
 * 为准——前端无法探测文件系统，因此不做伪探测，`probeError` 如实标注降级原因。
 */
export function buildFallbackRoot(sessionId: string, workspacePath?: string): FsRoot {
  const session = sessionId.trim();
  const path = (workspacePath ?? "").trim();
  const scope = session ? `session:${session}` : "cwd";
  return {
    scope,
    kind: session ? "session" : "cwd",
    name: path ? entryNameFromPath(normalizeRelativePath(path)) : scope,
    path,
    exists: path.length > 0,
    isGitRepo: false,
    probeError: "fs_roots_unavailable",
  };
}

const TWO_DIGITS = (value: number) => String(value).padStart(2, "0");

/**
 * 展示用修改时间。后端 `mtime` 缺失时约定为 -1（探测失败），此处如实显示「—」，
 * 不把 -1 渲染成 1970 年（避免用假数据充数）。
 */
export function formatEntryMtime(mtime: number): string {
  if (!Number.isFinite(mtime) || mtime <= 0) {
    return "";
  }
  const date = new Date(mtime);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  return (
    `${date.getFullYear()}-${TWO_DIGITS(date.getMonth() + 1)}-${TWO_DIGITS(date.getDate())}` +
    ` ${TWO_DIGITS(date.getHours())}:${TWO_DIGITS(date.getMinutes())}`
  );
}
