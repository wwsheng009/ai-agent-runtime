// 「计划归档」面的纯函数（与 artifact-panel-shared.ts 同定位）：
// 组件文件只导出组件（react-refresh/only-export-components），状态/决策的文案键与摘要
// 逻辑放这里，便于单测直接钉住边界。

import type {
  RuntimePlanDiffLine,
  RuntimePlanDiffLineKind,
  RuntimeStoredPlan,
} from "@/types/runtime";

/** 备注摘要上限：列表里只给判读片段，完整备注不展开（阅读面不抢正文位置）。 */
export const ROUND_NOTES_SUMMARY_MAX = 140;

/** 动态文案键前缀（`t()` 需要完整键路径，不能只给叶子名）。 */
const PLAN_I18N_PREFIX = "panels.artifacts.plans.";

export const STORED_PLAN_STATUS_I18N_KEYS: Record<string, string> = {
  pending: "pending",
  approved: "approved",
  not_implemented: "notImplemented",
};

const PLAN_DECISION_I18N_KEYS: Record<string, string> = {
  approve: "approve",
  request_changes: "requestChanges",
  quit: "quit",
  enter: "enter",
};

/** 状态徽标配色：待评审=橙（待办）、已批准=青（通过）、未实施=灰（终态）。 */
export function storedPlanStatusClass(status: string) {
  switch (status) {
    case "pending":
      return "border-accent-orange/30 bg-accent-orange/10 text-accent-orange";
    case "approved":
      return "border-accent-teal/30 bg-accent-teal/10 text-accent-teal";
    case "not_implemented":
      return "border-white/12 bg-white/5 text-muted-foreground";
    default:
      return undefined;
  }
}

/** 未知状态不猜语义：原文透传（后端新增状态时不至于显示成空白）。 */
export function storedPlanStatusLabel(status: string) {
  const normalized = status.trim();
  if (!normalized) {
    return "—";
  }
  return STORED_PLAN_STATUS_I18N_KEYS[normalized]
    ? `${PLAN_I18N_PREFIX}status.${STORED_PLAN_STATUS_I18N_KEYS[normalized]}`
    : normalized;
}

export function storedPlanDecisionLabel(decision?: string) {
  const normalized = decision?.trim();
  if (!normalized) {
    return "—";
  }
  return PLAN_DECISION_I18N_KEYS[normalized]
    ? `${PLAN_I18N_PREFIX}decisions.${PLAN_DECISION_I18N_KEYS[normalized]}`
    : normalized;
}

export function summarizeRoundNotes(notes?: string) {
  const normalized = notes?.replace(/\s+/g, " ").trim() ?? "";
  if (normalized.length <= ROUND_NOTES_SUMMARY_MAX) {
    return normalized;
  }
  return `${normalized.slice(0, ROUND_NOTES_SUMMARY_MAX - 1)}…`;
}

export function formatStoredPlanProject(plan: RuntimeStoredPlan) {
  return plan.project_slug?.trim() || plan.project?.trim() || plan.project_path?.trim() || "";
}

export function formatStoredPlanTitle(plan: RuntimeStoredPlan) {
  return plan.title?.trim() || plan.plan_path?.trim() || plan.id;
}

/**
 * 把统一 diff 文本切成带语义的行，供渲染层上色。
 *
 * 前两行按框架行（`--- v1 …` / `+++ v3 …`）识别 —— 只按首个字符判断会把以 `--`
 * 开头的删除行误判成框架行；`@@` 是 hunk 头，`\` 是「无末尾换行」标记，其余按标记符分类。
 */
export function classifyPlanDiffLines(text: string): RuntimePlanDiffLine[] {
  const lines = text.length === 0 ? [] : text.split("\n");
  if (lines.length > 0 && lines[lines.length - 1] === "") {
    lines.pop();
  }
  // hunk 头给出两边的起始行号，之后按标记符推进——评论锚点要用**正文本行号**，
  // 而不是 diff 文本里的行序（后者含框架行 / hunk 头 / 删除行）。
  let oldLine = 0;
  let newLine = 0;
  return lines.map((line, index) => {
    const kind = classifyPlanDiffLine(line, index);
    const entry: RuntimePlanDiffLine = { kind, text: line };
    switch (kind) {
      case "hunk": {
        const hunk = parsePlanDiffHunkHeader(line);
        oldLine = hunk?.oldStart ?? 0;
        newLine = hunk?.newStart ?? 0;
        return entry;
      }
      case "meta":
        return entry;
      case "context":
        entry.oldLine = oldLine++;
        entry.newLine = newLine++;
        return entry;
      case "del":
        entry.oldLine = oldLine++;
        return entry;
      default:
        entry.newLine = newLine++;
        return entry;
    }
  });
}

/** 解析 `@@ -old,count +new,count @@`；格式不符返回 null（后端渲染的 hunk 头永远合规）。 */
export function parsePlanDiffHunkHeader(
  line: string,
): { oldStart: number; newStart: number } | null {
  const match = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(line.trim());
  if (!match) {
    return null;
  }
  return { oldStart: Number(match[1]), newStart: Number(match[2]) };
}

/** 行号区间文案：`L12` / `L12-14`（与 CLI、后端 FormatCommentRange 同一口径）。 */
export function formatPlanCommentRange(start: number, end: number) {
  return end <= start ? `L${start}` : `L${start}-${end}`;
}

const PLAN_COMMENT_STATUS_I18N_KEYS: Record<string, string> = {
  anchored: "anchored",
  moved: "moved",
  orphaned: "orphaned",
};

/** 重放状态文案；未知状态原文透传。 */
export function planCommentStatusLabel(status: string) {
  const normalized = status.trim();
  if (!normalized) {
    return "—";
  }
  return PLAN_COMMENT_STATUS_I18N_KEYS[normalized]
    ? `${PLAN_I18N_PREFIX}comments.status.${PLAN_COMMENT_STATUS_I18N_KEYS[normalized]}`
    : normalized;
}

/** 状态配色：仍锚定=青（可对齐）、已移动=橙（要看一眼）、失效=灰（保留原文）。 */
export function planCommentStatusClass(status: string) {
  switch (status) {
    case "anchored":
      return "border-accent-teal/30 bg-accent-teal/10 text-accent-teal";
    case "moved":
      return "border-accent-orange/30 bg-accent-orange/10 text-accent-orange";
    case "orphaned":
      return "border-white/12 bg-white/5 text-muted-foreground";
    default:
      return undefined;
  }
}

function classifyPlanDiffLine(line: string, index: number): RuntimePlanDiffLineKind {
  if ((index === 0 && line.startsWith("---")) || (index === 1 && line.startsWith("+++"))) {
    return "meta";
  }
  if (line.startsWith("@@")) {
    return "hunk";
  }
  if (line.startsWith("\\")) {
    return "meta";
  }
  if (line.startsWith("+")) {
    return "add";
  }
  if (line.startsWith("-")) {
    return "del";
  }
  return "context";
}

/** 每类 diff 行的着色：新增走 teal，删除走 orange，hunk/框架行走弱化文本。 */
export function planDiffLineClass(kind: RuntimePlanDiffLineKind) {
  switch (kind) {
    case "add":
      return "text-accent-teal";
    case "del":
      return "text-accent-orange";
    case "hunk":
      return "text-muted-foreground";
    case "meta":
      return "text-foreground/70";
    default:
      return "text-muted-foreground/80";
  }
}
