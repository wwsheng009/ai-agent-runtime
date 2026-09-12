// 由 hooks/workspace/use-runtime-checkpoints.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  buildCheckpointFileCode,
  formatCheckpointProvenance,
  pickInitialCheckpointFilePath,
} from "@/components/workspace/artifact-panel-shared";

import { type ResolveCheckpointDetailStateOptions } from "./types";

export function resolveCheckpointDetailState({
  checkpointFiles,
  checkpointPreview,
  selectedCheckpoint,
  selectedCheckpointFilePath,
}: ResolveCheckpointDetailStateOptions) {
  const checkpointPreviewFiles = checkpointPreview?.preview_files ?? [];
  const resolvedSelectedCheckpointFilePath =
    selectedCheckpointFilePath ??
    pickInitialCheckpointFilePath(checkpointFiles, checkpointPreviewFiles);
  const selectedCheckpointFile =
    checkpointFiles.find((file) => file.path === resolvedSelectedCheckpointFilePath) ??
    checkpointFiles[0];
  const selectedCheckpointPreviewFile =
    checkpointPreviewFiles.find(
      (file) => file.path === resolvedSelectedCheckpointFilePath,
    ) ??
    checkpointPreviewFiles[0];

  return {
    checkpointFileCode: buildCheckpointFileCode(
      selectedCheckpointFile,
      selectedCheckpointPreviewFile,
    ),
    checkpointProvenance: formatCheckpointProvenance(
      checkpointPreview?.provenance ?? selectedCheckpoint?.provenance,
    ),
    checkpointPreviewFiles,
    selectedCheckpointFilePath: resolvedSelectedCheckpointFilePath,
    selectedCheckpointFile,
    selectedCheckpointPreviewFile,
  };
}
