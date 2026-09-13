// 由 components/workspace/artifact-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { useRef, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { FileCode2Icon, HistoryIcon, ScrollTextIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

import {
  type ArtifactPanelSurface,
  type ArtifactPanelSurfaceTabIds,
} from "./types";

function surfaceButtonClass(
  active: boolean,
  tone: "artifact" | "checkpoint" | "plan",
  disabled = false,
) {
  if (disabled) {
    return "inline-flex items-center gap-2 rounded-control border border-white/8 bg-white/4 px-2.5 py-1 text-base text-muted-foreground opacity-60";
  }

  if (tone === "artifact") {
    return cn(
      "inline-flex items-center gap-2 rounded-control border px-2.5 py-1 text-base transition focus:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--workspace-sidebar-bg)]",
      active
        ? "border-accent-gold/30 bg-accent-gold/8 text-accent-gold"
        : "border-white/10 bg-white/4 text-muted-foreground hover:border-white/16 hover:bg-white/8",
    );
  }

  if (tone === "plan") {
    return cn(
      "inline-flex items-center gap-2 rounded-control border px-2.5 py-1 text-base transition focus:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--workspace-sidebar-bg)]",
      active
        ? "border-[#9db7ff]/30 bg-[#9db7ff]/10 text-[#9db7ff]"
        : "border-white/10 bg-white/4 text-muted-foreground hover:border-white/16 hover:bg-white/8",
    );
  }

  return cn(
    "inline-flex items-center gap-2 rounded-control border px-2.5 py-1 text-base transition focus:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--workspace-sidebar-bg)]",
    active
      ? "border-accent-teal/30 bg-accent-teal/10 text-accent-teal"
      : "border-white/10 bg-white/4 text-muted-foreground hover:border-white/16 hover:bg-white/8",
  );
}

function surfaceFromIndex(index: number): ArtifactPanelSurface {
  if (index === 1) {
    return "plan";
  }
  if (index === 2) {
    return "checkpoints";
  }
  return "artifacts";
}

function getEnabledTabIndices(disabledStates: boolean[]) {
  return disabledStates.flatMap((disabled, index) => (disabled ? [] : [index]));
}

function handleHorizontalTabKeyDown(
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

type ArtifactPanelSurfaceTabsProps = {
  activeSurface: ArtifactPanelSurface;
  artifactCount: number;
  backtrackCount: number;
  onSelectSurface: (surface: ArtifactPanelSurface) => void;
  planIsActive: boolean;
  sessionId?: string;
  surfaceTabDisabledStates: boolean[];
  tabIds: ArtifactPanelSurfaceTabIds;
  titleId: string;
};

export function ArtifactPanelSurfaceTabs({
  activeSurface,
  artifactCount,
  backtrackCount,
  onSelectSurface,
  planIsActive,
  sessionId,
  surfaceTabDisabledStates,
  tabIds,
  titleId,
}: ArtifactPanelSurfaceTabsProps) {
  const surfaceTabRefs = useRef<Array<HTMLButtonElement | null>>([]);

  return (
    <div className="border-b border-white/8 px-3 py-2.5">
      <div className="flex items-center justify-between gap-3">
        <div className="sr-only" id={titleId}>
          Artifacts
        </div>
        <div
          aria-label="Artifact panel surfaces"
          aria-orientation="horizontal"
          className="flex flex-wrap gap-1.5"
          role="tablist"
        >
          <button
            aria-controls={tabIds.artifactPanelId}
            aria-selected={activeSurface === "artifacts"}
            id={tabIds.artifactTabId}
            ref={(node) => {
              surfaceTabRefs.current[0] = node;
            }}
            role="tab"
            tabIndex={activeSurface === "artifacts" ? 0 : -1}
            type="button"
            onClick={() => onSelectSurface("artifacts")}
            onKeyDown={(event) =>
              handleHorizontalTabKeyDown(event, {
                currentIndex: 0,
                disabledStates: surfaceTabDisabledStates,
                onSelectIndex: (index) => onSelectSurface(surfaceFromIndex(index)),
                refs: surfaceTabRefs.current,
              })
            }
            className={surfaceButtonClass(
              activeSurface === "artifacts",
              "artifact",
            )}
          >
            <FileCode2Icon size={14} />
            Items
          </button>
          <button
            aria-controls={tabIds.planPanelId}
            aria-selected={activeSurface === "plan"}
            id={tabIds.planTabId}
            ref={(node) => {
              surfaceTabRefs.current[1] = node;
            }}
            role="tab"
            tabIndex={activeSurface === "plan" ? 0 : -1}
            type="button"
            onClick={() => onSelectSurface("plan")}
            onKeyDown={(event) =>
              handleHorizontalTabKeyDown(event, {
                currentIndex: 1,
                disabledStates: surfaceTabDisabledStates,
                onSelectIndex: (index) => onSelectSurface(surfaceFromIndex(index)),
                refs: surfaceTabRefs.current,
              })
            }
            className={surfaceButtonClass(
              activeSurface === "plan",
              "plan",
              !sessionId,
            )}
            disabled={!sessionId}
          >
            <ScrollTextIcon size={14} />
            Plan
            {planIsActive ? (
              <span className="rounded-full bg-[#9db7ff]/20 px-1.5 py-0.5 text-[10px] tracking-[0.08em] text-[#9db7ff]">
                live
              </span>
            ) : null}
          </button>
          <button
            aria-controls={tabIds.checkpointPanelId}
            aria-selected={activeSurface === "checkpoints"}
            id={tabIds.checkpointTabId}
            ref={(node) => {
              surfaceTabRefs.current[2] = node;
            }}
            role="tab"
            tabIndex={activeSurface === "checkpoints" ? 0 : -1}
            type="button"
            onClick={() => onSelectSurface("checkpoints")}
            onKeyDown={(event) =>
              handleHorizontalTabKeyDown(event, {
                currentIndex: 2,
                disabledStates: surfaceTabDisabledStates,
                onSelectIndex: (index) => onSelectSurface(surfaceFromIndex(index)),
                refs: surfaceTabRefs.current,
              })
            }
            className={surfaceButtonClass(
              activeSurface === "checkpoints",
              "checkpoint",
              !sessionId,
            )}
            disabled={!sessionId}
          >
            <HistoryIcon size={14} />
            Restore
            {backtrackCount > 0 ? (
              <span className="rounded-full bg-accent-gold/20 px-1.5 py-0.5 text-[10px] tracking-[0.08em] text-accent-gold">
                {backtrackCount}
              </span>
            ) : null}
          </button>
        </div>
        <div className="flex items-center gap-2">
          <Badge>{artifactCount}</Badge>
        </div>
      </div>
    </div>
  );
}
