/**
 * 回合启动准备（工作区多会话并发 Batch 1 机械拆分）。
 *
 * 从 `useWorkspaceAgentChatTurn.submitPrompt` 拆出「不依赖 React 状态」的一段：
 * 定位发起会话 → 解析回合键（轨迹 store 与注册表条目共用同一 key）→ 组装请求
 * 载荷与用户消息 / 请求工件 → 建立回合身份（AbortController + 运行时状态）。
 *
 * 返回值逐项对应拆出前的局部变量；`deltaCoordinator.beginTurn` 仍在返回前调用
 * （必须在首个 SSE 帧之前注册回合账目，顺序与拆出前一致）。
 */
import type { AppSettings } from "@/core/settings";
import { type Artifact, type ChatMessage, type Thread } from "@/data/mock";
import { resolveChatTurnWorkspacePath } from "@/hooks/workspace/agent-chat-turn/shared";
import { createThreadFromPrompt } from "@/hooks/workspace/agent-chat-turn/thread-factory";
import {
  createTurnRuntimeState,
  type ChatTurnRuntimeState,
} from "@/hooks/workspace/agent-chat-turn/turn-state";
import type { TrajectoryStore } from "@/hooks/workspace/use-trajectory-snapshot";
import { resolveTrajectoryStoreKey } from "@/hooks/workspace/use-trajectory-store-pool";
import { NEW_THREAD_ID } from "@/hooks/workspace/use-workspace-thread-selection";
import { normalizeSessionId } from "@/lib/session-id";
import type { TrajectoryStorePool } from "@/lib/trajectory/store-pool";
import {
  buildTurnJsonArtifact,
  type RuntimeDeltaCoordinator,
} from "@/lib/workspace-thread-state";
import type { AgentChatRequest } from "@/types/runtime";

/**
 * 提交回合的可选覆盖：`/skill` 用 prompt 覆盖草稿并声明本轮 expose_skills；
 * `images`（S5）是已上传成功的服务端附件路径，走运行时命令通道投递
 * （`submit_prompt.images`）——本结构仅承载入参，不进入 `/api/agent/chat` 载荷。
 */
export type AgentChatSubmitOptions = {
  prompt?: string;
  exposeSkills?: readonly string[];
  images?: readonly string[];
};

export type AgentChatTurnBootstrapInput = {
  prompt: string;
  selectedThread: Thread;
  /** 页面级池：回合按**发起会话**的键取 store（后台/草稿回合不写进选中会话）。 */
  trajectoryStorePool?: TrajectoryStorePool;
  /** 无池时的回退 store（旧行为：单实例）。 */
  trajectoryStore: TrajectoryStore;
  deltaCoordinator?: RuntimeDeltaCoordinator;
  userId?: string;
  workspacePath?: string;
  selectedProvider: string;
  selectedModel: string;
  selectedReasoningEffort: string;
  settings: AppSettings;
  /** P2：本回合暴露给模型的 skill 名单（省略/空数组 = 不发送该字段）。 */
  exposeSkills?: readonly string[];
};

export type AgentChatTurnBootstrap = {
  threadSnapshot: Thread;
  threadId: string;
  turnKey: string;
  turnTrajectoryStore: TrajectoryStore;
  sessionIdBeforeTurn: string;
  turnId: string;
  assistantMessageId: string;
  userMessage: ChatMessage;
  requestPayload: AgentChatRequest;
  requestArtifact: Artifact;
  turnState: ChatTurnRuntimeState;
  controller: AbortController;
};

export function prepareAgentChatTurn(
  input: AgentChatTurnBootstrapInput,
): AgentChatTurnBootstrap {
  const { prompt, selectedThread, settings } = input;
  const threadSnapshot =
    selectedThread.id === NEW_THREAD_ID
      ? createThreadFromPrompt(prompt)
      : selectedThread;
  const threadId = threadSnapshot.id;
  // 回合账目（轨迹 store / 注册表条目）按**发起会话**定位：草稿线程落库后的
  // 键升级由注册表与 store 池的 adopt 承接，后台回合不写进选中会话的 store。
  const turnKey = resolveTrajectoryStoreKey(
    threadSnapshot.sessionId,
    threadSnapshot.id,
  );
  const turnTrajectoryStore = input.trajectoryStorePool
    ? input.trajectoryStorePool.acquire(turnKey)
    : input.trajectoryStore;
  const sessionIdBeforeTurn = normalizeSessionId(threadSnapshot.sessionId ?? "");
  const turnId = crypto.randomUUID();
  const assistantMessageId = `turn-${turnId}-assistant`;
  input.deltaCoordinator?.beginTurn(turnId);
  const userMessage: ChatMessage = {
    id: crypto.randomUUID(),
    role: "user",
    author: "You",
    label: "draft",
    segments: [{ type: "text", content: prompt }],
  };
  const requestPayload: AgentChatRequest = {
    messages: [{ role: "user" as const, content: prompt }],
    session_id: threadSnapshot.sessionId,
    turn_id: turnId,
    user_id: input.userId || undefined,
    workspace_path: resolveChatTurnWorkspacePath(
      threadSnapshot.sessionId,
      input.workspacePath,
    ),
    provider: input.selectedProvider || undefined,
    model: input.selectedModel || undefined,
    reasoning_effort: input.selectedReasoningEffort || undefined,
    ...(input.exposeSkills && input.exposeSkills.length > 0
      ? { expose_skills: [...input.exposeSkills] }
      : {}),
    enable_react: settings.chat.enableReact,
    enable_routing: true,
    // P4-刷新续传：页面刷新/关标签会 abort 这个 POST，服务器随后照常取消
    // `r.Context()`。声明 resume_on_disconnect 后回合与请求解耦继续执行，
    // 刷新后的新页面据 `/runtime` 的 active_turn 重新挂载回合身份，
    // 在 `/runtime/stream` 上按游标续传（否则刷新即中止本回合）。
    resume_on_disconnect: true,
    max_steps: settings.chat.maxSteps,
  };
  const requestArtifact = buildTurnJsonArtifact(
    turnId,
    "agent-chat-request",
    "Streaming request payload sent from the Vite workspace to /api/agent/chat.",
    {
      ...requestPayload,
      stream: true,
    },
  );

  return {
    assistantMessageId,
    controller: new AbortController(),
    requestArtifact,
    requestPayload,
    sessionIdBeforeTurn,
    threadId,
    threadSnapshot,
    turnId,
    turnKey,
    turnState: createTurnRuntimeState(threadSnapshot),
    turnTrajectoryStore,
    userMessage,
  };
}
