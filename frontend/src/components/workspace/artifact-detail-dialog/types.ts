// 由 components/workspace/artifact-detail-dialog.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Artifact } from "@/data/mock";

export type ArtifactDetailDialogProps = {
  artifact: Artifact | null;
  onClose: () => void;
  open: boolean;
};

export type ArtifactDetailView = "preview" | "source";

export type ArtifactMetaItem = {
  label: string;
  value: string;
};
