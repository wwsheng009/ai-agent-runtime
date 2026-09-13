// 工作台右侧栏：会话用量面板（附着会话时随栏显示）+ artifact 面板（顶栏开关控制）。
// 由 workspace-shell/artifact-rail-section.tsx 演进而来，保留原 artifact 面板语义。

import { type TFunction } from "i18next";
import { Suspense } from "react";

import { SessionUsagePanel } from "@/components/workspace/session-usage-panel";

import { ArtifactPanel, ArtifactPanelFallback } from "./lazy-surfaces";
import { type WorkspaceShellProps } from "./types";

type WorkspaceRightRailSectionProps = Pick<
  WorkspaceShellProps,
  | "isResponding"
  | "selectedArtifactId"
  | "selectedThread"
> & {
  artifactRailOpen: boolean;
  handleOpenArtifact: (artifactId: string) => void;
  isNewThread: boolean;
  t: TFunction<"workspace">;
};

export function WorkspaceRightRailSection({
  artifactRailOpen,
  handleOpenArtifact,
  isNewThread,
  isResponding,
  selectedArtifactId,
  selectedThread,
  t,
}: WorkspaceRightRailSectionProps) {
  const sessionId = selectedThread.sessionId?.trim() ?? "";
  const showUsagePanel = !isNewThread && Boolean(sessionId);
  const showArtifactPanel = artifactRailOpen && !isNewThread;

  if (!showUsagePanel && !showArtifactPanel) {
    return null;
  }

  return (
    <div className="hidden min-h-0 min-w-0 flex-col overflow-hidden border-l border-white/8 [background:var(--workspace-sidebar-bg)] xl:flex">
      {showUsagePanel ? (
        <SessionUsagePanel
          key={sessionId}
          className="shrink-0"
          isResponding={isResponding}
          lastRuntimeEventType={selectedThread.lastRuntimeEventType}
          runtimeEventCount={selectedThread.runtimeEventCount}
          sessionId={sessionId}
        />
      ) : null}
      {showArtifactPanel ? (
        <Suspense fallback={<ArtifactPanelFallback message={t("shell.loadingArtifactPanel")} />}>
          <ArtifactPanel
            artifacts={selectedThread.artifacts}
            lastRuntimeEventType={selectedThread.lastRuntimeEventType}
            runtimeEventCount={selectedThread.runtimeEventCount}
            selectedArtifactId={selectedArtifactId}
            sessionId={selectedThread.sessionId}
            onOpenArtifact={handleOpenArtifact}
          />
        </Suspense>
      ) : null}
    </div>
  );
}
