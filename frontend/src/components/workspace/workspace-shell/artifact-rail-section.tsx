// 由 components/workspace/workspace-shell.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense } from "react";
import { type TFunction } from "i18next";

import { ArtifactPanel, ArtifactPanelFallback } from "./lazy-surfaces";
import { type WorkspaceShellProps } from "./types";

type WorkspaceArtifactRailSectionProps = Pick<
  WorkspaceShellProps,
  | "selectedArtifactId"
  | "selectedThread"
> & {
  artifactRailOpen: boolean;
  handleOpenArtifact: (artifactId: string) => void;
  isNewThread: boolean;
  t: TFunction<"workspace">;
};

export function WorkspaceArtifactRailSection({
  artifactRailOpen,
  handleOpenArtifact,
  isNewThread,
  selectedArtifactId,
  selectedThread,
  t,
}: WorkspaceArtifactRailSectionProps) {
  return (
    <>
      {artifactRailOpen && !isNewThread ? (
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
    </>
  );
}
