// 由 components/workspace/workspace-sidebar.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { ArchiveIcon, CheckIcon, Clock3Icon, HistoryIcon, LoaderCircleIcon, MessageSquareIcon, SearchIcon, SparklesIcon, TriangleAlertIcon, UsersIcon } from "lucide-react";
import { type ThreadSessionDescriptor } from "@/components/workspace/workspace-sidebar-shared";
import { type Thread } from "@/data/mock";
import { type SidebarSessionRowStatus } from "./session-row-status";
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

/**
 * P1-9 行状态图标：等待类（审批 > 计划 > 回答）用高饱和橙色/金色最醒目，
 * 运行中/子代理次之，归档为静默态。
 */
export function getRuntimeSessionActivityIcon(
  status: SidebarSessionRowStatus,
  labels: Pick<
    WorkspaceSidebarIconLabels,
    | "sessionArchived"
    | "sessionClosed"
    | "sessionPending"
    | "sessionPlanPending"
    | "sessionRunning"
    | "sessionSubagents"
    | "sessionWaitingAnswer"
    | "sessionWaitingApproval"
  >,
): SidebarStateIconSpec {
  switch (status.kind) {
    case "waitingApproval":
      return {
        icon: TriangleAlertIcon,
        label: labels.sessionWaitingApproval,
        toneClassName:
          "border-accent-orange/24 bg-accent-orange/10 text-accent-orange",
      };
    case "planPending":
      return {
        icon: Clock3Icon,
        label: labels.sessionPlanPending,
        toneClassName:
          "border-accent-gold/24 bg-accent-gold/10 text-accent-gold",
      };
    case "waitingAnswer":
      return {
        icon: MessageSquareIcon,
        label: labels.sessionWaitingAnswer,
        toneClassName:
          "border-accent-gold/24 bg-accent-gold/10 text-accent-gold",
      };
    case "running":
      return {
        icon: LoaderCircleIcon,
        label: labels.sessionRunning,
        toneClassName:
          "border-accent-secondary-border bg-accent-secondary-soft text-accent-secondary",
      };
    case "subagents":
      return {
        icon: UsersIcon,
        label: labels.sessionSubagents,
        toneClassName:
          "border-accent-teal/24 bg-accent-teal/10 text-accent-teal",
      };
    case "archived":
      return {
        icon: ArchiveIcon,
        label: labels.sessionArchived,
        toneClassName: "border-border bg-surface-soft text-muted-foreground",
      };
    case "closed":
      return {
        icon: CheckIcon,
        label: labels.sessionClosed,
        toneClassName: "border-border bg-surface-soft text-muted-foreground",
      };
    default:
      return {
        icon: Clock3Icon,
        label: labels.sessionPending,
        toneClassName: "border-border bg-surface-soft text-muted-foreground",
      };
  }
}
