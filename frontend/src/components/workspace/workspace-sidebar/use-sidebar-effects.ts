// 由 components/workspace/workspace-sidebar.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Dispatch, type SetStateAction, useEffect } from "react";

import {
  type SidebarDirectoryGroup,
  type SidebarSectionState,
  type SidebarThread,
} from "./types";

type UseSidebarEffectsParams = {
  deferredQuery: string;
  mergedDirectoryGroups: SidebarDirectoryGroup[];
  mobileOpen: boolean;
  onCloseMobile?: () => void;
  selectedThreadId: string;
  sessionThreadById: Map<string, SidebarThread>;
  setOpenSections: Dispatch<SetStateAction<SidebarSectionState>>;
  setOpenSessionDirectories: Dispatch<SetStateAction<Record<string, boolean>>>;
};

export function useSidebarEffects({
  deferredQuery,
  mergedDirectoryGroups,
  mobileOpen,
  onCloseMobile,
  selectedThreadId,
  sessionThreadById,
  setOpenSections,
  setOpenSessionDirectories,
}: UseSidebarEffectsParams) {
  useEffect(() => {
    if (!deferredQuery) {
      return;
    }

    let cancelled = false;
    queueMicrotask(() => {
      if (cancelled) {
        return;
      }

      // Phase 2（合并方案 §3.2）：会话树已并入 `directories` 段，检索时展开合并段与历史段。
      setOpenSections((current) =>
        current.chats && current.directories
          ? current
          : { ...current, chats: true, directories: true },
      );
    });

    return () => {
      cancelled = true;
    };
    // setOpenSections 来自父层 useState，identity 稳定，加入依赖数组不改变语义。
  }, [deferredQuery, setOpenSections]);

  useEffect(() => {
    if (!mobileOpen || !onCloseMobile) {
      return;
    }

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        onCloseMobile();
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => {
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [mobileOpen, onCloseMobile]);

  useEffect(() => {
    setOpenSessionDirectories((current) => {
      const knownKeys = new Set(mergedDirectoryGroups.map((group) => group.key));
      const next: Record<string, boolean> = {};
      let changed = false;

      for (const [key, value] of Object.entries(current)) {
        if (knownKeys.has(key)) {
          next[key] = value;
        } else {
          changed = true;
        }
      }

      mergedDirectoryGroups.forEach((group, index) => {
        if (typeof next[group.key] === "boolean") {
          return;
        }
        const containsSelectedThread = group.sessions.some((session) => {
          const thread = sessionThreadById.get(session.id);
          return thread?.id === selectedThreadId || thread?.sessionId === selectedThreadId;
        });
        next[group.key] = containsSelectedThread || index === 0;
        changed = true;
      });

      return changed ? next : current;
    });
    // setOpenSessionDirectories 同上（父层 useState setter，identity 稳定）。
  }, [
    mergedDirectoryGroups,
    selectedThreadId,
    sessionThreadById,
    setOpenSessionDirectories,
  ]);
}
