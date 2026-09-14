// 由 components/workspace/message-list.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import type { CSSProperties } from "react";

import type { Artifact, ChatMessage } from "@/data/mock";
import type { ConnectionStatus } from "@/lib/connection-status";
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
  canBacktrack?: boolean;
  className?: string;
  /** P1-8：会话运行时流连接状态（非在线态在消息流尾可见并附手动重试）。 */
  connectionStatus?: ConnectionStatus | null;
  contentClassName?: string;
  isResponding: boolean;
  messages: ChatMessage[];
  onBacktrackToMessage?: (
    messageId: string,
    mode?: "conversation" | "both",
    options?: MessageBacktrackOptions,
  ) => void;
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
