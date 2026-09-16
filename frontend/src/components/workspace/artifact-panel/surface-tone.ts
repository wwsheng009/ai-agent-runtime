// 页签/徽标 tone → 类名的统一出口（P0-1 拆分：组件文件只导出组件，样式映射放这里）。
//
// 背景：`react-refresh/only-export-components` 要求「只导出组件的文件才能热更新」，
// 因此把 `surfaceBadgeClass` 这类纯样式函数从 `surface-tabs.tsx` 搬到本模块，
// 让组件文件保持「只导出组件」；配色口径仍只有这一处（避免多处手写 px/圆角）。

import type { WorkspacePanelSurfaceTone } from "@/components/workspace/panel-registry";
import { cn } from "@/lib/utils";

/** 页签徽标样式的统一出口（各面复用，避免各处手写 px/圆角）。 */
export function surfaceBadgeClass(tone: WorkspacePanelSurfaceTone) {
  return cn(
    "rounded-full px-1.5 py-0.5 app-text-10 tracking-[0.08em]",
    tone === "plan" && "bg-[#9db7ff]/20 text-[#9db7ff]",
    tone === "checkpoint" && "bg-accent-gold/20 text-accent-gold",
    tone === "artifact" && "bg-accent-gold/20 text-accent-gold",
    tone === "usage" && "bg-accent-primary/20 text-accent-primary",
    tone === "file" && "bg-accent-cyan/20 text-accent-cyan",
    tone === "git" && "bg-accent-violet/20 text-accent-violet",
  );
}
