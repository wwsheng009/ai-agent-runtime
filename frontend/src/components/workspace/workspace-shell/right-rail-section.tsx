// 工作台右侧栏：单一可折叠面板，内含「条目 / 计划 / 还原 / 会话用量」页签。
// 会话用量不再是独立面板，而是 ArtifactPanel 的 usage 页签（由顶栏右侧栏开关统一折叠/展开）。

import { type TFunction } from "i18next";
import { Suspense } from "react";

import { PanelErrorBoundary } from "@/components/errors/boundaries";

import { ArtifactPanel, ArtifactPanelFallback } from "./lazy-surfaces";
import { type WorkspaceShellProps } from "./types";

type WorkspaceRightRailSectionProps = Pick<
  WorkspaceShellProps,
  | "isResponding"
  | "selectedArtifactId"
  | "selectedThread"
> & {
  handleOpenArtifact: (artifactId: string) => void;
  isNewThread: boolean;
  /** 合并面板的开合：由顶栏开关控制；关闭时整列不占位。 */
  rightRailOpen: boolean;
  t: TFunction<"workspace">;
};

export function WorkspaceRightRailSection({
  handleOpenArtifact,
  isNewThread,
  isResponding,
  rightRailOpen,
  selectedArtifactId,
  selectedThread,
  t,
}: WorkspaceRightRailSectionProps) {
  if (!rightRailOpen || isNewThread) {
    return null;
  }

  const sessionId = selectedThread.sessionId?.trim() ?? "";

  return (
    <div className="hidden min-h-0 min-w-0 flex-col overflow-hidden border-l border-white/8 [background:var(--workspace-sidebar-bg)] xl:flex">
      <PanelErrorBoundary key={`right-rail-${sessionId || "no-session"}`}>
        <Suspense
          fallback={
            <ArtifactPanelFallback message={t("shell.loadingArtifactPanel")} />
          }
        >
          <ArtifactPanel
            artifacts={selectedThread.artifacts}
            isResponding={isResponding}
            lastRuntimeEventType={selectedThread.lastRuntimeEventType}
            runtimeEventCount={selectedThread.runtimeEventCount}
            selectedArtifactId={selectedArtifactId}
            sessionId={selectedThread.sessionId}
            onOpenArtifact={handleOpenArtifact}
          />
        </Suspense>
      </PanelErrorBoundary>
    </div>
  );
}
