// 由 components/workspace/artifact-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Artifact } from "@/data/mock";

// 「会话用量」与条目/计划/还原同属一个右侧面板，仅作为第四个页签，不再单独成面板。
export type ArtifactPanelSurface = "artifacts" | "checkpoints" | "plan" | "usage";

export type ArtifactPanelProps = {
  artifacts: Artifact[];
  isResponding?: boolean;
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
  usagePanelId: string;
  usageTabId: string;
};
