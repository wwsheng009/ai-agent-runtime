// §4.6 plan 状态读法的纯函数：逻辑放这里而不是组件里，是为了让单测直接钉住边界
// （plan 空快照、pending_exit_request 优先于「已就绪」等），组件文件只负责渲染。
//
// 权限模式的 tone / 文案键曾也在这里；composer 顶部不再重复显示模式后已随组件移除，
// 权限模式的文案与切换统一由 composer 底部的 ComposerPermissionModeControl 承担。

import type { RuntimeSessionPlanMode } from "@/lib/runtime-api";

/**
 * plan 模式下的状态读法（文案键叶子）：
 * 模型已请求裁决 > 计划正文可用（可评审）> 计划尚未写就。
 * 非 active 返回 null（不显示 plan 上下文）。
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
