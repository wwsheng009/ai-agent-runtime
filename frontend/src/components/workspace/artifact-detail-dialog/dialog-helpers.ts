// 由 components/workspace/artifact-detail-dialog.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Artifact } from "@/data/mock";
import {
  classifyArtifactCategory,
  formatArtifactCategory,
} from "@/lib/workspace-artifacts";

import type { ArtifactDetailView, ArtifactMetaItem } from "./types";

export function buildMetaItems(
  artifact: Artifact,
  view: ArtifactDetailView,
): ArtifactMetaItem[] {
  const lines = artifact.kind === "image" ? null : artifact.content.split(/\r?\n/).length;
  const category = classifyArtifactCategory(artifact);
  const readingMode =
    artifact.kind === "image"
      ? "Image"
      : view === "preview"
        ? "Preview"
        : "Source";

  return [
    { label: "Category", value: formatArtifactCategory(category) },
    { label: "Path", value: artifact.path },
    { label: "Language", value: artifact.language ?? "—" },
    { label: "Format", value: artifact.kind },
    { label: "Reading mode", value: readingMode },
    {
      label: artifact.kind === "image" ? "Byte count" : "Lines",
      value:
        artifact.kind === "image"
          ? artifact.byteCount != null
            ? formatBytes(artifact.byteCount)
            : "—"
          : `${lines}`,
    },
  ];
}

export function formatBytes(value: number) {
  if (!Number.isFinite(value) || value < 0) {
    return "—";
  }
  if (value < 1024) {
    return `${value} B`;
  }
  const units = ["KB", "MB", "GB", "TB"];
  let current = value / 1024;
  let unitIndex = 0;
  while (current >= 1024 && unitIndex < units.length - 1) {
    current /= 1024;
    unitIndex++;
  }
  return `${current.toFixed(current >= 10 ? 0 : 1)} ${units[unitIndex]}`;
}
