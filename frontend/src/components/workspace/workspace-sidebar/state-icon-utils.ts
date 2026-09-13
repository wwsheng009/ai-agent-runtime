// 由 components/workspace/workspace-sidebar.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { CheckIcon, Clock3Icon, HistoryIcon, LoaderCircleIcon, SearchIcon, SparklesIcon, TriangleAlertIcon } from "lucide-react";
import { type ThreadSessionDescriptor } from "@/components/workspace/workspace-sidebar-shared";
import { type Thread } from "@/data/mock";
import {
  type SidebarStateIconSpec,
  type WorkspaceSidebarIconLabels,
} from "./types";

export function getThreadWorkflowIcon(
  thread: Thread,
  labels: Pick<
    WorkspaceSidebarIconLabels,
    "threadReview" | "threadDraft" | "threadActive"
  >,
): SidebarStateIconSpec {
  if (thread.status === "review") {
    return {
      icon: SearchIcon,
      label: labels.threadReview,
      toneClassName:
        "border-accent-primary-border bg-accent-primary-soft text-accent-primary",
    };
  }

  if (thread.status === "draft") {
    return {
      icon: Clock3Icon,
      label: labels.threadDraft,
      toneClassName:
        "border-border bg-surface-soft text-muted-foreground",
    };
  }

  return {
    icon: SparklesIcon,
    label: labels.threadActive,
    toneClassName:
      "border-accent-secondary-border bg-accent-secondary-soft text-accent-secondary",
  };
}

export function getSessionStatusIcon(
  label: ThreadSessionDescriptor["label"],
  labels: Pick<
    WorkspaceSidebarIconLabels,
    "sessionError" | "sessionRestored" | "sessionAttached" | "sessionPending"
  >,
): SidebarStateIconSpec {
  if (label === "error") {
    return {
      icon: TriangleAlertIcon,
      label: labels.sessionError,
      toneClassName: "border-[#f59e7d]/24 bg-[#f59e7d]/10 text-[#f59e7d]",
    };
  }

  if (label === "restored") {
    return {
      icon: HistoryIcon,
      label: labels.sessionRestored,
      toneClassName: "border-[#8fd0c6]/24 bg-[#8fd0c6]/10 text-[#8fd0c6]",
    };
  }

  if (label === "attached") {
    return {
      icon: CheckIcon,
      label: labels.sessionAttached,
      toneClassName: "border-[#f0c77b]/24 bg-[#f0c77b]/10 text-[#f0c77b]",
    };
  }

  return {
    icon: LoaderCircleIcon,
    label: labels.sessionPending,
    toneClassName:
      "border-border bg-surface-soft text-muted-foreground",
  };
}
