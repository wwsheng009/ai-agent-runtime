// @vitest-environment jsdom

// diff 视图单测：诚实纪律（解析失败降级 / 截断证据 / 二进制不渲染 / 无改动 ≠ 空）+ 交互（模式、折叠、上下文、预算）。
//
// 断言口径：DOM 上的 `data-*` 承载**后端原始字段与计数**（parse_error / truncated_reason / 行数），
// 因此断言核对的是「原样透传」，而不是前端加工后的文案。

import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { i18n } from "@/i18n";
import { zhWorkspacePanelsGit } from "@/i18n/resources/zh-CN/workspace/panels-git";
import type { GitSnapshot } from "@/hooks/workspace/use-git-changes";
import { DEFAULT_DIFF_ROW_LIMIT, buildDiffRows } from "@/lib/git/diff-view-model";
import type {
  GitDiffHunk,
  GitDiffLine,
  GitDiffLineType,
  GitDiffResult,
} from "@/types/runtime/git-browse";

import { GitDiffView } from "./diff-view";
import { RAW_TEXT_LINE_CAP } from "./diff-hunk";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function line(type: GitDiffLineType, oldNo: number | null, newNo: number | null, text: string): GitDiffLine {
  return { type, oldNo, newNo, text };
}

function hunk(lines: GitDiffLine[], oldStart = 1, newStart = 1): GitDiffHunk {
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

function diffResult(overrides: Partial<GitDiffResult> = {}): GitDiffResult {
  return {
    file: { path: "src/app.ts", status: "M", isBinary: false, isSubmodule: false },
    target: "working",
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

function snapshot(overrides: Partial<GitSnapshot<GitDiffResult>> = {}): GitSnapshot<GitDiffResult> {
  return { status: "ready", data: diffResult(), error: null, unavailable: false, ...overrides };
}

function defaultProps(overrides: Partial<Parameters<typeof GitDiffView>[0]> = {}) {
  return {
    snapshot: snapshot(),
    selectedPath: "src/app.ts",
    stale: false,
    targetLabel: "工作区",
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

describe("GitDiffView", () => {
  let roots: Root[] = [];

  beforeEach(() => {
    i18n.addResourceBundle("zh-CN", "workspace", { panels: { git: zhWorkspacePanelsGit } }, true, false);
    if (i18n.language !== "zh-CN") {
      void i18n.changeLanguage("zh-CN");
    }
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    for (const root of roots) {
      act(() => root.unmount());
    }
    roots = [];
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
    vi.restoreAllMocks();
  });

  function mount(node: ReactNode) {
    const container = document.createElement("div");
    document.body.appendChild(container);
    const root = createRoot(container);
    act(() => root.render(node));
    roots.push(root);
    return container;
  }

  it("parse_error 非空 → 降级为纯文本原始 diff，且绝不显示「没有差异」", () => {
    const raw = "diff --git a/x b/x\n@@ broken @@\n";
    const container = mount(
      <GitDiffView
        {...defaultProps({
          snapshot: snapshot({
            data: diffResult({ parseError: "unexpected hunk header", raw, hunks: [] }),
          }),
        })}
      />,
    );

    expect(container.querySelector('[data-parse-error="unexpected hunk header"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="git-diff-no-changes"]')).toBeNull();
    expect(container.querySelector('[data-testid="git-diff-rows"]')).toBeNull();

    const rawPane = container.querySelector('[data-testid="git-diff-raw"]');
    expect(rawPane?.textContent).toContain("@@ broken @@");
    expect(rawPane?.getAttribute("data-raw-lines")).toBe("3");
  });

  it("原始文本超过展示上限时提示按上限截断，且不丢上限之外的行数信息", () => {
    const raw = Array.from({ length: RAW_TEXT_LINE_CAP + 25 }, (_, index) => `line ${index}`).join("\n");
    const container = mount(
      <GitDiffView
        {...defaultProps({
          snapshot: snapshot({ data: diffResult({ parseError: "boom", raw, hunks: [] }) }),
        })}
      />,
    );
    const rawPane = container.querySelector('[data-testid="git-diff-raw"]');
    expect(rawPane?.getAttribute("data-raw-lines")).toBe(String(RAW_TEXT_LINE_CAP + 25));
    expect(rawPane?.getAttribute("data-raw-shown")).toBe(String(RAW_TEXT_LINE_CAP));
    expect(container.textContent).toContain("原始文本按当前渲染上限截断显示。");
  });

  it("truncated → 显示截断行数与 truncated_reason，并提供复制 / 下载 .patch", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const container = mount(
      <GitDiffView
        {...defaultProps({
          snapshot: snapshot({
            data: diffResult({ truncated: true, truncatedReason: "max_bytes" }),
          }),
        })}
      />,
    );

    const banner = container.querySelector('[data-testid="git-diff-truncated"]');
    expect(banner?.getAttribute("data-truncated-reason")).toBe("max_bytes");
    // raw 共 5 行（末尾换行不计入）。
    expect(banner?.getAttribute("data-truncated-lines")).toBe("5");
    expect(banner?.textContent).toContain("已截断");

    const [copyButton, downloadButton] = [...(banner?.querySelectorAll("button") ?? [])];
    await act(async () => {
      copyButton.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await Promise.resolve();
    });
    expect(writeText).toHaveBeenCalledWith(diffResult().raw);
    expect(banner?.textContent).toContain("已复制到剪贴板");
    expect(downloadButton.textContent).toContain("下载 .patch");
  });

  it("truncated 但后端未给原因 → 明确写「未提供 truncated_reason」而不是省略", () => {
    const container = mount(
      <GitDiffView
        {...defaultProps({ snapshot: snapshot({ data: diffResult({ truncated: true }) }) })}
      />,
    );
    const banner = container.querySelector('[data-testid="git-diff-truncated"]');
    expect(banner?.getAttribute("data-truncated-reason")).toBe("");
    expect(banner?.textContent).toContain("truncated_reason");
  });

  it("二进制文件 → 只给结论与统计口径，不渲染任何 diff 行", () => {
    const container = mount(
      <GitDiffView
        {...defaultProps({
          snapshot: snapshot({
            data: diffResult({
              file: { path: "logo.png", status: "M", isBinary: true, isSubmodule: false },
              insertions: -1,
              deletions: -1,
              hunks: [],
              raw: "",
            }),
          }),
        })}
      />,
    );
    const banner = container.querySelector('[data-testid="git-diff-binary"]');
    expect(banner?.getAttribute("data-binary-stats")).toBe("unavailable");
    expect(banner?.textContent).toContain("二进制文件，已变更");
    expect(banner?.textContent).toContain("不猜测大小变化");
    expect(container.querySelector('[data-testid="git-diff-rows"]')).toBeNull();
    expect(container.querySelector('[data-testid="git-diff-no-changes"]')).toBeNull();
  });

  it("无 hunks 且无解析失败 → 才显示「该对比目标下没有差异」", () => {
    const container = mount(
      <GitDiffView
        {...defaultProps({
          snapshot: snapshot({ data: diffResult({ hunks: [], raw: "", insertions: 0, deletions: 0 }) }),
        })}
      />,
    );
    expect(container.querySelector('[data-testid="git-diff-no-changes"]')?.textContent).toContain(
      "没有差异",
    );
  });

  it("unified 渲染行号 + 前缀 + 文本；点击模式按钮回调 split", () => {
    const onModeChange = vi.fn();
    const container = mount(<GitDiffView {...defaultProps({ onModeChange })} />);

    const rows = container.querySelectorAll('[role="row"]');
    expect(rows.length).toBeGreaterThan(0);
    expect([...container.querySelectorAll("[data-diff-cell]")].map((cell) => cell.getAttribute("data-diff-cell"))).toContain(
      "add",
    );
    expect(container.textContent).toContain("const a = 2;");

    const splitButton = [...container.querySelectorAll("button")].find((button) =>
      button.textContent?.includes("并排视图"),
    );
    act(() => {
      splitButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onModeChange).toHaveBeenCalledWith("split");
  });

  it("split 视图按 old/new 配对，缺侧为占位且不复制对侧文本", () => {
    const container = mount(
      <GitDiffView
        {...defaultProps({
          mode: "split",
          snapshot: snapshot({
            data: diffResult({
              hunks: [
                hunk([line("del", 7, null, "old-only"), line("context", 8, 9, "ctx")]),
                hunk([line("del", 20, null, "pair-old"), line("add", null, 22, "pair-new")]),
              ],
            }),
          }),
        })}
      />,
    );

    const cells = [...container.querySelectorAll('[data-diff-cell="del"], [data-diff-cell="add"]')];
    expect(cells[0].textContent).toContain("old-only");
    expect(cells[0].textContent).not.toContain("pair-new");
    expect(cells[1].textContent).toContain("pair-old");
    expect(cells[1].textContent).not.toContain("pair-new");
    expect(cells[2].textContent).toContain("pair-new");
    expect(cells[2].textContent).not.toContain("pair-old");
    // 缺侧用 empty 占位，而不是复制对侧内容或编造行号。
    const empties = [...container.querySelectorAll('[data-diff-cell="empty"]')];
    expect(empties.length).toBeGreaterThan(0);
    expect(empties.every((cell) => (cell.textContent ?? "").trim() === "")).toBe(true);
    const rows = [...container.querySelectorAll('[role="row"]')];
    expect(rows.some((row) => row.getAttribute("aria-label")?.includes("替换行"))).toBe(true);
  });

  it("hunk 折叠只影响渲染，展开入口用更大 context 重新请求", () => {
    const onExpandContext = vi.fn();
    const container = mount(<GitDiffView {...defaultProps({ onExpandContext })} />);

    const toggle = container.querySelector('[data-diff-hunk-header="expanded"] button');
    expect(toggle).not.toBeNull();
    act(() => {
      toggle?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(container.querySelector('[data-diff-hunk-header="collapsed"]')).not.toBeNull();
    expect(container.textContent).toContain("已折叠 3 行");
    expect(container.textContent).not.toContain("const a = 2;");

    const expand = [...container.querySelectorAll("button")].find((button) =>
      button.textContent?.includes("展开"),
    );
    act(() => {
      expand?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onExpandContext).toHaveBeenCalled();
  });

  it("上下文到上限 → 隐藏展开入口并给出上限说明（不发无效请求）", () => {
    const container = mount(<GitDiffView {...defaultProps({ canExpandContext: false })} />);
    expect(container.querySelector('[data-testid="git-diff-context-limit"]')).not.toBeNull();
    const labels = [...container.querySelectorAll("button")].map((button) => button.textContent ?? "");
    expect(labels.some((label) => label.includes("展开"))).toBe(false);
  });

  it("渲染预算截断 → 显示已渲染/总行数并给「继续加载」（不改写服务端结论）", () => {
    const onShowMoreRows = vi.fn();
    const container = mount(<GitDiffView {...defaultProps({ rowLimit: 4, onShowMoreRows })} />);

    const banner = container.querySelector('[data-testid="git-diff-row-limit"]');
    expect(banner?.getAttribute("data-row-limit-shown")).toBe("4");
    expect(banner?.getAttribute("data-row-limit-total")).toBe("5");
    expect(banner?.textContent).toContain("前 4 行");

    const button = [...(banner?.querySelectorAll("button") ?? [])][0];
    act(() => {
      button.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onShowMoreRows).toHaveBeenCalledTimes(1);
  });

  it("未选文件 / 加载中 / 失败三态各自可解释，失败给重试", () => {
    const onRetry = vi.fn();
    const empty = mount(<GitDiffView {...defaultProps({ selectedPath: null })} />);
    expect(empty.querySelector('[data-testid="git-diff-empty"]')).not.toBeNull();

    const loading = mount(
      <GitDiffView {...defaultProps({ snapshot: { status: "loading", data: null, error: null, unavailable: false } })} />,
    );
    expect(loading.querySelector('[data-testid="git-diff-loading"]')).not.toBeNull();

    const failed = mount(
      <GitDiffView
        {...defaultProps({
          onRetry,
          snapshot: { status: "error", data: null, error: new Error("network"), unavailable: false },
        })}
      />,
    );
    const errorBox = failed.querySelector('[data-testid="git-diff-error"]');
    expect(errorBox?.textContent).toContain("network");
    act(() => {
      errorBox?.querySelector("button")?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("空白开关只回调、不本地过滤（由服务端重算）", () => {
    const onWhitespaceChange = vi.fn();
    const container = mount(<GitDiffView {...defaultProps({ onWhitespaceChange })} />);
    const toggle = [...container.querySelectorAll("button")].find((button) =>
      button.textContent?.includes("空白差异"),
    );
    act(() => {
      toggle?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onWhitespaceChange).toHaveBeenCalledWith("ignore_all");
  });

  it("2 万行 diff：首屏只渲染视口窗口 + 渲染预算封顶，模型与挂载耗时可控", () => {
    // 200 个 hunk × 100 行 ≈ 20 000 行：同一份数据既跑模型计时，也跑首屏挂载计时。
    const bulkHunks = Array.from({ length: 200 }, (_, h) => {
      const base = h * 100;
      return hunk(
        [
          line("del", base + 1, null, `old-${h}`),
          line("add", null, base + 1, `new-${h}`),
          ...Array.from({ length: 98 }, (_, i) =>
            line("context", base + i + 2, base + i + 2, `ctx-${h}-${i}`),
          ),
        ],
        base + 2,
        base + 2,
      );
    });

    const modelStarted = performance.now();
    const modelled = buildDiffRows(bulkHunks, { mode: "unified" });
    const modelMs = performance.now() - modelStarted;
    expect(modelled.length).toBeGreaterThan(DEFAULT_DIFF_ROW_LIMIT);

    const mountStarted = performance.now();
    const container = mount(
      <GitDiffView
        {...defaultProps({ snapshot: snapshot({ data: diffResult({ hunks: bulkHunks }) }) })}
      />,
    );
    const mountMs = performance.now() - mountStarted;

    // 视口渲染 + hunk 级虚拟化：DOM 行数只与可视窗口有关，与 2 万行总长无关。
    const renderedRows = container.querySelectorAll("[data-virtual-row-index]").length;
    expect(renderedRows).toBeLessThan(200);
    // 渲染预算仍是 2000 行（不是全量），必须给「继续加载」入口。
    expect(container.querySelector('[data-testid="git-diff-row-limit"]')).not.toBeNull();
    expect(mountMs).toBeLessThan(1500);
    console.info(
      `[perf] 20k 行 diff：模型构建 ${modelMs.toFixed(1)}ms，首屏挂载 ${mountMs.toFixed(1)}ms，DOM 行 ${renderedRows}`,
    );
  });
});
