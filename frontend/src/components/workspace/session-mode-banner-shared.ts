// §4.6 Web 侧「常驻模式标识」的纯函数：tone 映射、文案键解析、plan 状态读法。
//
// 逻辑放这里而不是组件里，是为了让单测直接钉住边界（未知模式、plan 空快照、
// pending_exit_request 优先于「已就绪」等），组件文件只负责渲染。

import type { RuntimeSessionPlanMode } from "@/lib/runtime-api";

export type SessionModeBannerTone = "plan" | "danger" | "neutral";

/** 与 composer 权限控件同一口径的 canonical 值；未知值不写死文案、回落后端原文。 */
const KNOWN_MODE_VALUES = new Set([
  "accept_edits",
  "bypass_permissions",
  "default",
  "plan",
]);

export function sessionModeBannerTone(mode: string): SessionModeBannerTone {
  if (mode === "plan") {
    return "plan";
  }
  if (mode === "bypass_permissions") {
    return "danger";
  }
  return "neutral";
}

/** 已知模式的文案键；未知模式返回 null（调用方回落后端原文，避免枚举漂移时空白）。 */
export function sessionModeBannerLabelKey(mode: string) {
  return KNOWN_MODE_VALUES.has(mode) ? `composer.permission.mode.${mode}.label` : null;
}

/**
 * plan 模式下的状态读法（文案键叶子）：
 * 模型已请求裁决 > 计划正文可用（可评审）> 计划尚未写就。
 * 非 active 返回 null（横幅只显示模式本身）。
 */
export function sessionModeBannerHintLeaf(plan: RuntimeSessionPlanMode | null) {
  if (!plan?.active) {
    return null;
  }
  if (plan.pending_exit_request) {
    return "modelRequested";
  }
  if (plan.plan_content_available) {
    return "ready";
  }
  return "waiting";
}

/** 每档 tone 的着色：plan 走强调色，跳权模式走告警色，其余走中性文本。 */
export function sessionModeBannerToneClass(tone: SessionModeBannerTone) {
  switch (tone) {
    case "plan":
      return "border-accent-teal/30 bg-accent-teal/10 text-accent-teal";
    case "danger":
      return "border-accent-gold/40 bg-accent-gold/10 text-accent-gold";
    default:
      return "text-muted-foreground";
  }
}
