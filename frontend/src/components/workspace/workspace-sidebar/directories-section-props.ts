// P0-2 拆分：段组件的 props 契约（原 directories-section.tsx L48-L120 的类型块）。
// 拆出原因：该类型单文件占 ~70 非空行且与实现无耦合，便于后续复用与审阅。

import { type Dispatch, type SetStateAction } from "react";
import { type TFunction } from "i18next";

import type { SessionStatsStatus } from "@/hooks/workspace/use-session-stats";
import { type SessionGroupingMode } from "@/lib/workspace/session-grouping";
import { type SessionOrderMode } from "@/lib/workspace/session-order";
import { type RuntimeWorkspaceDirectory } from "@/lib/runtime-api";
import { type RuntimeSessionStats } from "@/types/runtime";

import { type SidebarSessionActivity } from "./session-row-status";
import {
  type SidebarDirectoryDeleteTarget,
  type SidebarDirectoryGroup,
  type SidebarSectionId,
  type SidebarSectionState,
  type SidebarThread,
} from "./types";

export type WorkspaceSidebarDirectoriesSectionProps = {
  // ── 目录注册表管理（原工作目录段） ──────────────────────────────
  cancelDirectoryRename: () => void;
  commitDirectoryRename: (nextName: string) => Promise<void>;
  creatingSessionKey: string | null;
  handleCreateSessionInDirectory: (
    group: SidebarDirectoryGroup,
  ) => Promise<void>;
  mergedDirectoryGroups: SidebarDirectoryGroup[];
  renamingDirectoryId: string | null;
  setDirectoryAddOpen: Dispatch<SetStateAction<boolean>>;
  setDirectoryDeleteTarget: Dispatch<
    SetStateAction<SidebarDirectoryDeleteTarget | null>
  >;
  startDirectoryRename: (group: SidebarDirectoryGroup) => void;
  /** 平铺模式下的目录管理入口（段内工具条按钮 → 管理弹层）。 */
  onRequestManageDirectories: () => void;
  /** 派生目录（未注册）组头动作：注册并在该目录下新建会话（方案 §15）。 */
  onRegisterAndCreateSession?: (group: SidebarDirectoryGroup) => Promise<void>;
  /** 正在「注册并新建会话」的目录路径（派生组按 fullPath 命中 → 忙碌态）。 */
  registeringDirectoryPath?: string | null;
  workspaceDirectories: RuntimeWorkspaceDirectory[];
  workspaceDirectoriesError: string | null;
  workspaceDirectoriesLoading: boolean;
  workspaceDirectoriesRefreshing?: boolean;
  onRefreshWorkspaceDirectories?: () => void;
  runtimeSessionsRefreshing?: boolean;
  onRefreshRuntimeSessions?: () => void;
  // ── 会话浏览（原会话段） ───────────────────────────────────────
  cancelSessionRename: () => void;
  canMoveSessionToGroup: (groupKey: string) => boolean;
  deferredQuery: string;
  handleRenameSession: (sessionId: string, title: string) => Promise<void>;
  hiddenArchivedCount: number;
  onArchiveSession?: (sessionId: string) => void;
  onDeleteSession?: (sessionId: string) => void;
  onForkSession?: (sessionId: string, sourceTitle: string) => void;
  onMoveSessionToGroup: (sessionId: string, groupKey: string) => void;
  onRefreshSessionStats: () => void;
  onReorderSessions: (accountKey: string, order: readonly string[]) => void;
  onRestoreSession?: (sessionId: string) => void;
  /** §4.8：就地停止会话在途回合（菜单项只在「运行中 / 等待类」时出现）。 */
  onStopSession?: (sessionId: string) => void;
  onSelectSessionGroupingMode: (mode: SessionGroupingMode) => void;
  onSelectSessionOrderMode: (mode: SessionOrderMode) => void;
  onToggleArchivedSessions: () => void;
  sessionActivity?: Record<string, SidebarSessionActivity>;
  sessionDirectoryGroups: SidebarDirectoryGroup[];
  sessionGroupingMode: SessionGroupingMode;
  /** 跨组移动失败的就地提示（失败即回滚，提示挂在合并段而不是独立目录段）。 */
  sessionMoveError: string | null;
  sessionOrderMode: SessionOrderMode;
  sessionStats: RuntimeSessionStats | null;
  sessionStatsError: unknown;
  sessionStatsStatus: SessionStatsStatus;
  sessionStatsUnavailable: boolean;
  sessionThreadById: Map<string, SidebarThread>;
  sessionThreads: SidebarThread[];
  showArchivedSessions: boolean;
  // ── 接线层公共 ────────────────────────────────────────────────
  onSelectThread: (threadId: string) => void;
  openSections: SidebarSectionState;
  openSessionDirectories: Record<string, boolean>;
  renamingSessionId: string | null;
  selectedThreadId: string;
  showWorkspaceSection: boolean;
  sidebarActionError: string | null;
  setSidebarActionError: Dispatch<SetStateAction<string | null>>;
  startSessionRename: (sessionId: string) => void;
  t: TFunction<"workspace">;
  toggleSection: (section: SidebarSectionId) => void;
  toggleSessionDirectory: (directoryKey: string) => void;
};
