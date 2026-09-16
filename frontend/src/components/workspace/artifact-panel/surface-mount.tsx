// 自包含面（files / git）的懒加载挂载点。
//
// 设计要点：
//   * 懒加载组件由注册表在**模块作用域**创建（`panel-registry.ts` 的 `lazySurface`），
//     本组件只做 `spec.surface` 的渲染分发 —— 渲染期不创建组件身份，也就不会卸载重挂 /
//     重复请求（目录列表 / status 不会被打两次），同时满足 `react-hooks/static-components`。
//   * 未激活的面**不挂载**（不预取、不请求），与「页面不白屏」的取舍一致。
//   * fallback 使用既有 loadingArtifactPanel 文案，避免新增不必要的文案键。

import { Suspense } from "react";
import { useTranslation } from "react-i18next";

import {
  type WorkspacePanelThreadRelation,
  type WorkspacePanelSurfaceSpec,
  type WorkspacePanelSurfaceTabIds,
} from "@/components/workspace/panel-registry";

export function ArtifactPanelSurfaceLoading() {
  const { t } = useTranslation("workspace");

  return (
    <div className="grid min-h-0 flex-1 gap-3 overflow-auto p-3">
      <div className="flex min-h-[14rem] items-center justify-center rounded-panel border border-white/8 bg-white/[0.035] px-3.5 py-2.5 text-sm text-muted-foreground">
        {t("shell.loadingArtifactPanel")}
      </div>
    </div>
  );
}

export function SelfContainedSurfacePanel({
  active,
  sessionId,
  spec,
  tabIds,
  threadRelation,
  workspacePath,
}: {
  active: boolean;
  sessionId: string;
  spec: WorkspacePanelSurfaceSpec;
  tabIds: WorkspacePanelSurfaceTabIds;
  threadRelation?: WorkspacePanelThreadRelation;
  workspacePath?: string;
}) {
  // 只做属性读取：组件身份来自注册表模块作用域，渲染期不创建组件。
  const Surface = spec.surface;
  if (!Surface) {
    return null;
  }

  return (
    <div
      aria-labelledby={tabIds.tabId}
      // 必须是 **flex 列容器**：自包含面（files / git）的根节点用 `flex-1` + 内部
      // `grid-rows-[…minmax(0,1fr)…]` 建立高度链。若这里只是块级 div，子面的 `flex-1`
      // 不生效 → 子面高度退化为内容高度 → 内部 `overflow-auto` 区域永远不会产生滚动条，
      // 预览区也被挤到裁剪区之外。
      className="flex min-h-0 flex-1 flex-col overflow-hidden"
      hidden={!active}
      id={tabIds.panelId}
      role="tabpanel"
    >
      {active ? (
        <Suspense fallback={<ArtifactPanelSurfaceLoading />}>
          <Surface
            sessionId={sessionId}
            threadRelation={threadRelation}
            workspacePath={workspacePath}
          />
        </Suspense>
      ) : null}
    </div>
  );
}
