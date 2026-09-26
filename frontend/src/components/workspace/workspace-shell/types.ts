// 由 components/workspace/workspace-shell.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type Artifact, type Thread } from "@/data/mock";
import type { ComposerAttachmentsController } from "@/hooks/workspace/composer/use-composer-attachments";
import { type AgentChatSubmitOptions } from "@/hooks/workspace/agent-chat-turn/turn-bootstrap";
import { type ComposerImageSubmitController } from "@/hooks/workspace/composer/use-composer-image-submit";
import { type RuntimeSessionsSummary } from "@/hooks/workspace/use-runtime-sessions-data";
import type { SessionBacktrackDialogState } from "@/hooks/workspace/use-session-backtrack";
import { type ConnectionStatus } from "@/lib/connection-status";
import type { PendingInteraction } from "@/lib/pending-interaction";
import { type RuntimeClientIdentity } from "@/lib/runtime-client";
import {
  type RuntimeSessionBacktrackMode,
  type RuntimeSessionPlanMode,
  type RuntimeSessionPlanModeExitDecision,
  type RuntimeSessionRecord,
  type RuntimeSessionUserSummary,
  type RuntimeModelsResponse,
  type RuntimeTeamRecord,
  type RuntimeTeamSummaryEntry,
  type RuntimeWorkspaceDirectory,
} from "@/lib/runtime-api";
import { type RuntimeSkillCatalog } from "@/types/runtime";

export type RuntimeSkillsResponse = RuntimeSkillCatalog;
import { type ChatStreamPhase } from "@/types/runtime";

import { type SidebarSessionActivity } from "@/components/workspace/workspace-sidebar/session-row-status";

/**
 * 尾部优先回放（tail-first）的「加载更早」入口。
 *
 * 首屏只回放最近一页事件（见 hooks/workspace/use-trajectory-recovery.ts），
 * 更早的页由轨迹视图顶部入口按需前插；无更早页时 hasEarlier=false，入口隐藏。
 */
export type TrajectoryEarlierEntry = {
  /** 窗口之前还有更早一页（服务端 has_more）。 */
  hasEarlier: boolean;
  /** 在途：入口禁用，避免并发重复翻页。 */
  loading: boolean;
  /** 加载更早一页（前插到窗口之前）。 */
  onLoad: () => void;
};

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
  runtimeSessionUsers: RuntimeSessionUserSummary[];
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
  onRefreshWorkspaceDirectories?: () => void;
  /** P1-8：手动重试连接（复用既有 seq 游标续传，不新建退避循环）。 */
  onRetryConnection?: () => void;
  /** 顶栏「刷新当前会话」：重拉权威历史 / 运行时状态快照 / 会话列表投影；缺省不渲染入口。 */
  onRefreshSession?: () => void | Promise<void>;
  /** 刷新在途标记（顶栏刷新按钮禁用并转圈）。 */
  sessionRefreshing?: boolean;
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
  /** P1-9 Fork（2026-09 升级）：以会话末尾为锚点的**整会话分支**——服务端把源会话
   *  历史前缀复制进新会话，源会话零改动；目录归属由服务端继承，前端不传工作目录。 */
  onForkRuntimeSession?: (
    sessionId: string,
    sourceTitle: string,
  ) => Promise<void> | void;
  /** 批次 2（分支方案 §5.4）：消息级「在新对话中分支」；缺省时轮尾行不渲染分支按钮。 */
  onBranchFromMessage?: (messageId: string) => void;
  /** 在途的分支锚点消息 id（消息级）；与 `onBranchFromMessage` 配套驱动按钮 pending。 */
  branchPendingMessageId?: string | null;
  /** 分支失败提示（§6.1 错误表：失败不改路由，只在消息流顶部提示原因）。 */
  branchError?: string | null;
  /** P1-9 非破坏删除：仅移除会话记录，不连带目录与磁盘数据。 */
  onDeleteRuntimeSession?: (sessionId: string) => Promise<void> | void;
  /**
   * §4.8 后台会话停止：按会话投递 `interrupt`（不要求本地持有该会话的 controller）。
   * 缺省时侧栏会话行不渲染「停止运行」菜单项。
   */
  onStopRuntimeSession?: (sessionId: string) => Promise<void> | void;
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
  /** 会话是否在生成回复（本地回合或刷新后认领的续传回合）：composer 停止态与流尾提示都按它判定。 */
  isResponding: boolean;
  /** 读侧静默看门狗命中：本页 chat 流已停止接收（服务端回合可能仍在跑）。 */
  streamStalled?: boolean;
  modelOptions: string[];
  phase?: ChatStreamPhase | null;
  reasoningEffortDefault: string;
  reasoningEffortError: string | null;
  reasoningEffortOptions: string[];
  trajectoryStore?: import("@/hooks/workspace/use-trajectory-snapshot").TrajectoryStore | null;
  /** 尾部优先回放的「加载更早」入口（可选：无窗口语义时不传）。 */
  trajectoryEarlier?: TrajectoryEarlierEntry;
  /** 对话面「加载更早」入口（会话历史尾部优先分页；可选：无分页元数据时不传）。 */
  earlierLoader?: import("@/lib/thread-state/history-paging").HistoryEarlierLoader;
  onDraftChange: (value: string) => void;
  onModelChange: (value: string) => void;
  onProviderChange: (value: string) => void;
  onReasoningEffortChange: (value: string) => void;
  onSelectArtifact: (artifactId: string) => void;
  onSelectThread: (threadId: string) => void;
  onRefreshRuntimeTeams?: () => void;
  onRefreshRuntimeSessions?: () => void;
  onResetRuntimeClientIdentity: () => void;
  onStopResponding: () => void;
  /** `options` 供 `/skill` 回合化覆盖 prompt / expose_skills；返回 false = 未启动。 */
  onSubmit: (options?: AgentChatSubmitOptions) => boolean | void;
  /** S5：带图发送（`submit_prompt.images`）的控制器（回执 + 关闭 + 启动）。 */
  imageSubmitFeedback?: ComposerImageSubmitController | null;
  /** P1-7：待交互统一呈现位（审批 / 提问 / 计划评审），null 时不渲染。 */
  pendingInteraction?: PendingInteraction | null;
  /** §4.6 常驻模式标识：页面既有的 `/plan` 快照（与右侧「计划」面板同源）。 */
  plan?: RuntimeSessionPlanMode | null;
  planStatusLabel?: string;
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
  /** 运行时 skill 目录（`/skill` 候选与弹窗）。 */
  runtimeSkills?: RuntimeSkillsResponse | null;
  runtimeSkillsError: string | null;
  runtimeSkillsLoading: boolean;
  selectedModel: string;
  selectedProvider: string;
  selectedReasoningEffort: string;
};

/**
 * 工作台中部视图页签。`skills` 为 P2-1B 新增（对话页签之后的只读技能目录），
 * 是否渲染对应按钮由各视图的数据可用性决定（如无轨迹 store 时不显示轨迹页签）。
 */
export type WorkspaceViewMode = "chat" | "skills" | "trajectory";
