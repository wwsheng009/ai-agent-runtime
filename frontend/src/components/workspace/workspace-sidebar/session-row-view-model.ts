// P2-6 子片 2：侧栏会话行的展示口径（纯派生，无 IO）。
// 从 sessions-section.tsx 拆出（P0-2 单文件非空行门禁）：把「标题 / 选中态 / 快照状态 /
// 状态图标 / 行内时间」的结算收在一处，组件只负责按结果渲染，避免行内再做判断。

import { type TFunction } from "i18next";

import { type Thread } from "@/data/mock";
import { formatRelativeTimestamp } from "@/lib/utils";
import { type RuntimeSessionRecord } from "@/types/runtime";

import { buildSidebarIconLabels, buildThreadSessionDetails } from "./labels";
import {
  resolveSidebarSessionRowState,
  resolveSidebarSessionRowStatus,
  type SidebarSessionActivity,
  type SidebarSessionRowState,
} from "./session-row-status";
import { type SidebarSessionItemTime } from "./session-item";
import { describeThreadSession } from "@/components/workspace/workspace-sidebar-shared";
import {
  getRuntimeSessionActivityIcon,
  getSessionStatusIcon,
} from "./state-icon-utils";
import { type SidebarStateIconSpec, type SidebarThread } from "./types";

export type SidebarSessionRowViewModel = {
  isActive: boolean;
  /** 行内时间；时间戳缺失或不可解析时不渲染（undefined）。 */
  itemTime: SidebarSessionItemTime | undefined;
  rowState: SidebarSessionRowState;
  statusIcon: SidebarStateIconSpec;
  thread: SidebarThread | undefined;
  title: string;
};

type ResolveSidebarSessionRowViewModelArgs = {
  session: RuntimeSessionRecord;
  sessionThread: SidebarThread | undefined;
  activity: SidebarSessionActivity | undefined;
  selectedThreadId: string;
  t: TFunction<"workspace">;
};

/** 会话尚无对应聊天线程时的占位线程（只为状态图标取标签，不进入列表渲染）。 */
function buildPlaceholderThread(
  session: RuntimeSessionRecord,
  title: string,
): Thread {
  return {
    id: session.id,
    title,
    summary: session.metadata?.summary ?? "",
    updatedAt: session.updatedAt || session.createdAt || "",
    status: "active",
    sessionId: session.id,
    tags: ["runtime-session"],
    prompts: [],
    messages: [],
    artifacts: [],
  };
}

export function resolveSidebarSessionRowViewModel({
  activity,
  selectedThreadId,
  session,
  sessionThread,
  t,
}: ResolveSidebarSessionRowViewModelArgs): SidebarSessionRowViewModel {
  const thread = sessionThread;
  const title =
    thread?.title || session.metadata?.title?.trim() || session.id;
  const rowState = resolveSidebarSessionRowState(session);
  const rowStatus = resolveSidebarSessionRowStatus(session, activity);
  const labels = buildSidebarIconLabels(t);
  const statusIcon =
    rowStatus.kind === "idle"
      ? getSessionStatusIcon(
          describeThreadSession(
            thread ?? buildPlaceholderThread(session, title),
            buildThreadSessionDetails(t),
          ).label,
          labels,
        )
      : getRuntimeSessionActivityIcon(rowStatus, labels);

  const timestamp = session.updatedAt || session.createdAt || "";
  const createdAt = session.createdAt || timestamp;
  const itemTime =
    timestamp && !Number.isNaN(Date.parse(timestamp))
      ? {
          relative: formatRelativeTimestamp(timestamp),
          title: t("sidebar.session.createdAt", {
            time: Number.isNaN(Date.parse(createdAt))
              ? createdAt
              : new Date(createdAt).toLocaleString(),
          }),
        }
      : undefined;

  return {
    isActive:
      thread?.id === selectedThreadId || thread?.sessionId === selectedThreadId,
    itemTime,
    rowState,
    statusIcon,
    thread,
    title,
  };
}
