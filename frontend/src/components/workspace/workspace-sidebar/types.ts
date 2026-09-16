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
  /** 桌面（xl+）侧栏收起为「只显示图标的列」；移动抽屉始终按展开态渲染。 */
  collapsed?: boolean;
  /** 收起/展开开关的接线口；缺省时头部与图标列不渲染切换按钮。 */
  onCollapsedChange?: (collapsed: boolean) => void;
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
  runtimeSessionUsers: RuntimeSessionUserSummary[];
  selectedRuntimeSessionUserId: string;
  onRefreshRuntimeTeams?: () => void;
  workspaceDirectories: RuntimeWorkspaceDirectory[];
  workspaceDirectoriesError: string | null;
  workspaceDirectoriesLoading: boolean;
  workspaceDirectoriesRefreshing?: boolean;
  /**
   * 注册一个工作目录（POST 注册表）。返回后端登记的目录记录：
   * 方案 §15 的「注册并新建会话」要用它的 id 绑定新会话。
   */
  onAddWorkspaceDirectory: (
    path: string,
    name?: string,
  ) => Promise<RuntimeWorkspaceDirectory | void>;
  onRenameWorkspaceDirectory: (id: string, name: string) => Promise<void>;
  onRemoveWorkspaceDirectory: (id: string) => Promise<void>;
  onCreateSessionInDirectory: (
    directory: WorkspaceDirectoryCreateRequest,
  ) => Promise<void>;
  onRenameRuntimeSession: (sessionId: string, title: string) => Promise<void>;
  /** P2-6 子片 2：跨组拖拽移动（只写回 `metadata.context.workspace_path`）。 */
  onMoveRuntimeSession?: (
    sessionId: string,
    workspacePath: string,
  ) => Promise<void> | void;
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

/**
 * Phase 2（合并方案 §3.2）：`sessions` 分区已被合并进 `directories` 段（目录会话树），
 * 分段折叠状态里不再有该 id。
 */
export type SidebarSectionId = "directories" | "chats" | "runtime";

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
  sessionError: string;
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
