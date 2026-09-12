// 由 components/workspace/artifact-detail-dialog.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type KeyboardEvent as ReactKeyboardEvent } from "react";

import { type Artifact } from "@/data/mock";
import {
  classifyArtifactCategory,
  formatArtifactCategory,
} from "@/lib/workspace-artifacts";
import { cn } from "@/lib/utils";

import type { ArtifactDetailView, ArtifactMetaItem } from "./types";

export function surfaceButtonClass(active: boolean) {
  return cn(
    "inline-flex items-center gap-2 rounded-[0.65rem] border px-2.5 py-1 text-base transition focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--dialog-bg)]",
    active
      ? "border-[#f0c77b]/30 bg-[#f0c77b]/8 text-[#f0c77b]"
      : "border-white/10 bg-white/4 text-[var(--muted-foreground)] hover:border-white/16 hover:bg-white/8",
  );
}

function getEnabledTabIndices(disabledStates: boolean[]) {
  return disabledStates.flatMap((disabled, index) => (disabled ? [] : [index]));
}

export function handleHorizontalTabKeyDown(
  event: ReactKeyboardEvent<HTMLButtonElement>,
  options: {
    currentIndex: number;
    disabledStates: boolean[];
    onSelectIndex: (index: number) => void;
    refs: Array<HTMLButtonElement | null>;
  },
) {
  const enabledIndices = getEnabledTabIndices(options.disabledStates);
  if (enabledIndices.length === 0) {
    return;
  }

  const currentEnabledIndex = enabledIndices.indexOf(options.currentIndex);
  let nextIndex = -1;

  if (event.key === "Home") {
    nextIndex = enabledIndices[0] ?? -1;
  } else if (event.key === "End") {
    nextIndex = enabledIndices[enabledIndices.length - 1] ?? -1;
  } else if (event.key === "ArrowRight" || event.key === "ArrowDown") {
    const targetEnabledIndex =
      currentEnabledIndex >= 0
        ? (currentEnabledIndex + 1) % enabledIndices.length
        : 0;
    nextIndex = enabledIndices[targetEnabledIndex] ?? -1;
  } else if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
    const targetEnabledIndex =
      currentEnabledIndex >= 0
        ? (currentEnabledIndex - 1 + enabledIndices.length) % enabledIndices.length
        : enabledIndices.length - 1;
    nextIndex = enabledIndices[targetEnabledIndex] ?? -1;
  }

  if (nextIndex < 0) {
    return;
  }

  event.preventDefault();
  options.onSelectIndex(nextIndex);
  options.refs[nextIndex]?.focus();
}

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
