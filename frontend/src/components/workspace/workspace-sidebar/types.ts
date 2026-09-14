// 由 components/workspace/workspace-sidebar.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type LucideIcon } from "lucide-react";

import { type Thread } from "@/data/mock";

import {
  type MergedDirectoryGroup,
  type ThreadSessionDescriptor,
} from "@/components/workspace/workspace-sidebar-shared";
import { type RuntimeSessionsSummary } from "@/hooks/workspace/use-runtime-sessions-data";
import {
  type RuntimeSessionRecord,
  type RuntimeSessionUserSummary,
  type RuntimeTeamRecord,
  type RuntimeTeamSummaryEntry,
  type RuntimeWorkspaceDirectory,
} from "@/lib/runtime-api";

import { type SidebarSessionActivity } from "./session-row-status";

export type WorkspaceDirectoryCreateRequest = {
  path: string;
  directoryId?: string;
  label: string;
};

export type WorkspaceSidebarProps = {
  density: "comfortable" | "compact";
  mobileOpen?: boolean;
  onCloseMobile?: () => void;
  onOpenSettings?: () => void;
  runtimeTeams: RuntimeTeamRecord[];
  runtimeTeamsError: string | null;
  runtimeTeamsLoading: boolean;
  runtimeTeamsRefreshing?: boolean;
  runtimeTeamSummaries: RuntimeTeamSummaryEntry[];
  runtimeSessionsError: string | null;
  runtimeSessions: RuntimeSessionRecord[];
  runtimeSessionsLoading: boolean;
  runtimeSessionsRefreshing?: boolean;
  runtimeSessionsSummary: RuntimeSessionsSummary;
  runtimeSessionDefaultUserId?: string;
  runtimeSessionUsers: RuntimeSessionUserSummary[];
  runtimeSessionUsersError: string | null;
  runtimeSessionUsersLoading: boolean;
  selectedRuntimeSessionUserId: string;
  onRefreshRuntimeTeams?: () => void;
  onSelectRuntimeSessionUser: (userId: string) => void;
  workspaceDirectories: RuntimeWorkspaceDirectory[];
  workspaceDirectoriesError: string | null;
  workspaceDirectoriesLoading: boolean;
  workspaceDirectoriesRefreshing?: boolean;
  onAddWorkspaceDirectory: (path: string, name?: string) => Promise<unknown>;
  onRenameWorkspaceDirectory: (id: string, name: string) => Promise<void>;
  onRemoveWorkspaceDirectory: (id: string) => Promise<void>;
  onCreateSessionInDirectory: (
    directory: WorkspaceDirectoryCreateRequest,
  ) => Promise<void>;
  onRenameRuntimeSession: (sessionId: string, title: string) => Promise<void>;
  /** P1-9 归档/恢复：可选，缺省时行内不渲染操作菜单。 */
  onArchiveRuntimeSession?: (sessionId: string) => Promise<void> | void;
  onRestoreRuntimeSession?: (sessionId: string) => Promise<void> | void;
  /** P1-9 Fork：同标题后缀 + 继承工作目录的新独立会话（不复制历史）。 */
  onForkRuntimeSession?: (
    sessionId: string,
    sourceTitle: string,
  ) => Promise<void> | void;
  /** P1-9 非破坏删除：仅移除会话记录，不连带目录与磁盘数据。 */
  onDeleteRuntimeSession?: (sessionId: string) => Promise<void> | void;
  /** P1-9 本地已知的会话活动（等待类最醒目）；键为 sessionId。 */
  sessionActivity?: Record<string, SidebarSessionActivity>;
  threads: Thread[];
  selectedThreadId: string;
  onSelectThread: (threadId: string) => void;
};

export type SidebarSectionId = "directories" | "chats" | "sessions" | "runtime";

export type SidebarSectionState = Record<SidebarSectionId, boolean>;

export type SidebarStateIconSpec = {
  icon: LucideIcon;
  label: string;
  toneClassName: string;
};

export type WorkspaceSidebarIconLabels = {
  threadReview: string;
  threadDraft: string;
  threadActive: string;
  sessionArchived: string;
  sessionClosed: string;
  sessionError: string;
  sessionRestored: string;
  sessionAttached: string;
  sessionPending: string;
  sessionPlanPending: string;
  sessionRunning: string;
  sessionSubagents: string;
  sessionWaitingAnswer: string;
  sessionWaitingApproval: string;
};

// 供拆分后的各子模块复用的公共别名类型（避免重复深引用）。
export type SidebarThread = Thread;
export type SidebarThreadDescriptor = ThreadSessionDescriptor;
export type SidebarDirectoryGroup = MergedDirectoryGroup;
