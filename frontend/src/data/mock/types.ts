// 由 data/mock.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import type { ToolSegmentDetails } from "@/lib/tool-row/details";
import type { TurnUsage } from "@/lib/turn-usage";

export type MessageSegment =
  | {
      type: "text";
      content: string;
    }
  | {
      type: "reasoning";
      content: string;
      running?: boolean;
    }
  | {
      type: "tool";
      toolCallId?: string;
      name: string;
      status: "started" | "running" | "finished" | "error";
      argsSummary?: string;
      resultSummary?: string;
      errorMessage?: string;
      /** P1-6：工具事件结构化明细（文件路径 / 命令 / 查询 / URL / diff 行数 / 退出码），缺失即不渲染。 */
      details?: ToolSegmentDetails;
    }
  | {
      type: "code";
      language: "bash" | "json" | "tsx" | "ts" | "html";
      code: string;
      title?: string;
    }
  | {
      type: "checklist";
      title: string;
      items: string[];
    }
  | {
      type: "receipt";
      title: string;
      items: Array<{
        label: string;
        value: string;
        tone?: "accent" | "warning" | "muted";
      }>;
    }
  | {
      type: "callout";
      title: string;
      content: string;
      tone?: "info" | "warning" | "success";
    }
  | {
      type: "image";
      src: string;
      alt?: string;
      caption?: string;
      width?: number;
      height?: number;
      artifactId?: string;
      imageId?: string;
    }
  | {
      type: "image-placeholder";
      imageId: string;
      phase: "started" | "partial" | "completed" | "failed";
      progress?: number;
      caption?: string;
      errorMessage?: string;
    };

export type ChatMessage = {
  id: string;
  role: "user" | "assistant";
  author: string;
  label: string;
  segments: MessageSegment[];
  relatedArtifactIds?: string[];
  interrupted?: boolean;
  /** Durable identity used to isolate runtime deltas across chat turns. */
  runtimeTurnId?: string;
  /** True only while the assistant message is receiving process events. */
  streaming?: boolean;
  /** P1-1：Turn token 用量；不完整或未知时为 null/undefined（渲染层隐藏整行）。 */
  usage?: TurnUsage | null;
};

export type Artifact = {
  id: string;
  name: string;
  path: string;
  summary: string;
  kind: "code" | "html" | "json" | "image";
  language?: "json" | "tsx" | "ts" | "html";
  content: string;
  previewHtml?: string;
  mimeType?: string;
  byteCount?: number;
  sha256?: string;
  revisedPrompt?: string;
};

export type Thread = {
  id: string;
  title: string;
  summary: string;
  updatedAt: string;
  status: "active" | "draft" | "review";
  sessionId?: string;
  transport?: "mock" | "live" | "error";
  runtimeSource?: string;
  runtimeEventCount?: number;
  lastRuntimeEventType?: string;
  lastError?: string | null;
  /** 会话级 reasoning effort 覆盖；空值表示跟随 config.yaml 默认档位。 */
  reasoningEffort?: string;
  /**
   * 批次 3（§5.5）：会话分支来源（由快照 `metadata.context` 的谱系键映射）。
   * 纯前端视图字段，不入后端请求；缺省 = 非分支会话。
   */
  forkedFrom?: {
    sessionId: string;
    /** 分支锚点消息 id（整会话分支缺省）。 */
    anchorMessageId?: string;
    /** 分支时的来源会话标题快照（父会话改名后不回溯）。 */
    originTitle?: string;
  };
  tags: string[];
  prompts: string[];
  messages: ChatMessage[];
  artifacts: Artifact[];
};
