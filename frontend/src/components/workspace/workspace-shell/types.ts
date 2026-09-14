// 由 components/workspace/workspace-shell.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Artifact, type Thread } from "@/data/mock";
import type { ComposerAttachmentsController } from "@/hooks/workspace/composer/use-composer-attachments";
import { type RuntimeSessionsSummary } from "@/hooks/workspace/use-runtime-sessions-data";
import type { SessionBacktrackDialogState } from "@/hooks/workspace/use-session-backtrack";
import { type ConnectionStatus } from "@/lib/connection-status";
import type { PendingInteraction } from "@/lib/pending-interaction";
import { type RuntimeClientIdentity } from "@/lib/runtime-client";
import {
  type RuntimeSessionBacktrackMode,
  type RuntimeSessionPlanModeExitDecision,
  type RuntimeSessionRecord,
  type RuntimeSessionUserSummary,
  type RuntimeModelsResponse,
  type RuntimeTeamRecord,
  type RuntimeTeamSummaryEntry,
  type RuntimeWorkspaceDirectory,
} from "@/lib/runtime-api";
import { type ChatStreamPhase } from "@/types/runtime";

import { type SidebarSessionActivity } from "@/components/workspace/workspace-sidebar/session-row-status";

export type WorkspaceShellProps = {
  threads: Thread[];
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
  workspaceDirectories: RuntimeWorkspaceDirectory[];
  workspaceDirectoriesError: string | null;
  workspaceDirectoriesLoading: boolean;
  workspaceDirectoriesRefreshing?: boolean;
  onAddWorkspaceDirectory: (path: string, name?: string) => Promise<unknown>;
  onRenameWorkspaceDirectory: (id: string, name: string) => Promise<void>;
  onRemoveWorkspaceDirectory: (id: string) => Promise<void>;
  /** P1-8：手动重试连接（复用既有 seq 游标续传，不新建退避循环）。 */
  onRetryConnection?: () => void;
  onCreateSessionInDirectory: (request: {
    path: string;
    directoryId?: string;
    label: string;
  }) => Promise<void>;
  onRenameRuntimeSession: (sessionId: string, title: string) => Promise<void>;
  /** P2-6 子片 2：跨组拖拽移动（只写回 `metadata.context.workspace_path`）。 */
  onMoveRuntimeSession?: (
    sessionId: string,
    workspacePath: string,
  ) => Promise<void> | void;
  /** P1-9 归档/恢复：可选，缺省时侧栏行内不渲染操作菜单。 */
  onArchiveRuntimeSession?: (sessionId: string) => Promise<void> | void;
  onRestoreRuntimeSession?: (sessionId: string) => Promise<void> | void;
  /** P1-9 Fork：同标题后缀 + 继承工作目录的新独立会话（不复制历史）。 */
  onForkRuntimeSession?: (
    sessionId: string,
    sourceTitle: string,
  ) => Promise<void> | void;
  /** P1-9 非破坏删除：仅移除会话记录，不连带目录与磁盘数据。 */
  onDeleteRuntimeSession?: (sessionId: string) => Promise<void> | void;
  /** P1-9 本地已知的会话活动（等待/运行类）；键为 sessionId。 */
  sessionActivity?: Record<string, SidebarSessionActivity>;
  runtimeClient: RuntimeClientIdentity;
  selectedRuntimeSessionUserId: string;
  selectedThread: Thread;
  selectedArtifact: Artifact | null;
  selectedArtifactId: string | null;
  composerAttachments: ComposerAttachmentsController;
  /** P1-8：会话运行时流连接状态（顶栏与消息流尾统一呈现）。 */
  connectionStatus?: ConnectionStatus | null;
  draft: string;
  isResponding: boolean;
  modelOptions: string[];
  phase?: ChatStreamPhase | null;
  reasoningEffortDefault: string;
  reasoningEffortError: string | null;
  reasoningEffortOptions: string[];
  trajectoryStore?: import("@/hooks/workspace/use-trajectory-snapshot").TrajectoryStore | null;
  onDraftChange: (value: string) => void;
  onModelChange: (value: string) => void;
  onProviderChange: (value: string) => void;
  onReasoningEffortChange: (value: string) => void;
  onSelectArtifact: (artifactId: string) => void;
  onSelectThread: (threadId: string) => void;
  onRefreshRuntimeTeams?: () => void;
  onSelectRuntimeSessionUser: (userId: string) => void;
  onResetRuntimeClientIdentity: () => void;
  onStopResponding: () => void;
  onSubmit: () => void;
  /** P1-7：待交互统一呈现位（审批 / 提问 / 计划评审），null 时不渲染。 */
  pendingInteraction?: PendingInteraction | null;
  onResolvePendingApproval?: (requestId: string, allow: boolean) => void;
  onAnswerPendingQuestion?: (questionId: string, answer: string) => void;
  /** P1-7：计划评审与既有 artifact 面板决策入口共用同一状态（notes / 提交中）。 */
  planActionPending?: boolean;
  planNotesDraft?: string;
  onPlanNotesChange?: (value: string) => void;
  onPlanDecision?: (
    decision: Exclude<RuntimeSessionPlanModeExitDecision, "">,
  ) => void;
  onBacktrackToMessage?: (
    messageId: string,
    mode?: "conversation" | "both",
    options?: { editPrompt?: string },
  ) => void;
  backtrackDialog?: SessionBacktrackDialogState;
  backtrackError?: string | null;
  backtrackNotice?: string | null;
  backtrackPendingMessageId?: string | null;
  backtrackNavigationActive?: boolean;
  backtrackSelectedMessageId?: string | null;
  canBacktrack?: boolean;
  onCloseBacktrackDialog?: () => void;
  onConfirmBacktrack?: () => void;
  onBacktrackEditPromptChange?: (value: string) => void;
  onBacktrackModeChange?: (mode: RuntimeSessionBacktrackMode) => void;
  onBacktrackPrefillChange?: (prefill: boolean) => void;
  onSelectBacktrackNavigationMessage?: (messageId: string) => void;
  providerOptions: string[];
  /** P2-7 子片 3：运行时模型目录（`/model` 候选与弹窗，与 composer 常驻座位同源）。 */
  runtimeModels?: RuntimeModelsResponse | null;
  runtimeModelsError: string | null;
  runtimeModelsLoading: boolean;
  selectedModel: string;
  selectedProvider: string;
  selectedReasoningEffort: string;
};
