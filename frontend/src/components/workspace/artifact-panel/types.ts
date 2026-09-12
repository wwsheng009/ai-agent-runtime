// 由 components/workspace/artifact-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Artifact } from "@/data/mock";

export type ArtifactPanelSurface = "artifacts" | "checkpoints" | "plan";

export type ArtifactPanelProps = {
  artifacts: Artifact[];
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
  onOpenArtifact: (artifactId: string) => void;
  selectedArtifactId: string | null;
  sessionId?: string;
};

export type ArtifactPanelSurfaceTabIds = {
  artifactPanelId: string;
  artifactTabId: string;
  checkpointPanelId: string;
  checkpointTabId: string;
  planPanelId: string;
  planTabId: string;
};
