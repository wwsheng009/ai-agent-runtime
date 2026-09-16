// 文件浏览器路径纯函数单测：分隔符归一化、面包屑、绝对路径展示、类型/语言判定。

import { describe, expect, it } from "vitest";

import {
  buildBreadcrumb,
  entryNameFromPath,
  fileExtension,
  formatEntryMtime,
  guessPrismLanguage,
  isEnterableDirectory,
  isImagePath,
  isMarkdownPath,
  isSvgPath,
  joinRelativePath,
  normalizeRelativePath,
  parentRelativePath,
  splitRelativeSegments,
  toAbsoluteDisplayPath,
} from "@/lib/file-browser/path-utils";
import type { FsEntry } from "@/types/runtime/fs-browser";

function entry(partial: Partial<FsEntry> & { name: string }): FsEntry {
  return { path: partial.name, type: "file", size: 1, mtime: 1, ...partial };
}

describe("normalizeRelativePath", () => {
  it("统一反斜杠、去掉首尾分隔符与 `.` 段", () => {
    expect(normalizeRelativePath("\\src\\components/./tree-list.tsx")).toBe(
      "src/components/tree-list.tsx",
    );
    expect(normalizeRelativePath("/")).toBe("");
    expect(normalizeRelativePath(".")).toBe("");
    expect(normalizeRelativePath("src//lib")).toBe("src/lib");
  });

  it("不解析 `..`（越界判定属后端职责，前端不猜测合法化）", () => {
    expect(normalizeRelativePath("../secret")).toBe("../secret");
    expect(normalizeRelativePath("a/../../b")).toBe("a/../../b");
  });
});

describe("路径拼接与拆分", () => {
  it("根目录下拼接不产生前导斜杠", () => {
    expect(joinRelativePath("", "src")).toBe("src");
    expect(joinRelativePath("/", "src")).toBe("src");
    expect(joinRelativePath("src", "lib")).toBe("src/lib");
  });

  it("拆分为父路径与末段名", () => {
    expect(splitRelativeSegments("src/lib/utils.ts")).toEqual(["src", "lib", "utils.ts"]);
    expect(parentRelativePath("src/lib/utils.ts")).toBe("src/lib");
    expect(parentRelativePath("utils.ts")).toBe("");
    expect(entryNameFromPath("src/lib/utils.ts")).toBe("utils.ts");
    expect(entryNameFromPath("")).toBe("");
  });
});

describe("buildBreadcrumb", () => {
  it("根段 path 为空串，后续段为可回跳的相对路径", () => {
    expect(buildBreadcrumb("ai-agent-runtime", "frontend/src")).toEqual([
      { name: "ai-agent-runtime", path: "" },
      { name: "frontend", path: "frontend" },
      { name: "src", path: "frontend/src" },
    ]);
  });

  it("根目录只有一段；根名缺失时保持空串而不是占位文案", () => {
    expect(buildBreadcrumb("", "")).toEqual([{ name: "", path: "" }]);
  });
});

describe("toAbsoluteDisplayPath", () => {
  it("按根路径的分隔符风格拼接（Windows / POSIX）", () => {
    expect(toAbsoluteDisplayPath("E:\\projects\\ai", "frontend/src")).toBe(
      "E:\\projects\\ai\\frontend\\src",
    );
    expect(toAbsoluteDisplayPath("/home/me/repo", "src/lib")).toBe(
      "/home/me/repo/src/lib",
    );
    expect(toAbsoluteDisplayPath("E:\\projects\\ai\\", "")).toBe("E:\\projects\\ai");
  });

  it("根路径缺失时返回空串（不伪造盘符）", () => {
    expect(toAbsoluteDisplayPath("", "src")).toBe("");
  });
});

describe("类型与语言判定", () => {
  it("markdown / 图片判定只认扩展名", () => {
    expect(isMarkdownPath("docs/README.md")).toBe(true);
    expect(isMarkdownPath("docs/README.MARKDOWN")).toBe(true);
    expect(isMarkdownPath("docs/readme.txt")).toBe(false);
    expect(isImagePath("assets/logo.PNG")).toBe(true);
    expect(isImagePath("assets/logo.ts")).toBe(false);
    expect(isSvgPath("assets/logo.svg")).toBe(true);
  });

  it("扩展名推导：无扩展名 / 隐藏文件 / 末尾点都返回空串", () => {
    expect(fileExtension("Makefile")).toBe("");
    expect(fileExtension(".gitignore")).toBe("");
    expect(fileExtension("archive.")).toBe("");
    expect(fileExtension("src/main.TS")).toBe(".ts");
  });

  it("Prism 语言未知时回落 text（不猜语法）", () => {
    expect(guessPrismLanguage("src/a.ts")).toBe("typescript");
    expect(guessPrismLanguage("scripts/build.ps1")).toBe("powershell");
    expect(guessPrismLanguage("logs/run.log")).toBe("text");
    expect(guessPrismLanguage("Makefile")).toBe("text");
  });

  it("只有 dir 可进入；unknown / symlink 不得当成目录", () => {
    expect(isEnterableDirectory(entry({ name: "src", type: "dir" }))).toBe(true);
    expect(isEnterableDirectory(entry({ name: "mystery", type: "unknown" }))).toBe(false);
    expect(isEnterableDirectory(entry({ name: "link", type: "symlink" }))).toBe(false);
  });
});

describe("formatEntryMtime", () => {
  it("-1 / 0 / 非法值按「无时间」呈现，不伪造 1970", () => {
    expect(formatEntryMtime(-1)).toBe("");
    expect(formatEntryMtime(0)).toBe("");
    expect(formatEntryMtime(Number.NaN)).toBe("");
    expect(formatEntryMtime(new Date(2026, 8, 16, 3, 4).getTime())).toBe(
      "2026-09-16 03:04",
    );
  });
});
