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
      toneClassName: "border-accent-orange/24 bg-accent-orange/10 text-accent-orange",
    };
  }

  if (label === "restored") {
    return {
      icon: HistoryIcon,
      label: labels.sessionRestored,
      toneClassName: "border-accent-teal/24 bg-accent-teal/10 text-accent-teal",
    };
  }

  if (label === "attached") {
    return {
      icon: CheckIcon,
      label: labels.sessionAttached,
      toneClassName: "border-accent-gold/24 bg-accent-gold/10 text-accent-gold",
    };
  }

  return {
    icon: LoaderCircleIcon,
    label: labels.sessionPending,
    toneClassName:
      "border-border bg-surface-soft text-muted-foreground",
  };
}
