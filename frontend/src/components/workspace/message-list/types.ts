// 由 components/workspace/message-list.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import type { CSSProperties } from "react";

import type { Artifact, ChatMessage } from "@/data/mock";
import type { ConnectionStatus } from "@/lib/connection-status";
import type { HistoryEarlierLoader } from "@/lib/thread-state/history-paging";
import type { ChatStreamPhase } from "@/types/runtime";

export type MessageBacktrackOptions = {
  editPrompt?: string;
};

export type MessageListProps = {
  artifacts: Artifact[];
  backtrackError?: string | null;
  backtrackNotice?: string | null;
  backtrackPendingMessageId?: string | null;
  backtrackNavigationActive?: boolean;
  backtrackSelectedMessageId?: string | null;
  /** 批次 2（§5.4）：分支失败提示（§6.1 错误表：失败不改路由，只在消息流顶部提示原因）。 */
  branchError?: string | null;
  /** 批次 2（§5.4）：在途的分支锚点消息 id（消息级）；与 `onBranchFromMessage` 配套驱动 pending。 */
  branchPendingMessageId?: string | null;
  canBacktrack?: boolean;
  className?: string;
  /** P1-8：会话运行时流连接状态（非在线态在消息流尾可见并附手动重试）。 */
  connectionStatus?: ConnectionStatus | null;
  contentClassName?: string;
  /**
   * 会话历史尾部优先分页的入口（P3-1）：顶部「加载更早」按钮 + 滚到顶端自动续页，
   * 两者共用同一条幂等入口；缺省（无分页元数据）时入口不渲染。
   */
  earlierLoader?: HistoryEarlierLoader;
  /**
   * 批次 2（§5.4）：存在未决的审批 / 提问 / 计划评审时，当前轮次尚未定型，
   * 整段历史不产出分支锚点（防止把挂起中的轮尾误判为已完成轮次）。
   */
  hasPendingApproval?: boolean;
  isResponding: boolean;
  messages: ChatMessage[];
  onBacktrackToMessage?: (
    messageId: string,
    mode?: "conversation" | "both",
    options?: MessageBacktrackOptions,
  ) => void;
  /**
   * 批次 2（§5.4）：消息级分支入口（「在新对话中分支」）。
   * 宿主只传入回调即可；可用性由 `resolveBranchAnchors` 在整段历史上求解，
   * 只有可分支锚点（每个已完成轮次的末条回答消息）会拿到并渲染入口。
   */
  onBranchFromMessage?: (messageId: string) => void;
  /** P2-1A：工具行文件路径的运行时预览入口（未命中关联产物时兜底）。 */
  onPreviewFilePath?: (path: string) => void;
  onSelectBacktrackNavigationMessage?: (messageId: string) => void;
  onSelectArtifact: (artifactId: string) => void;
  onRetryConnection?: () => void;
  phase?: ChatStreamPhase | null;
  /** P1-3：跨挂载滚动记忆键（线程 id）；缺省时滚动锚点只在本次挂载内有效。 */
  scrollMemoryKey?: string | null;
  style?: CSSProperties;
};
