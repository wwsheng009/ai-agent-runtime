// P0-2 随源拆分：GitDiffView 单测共享夹具（由 diff-view.test.tsx 抽出，仅搬迁不改语义）。
import { vi } from "vitest";

import type { GitSnapshot } from "@/hooks/workspace/use-git-changes";
import type { GitDiffHunk, GitDiffLine, GitDiffLineType, GitDiffResult } from "@/types/runtime/git-browse";

import { GitDiffView } from "./diff-view";

export function line(type: GitDiffLineType, oldNo: number | null, newNo: number | null, text: string): GitDiffLine {
  return { type, oldNo, newNo, text };
}

export function hunk(lines: GitDiffLine[], oldStart = 1, newStart = 1): GitDiffHunk {
  return {
    header: `@@ -${oldStart},2 +${newStart},2 @@`,
    oldStart,
    newStart,
    oldLines: lines.filter((item) => item.type !== "add").length,
    newLines: lines.filter((item) => item.type !== "del").length,
    lines,
  };
}

const CHANGE_HUNK = hunk([
  line("del", 1, null, "const a = 1;"),
  line("add", null, 1, "const a = 2;"),
  line("context", 2, 2, "export {};"),
]);

export function diffResult(overrides: Partial<GitDiffResult> = {}): GitDiffResult {
  return {
    file: { path: "src/app.ts", status: "M", isBinary: false, isSubmodule: false },
    target: "working",
    effectiveTarget: "working",
    targetFallback: false,
    context: 3,
    whitespace: "show",
    insertions: 1,
    deletions: 1,
    hunks: [CHANGE_HUNK],
    raw: "--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1,2 +1,2 @@\n-const a = 1;\n+const a = 2;\n",
    parseError: "",
    truncated: false,
    truncatedReason: "",
    generatedAt: 0,
    ...overrides,
  };
}

export function snapshot(overrides: Partial<GitSnapshot<GitDiffResult>> = {}): GitSnapshot<GitDiffResult> {
  return { status: "ready", data: diffResult(), error: null, unavailable: false, ...overrides };
}

export function defaultProps(overrides: Partial<Parameters<typeof GitDiffView>[0]> = {}) {
  return {
    snapshot: snapshot(),
    selectedPath: "src/app.ts",
    stale: false,
    targetLabel: "工作区",
    fallbackTargetLabel: null,
    mode: "unified" as const,
    onModeChange: vi.fn(),
    whitespace: "show" as const,
    onWhitespaceChange: vi.fn(),
    canExpandContext: true,
    onExpandContext: vi.fn(),
    rowLimit: 2000,
    onShowMoreRows: vi.fn(),
    onRetry: vi.fn(),
    ...overrides,
  };
}
