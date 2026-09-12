// 由 data/mock.ts 机械拆分而来（P0-2），仅搬迁不改语义。

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
  tags: string[];
  prompts: string[];
  messages: ChatMessage[];
  artifacts: Artifact[];
};
