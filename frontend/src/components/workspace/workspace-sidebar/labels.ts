// 由 components/workspace/workspace-sidebar.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";

import { type WorkspaceSidebarIconLabels } from "./types";

export type ThreadSessionDetails = {
  pending: string;
  error: string;
  restored: string;
  attached: string;
};

export function buildSidebarIconLabels(
  t: TFunction<"workspace">,
): WorkspaceSidebarIconLabels {
    const sidebarLabels: WorkspaceSidebarIconLabels = {
      threadReview: t("sidebar.threadStatuses.review"),
      threadDraft: t("sidebar.threadStatuses.draft"),
      threadActive: t("sidebar.threadStatuses.active"),
      sessionError: t("sidebar.sessionStatuses.error"),
      sessionRestored: t("sidebar.sessionStatuses.restored"),
      sessionAttached: t("sidebar.sessionStatuses.attached"),
      sessionPending: t("sidebar.sessionStatuses.pending"),
    };
  return sidebarLabels;
}

export function buildThreadSessionDetails(
  t: TFunction<"workspace">,
): ThreadSessionDetails {
    const threadSessionDetails = {
      pending: t("sidebar.emptyChats.default"),
      error: t("sidebar.sessionDetails.error"),
      restored: t("sidebar.sessionDetails.restored"),
      attached: t("sidebar.sessionDetails.attached"),
    };
  return threadSessionDetails;
}
