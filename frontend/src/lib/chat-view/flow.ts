/**
 * E1（§8.4）：`ChatMessage[] → ChatFlowItem[]` 扁平 flow 投影。
 *
 * 设计约束：
 * - 纯函数：不新增权威状态、不触碰 DOM、不含任何 i18n 文案（文案由渲染层决定）；
 * - `key` 稳定：沿用 `ChatViewNode.key`（P1-1 保证追加新段不改变既有 key），附加 kind 前缀；
 * - `anchorKey` = `message.id`（或 `message.id#node.key`），供回溯 / 搜索 / 引用跳转；
 * - 未知事件类型降级为 `fallback`（保留原始载荷），**不得抛错、不得吞事件**（§8.4 规则 9）；
 * - 折叠语义沿用 `projectChatView` 的 `collapsed / summary / finalAnswerStart`，
 *   仅表现层分层：收起态**不产出**过程行，由 `turn-process` 统计行承担（§13 C2）。
 */
import type { ChatMessage, MessageSegment } from "@/data/mock";

import { isSystemPromptMessage, projectChatView } from "./project";
import type { ChatViewNode } from "./types";

export type ChatFlowItemKind =
  | "assistant-step"
  | "context"
  | "fallback"
  | "notice"
  | "steering"
  | "system-prompt"
  | "tool-call"
  | "turn-process"
  | "turn-tail"
  | "user";

export type ChatFlowAnchor = {
  "data-chat-anchor-key": string;
  "data-chat-flow-key": string;
  "data-chat-flow-kind": ChatFlowItemKind;
};

/** 回合统计折叠行的计数口径：工具调用 / 带回复消息 / 子代理。 */
export type TurnProcessStats = {
  messages: number;
  subagents: number;
  toolCalls: number;
};

export type ChatFlowItem =
  | {
      kind: "context";
      key: string;
      anchorKey: string;
      source: string;
      title: string;
      /**
       * 本地 context 消息可能含多段（文段 + 代码 + 工具回执），故投影携带节点列表；
       * 与文档 §8.4 单 `node` 形状的差异在此注明（渲染层需要完整面板内容）。
       */
      nodes: ChatViewNode[];
    }
  | {
      kind: "system-prompt";
      key: string;
      anchorKey: string;
      title: string;
      nodes: ChatViewNode[];
    }
  | { kind: "user"; key: string; anchorKey: string; message: ChatMessage }
  | { kind: "steering"; key: string; anchorKey: string; message: ChatMessage }
  | { kind: "assistant-step"; key: string; anchorKey: string; node: ChatViewNode }
  | { kind: "tool-call"; key: string; anchorKey: string; node: ChatViewNode }
  | {
      kind: "turn-process";
      key: string;
      anchorKey: string;
      message: ChatMessage;
      stats: TurnProcessStats;
    }
  | {
      kind: "notice";
      key: string;
      anchorKey: string;
      tone: "error" | "info" | "warn";
      node: ChatViewNode;
    }
  | { kind: "turn-tail"; key: string; anchorKey: string; message: ChatMessage }
  | { kind: "fallback"; key: string; anchorKey: string; raw: unknown };

export type ProjectChatFlowOptions = {
  /**
   * 手动展开的回合（消息 id）：展开态才产出过程行；
   * 收起态的过程行不渲染（§13 C2 `processHidden`）。
   */
  expandedMessageIds?: readonly string[];
  /** 流式中的回合恒不折叠（§8.4 规则 8）。 */
  streamingMessageId?: string | null;
  /** 子代理计数（轨迹侧统计；本地 segment 模型暂无对应段）。 */
  subagentCountByMessage?: Readonly<Record<string, number>>;
};

/** 未知类型探测：协议演进时新增的 segment 类型必须走 `fallback` 降级。 */
const KNOWN_SEGMENT_TYPES: ReadonlySet<string> = new Set([
  "callout",
  "checklist",
  "code",
  "image",
  "image-placeholder",
  "reasoning",
  "receipt",
  "text",
  "tool",
]);

/** steering：本地协议暂无该事件，以消息 label 标记为准（保留契约，便于协议演进）。 */
export function isSteeringMessage(message: Pick<ChatMessage, "label">): boolean {
  return message.label === "steering";
}

/** 工具回执行：历史里的工具执行结果（`role="tool"` → label/author 标记）。 */
export function isToolReceiptMessage(
  message: Pick<ChatMessage, "author" | "label">,
): boolean {
  if (isSystemPromptMessage(message)) {
    return false;
  }
  return message.label === "tool" || message.author === "Tool receipt";
}

/** 历史上下文消息：system/message 与工具回执之外的注入历史（协议演进保留）。 */
export function isContextMessage(
  message: Pick<ChatMessage, "author" | "label">,
): boolean {
  if (isSystemPromptMessage(message) || isToolReceiptMessage(message)) {
    return false;
  }
  return message.label === "context" || message.author === "Context";
}

/** 段落 → flow kind；未知类型返回 `fallback`。 */
export function segmentFlowKind(segment: MessageSegment): ChatFlowItemKind {
  if (!KNOWN_SEGMENT_TYPES.has(segment.type)) return "fallback";
  if (segment.type === "tool") return "tool-call";
  if (segment.type === "callout") return "notice";
  return "assistant-step";
}

/** callout tone → notice tone（success 归入 info：本地色板用 teal 表达正向）。 */
function noticeTone(segment: MessageSegment): "error" | "info" | "warn" {
  if (segment.type !== "callout") return "info";
  return segment.tone === "warning" ? "warn" : "info";
}

/** 历史上下文行的「来源」摘要：首行文本（注入文件/包名），无文本时退回 label。 */
export function contextSource(message: ChatMessage): string {
  const text = message.segments
    .filter(
      (segment): segment is Extract<MessageSegment, { type: "text" }> =>
        segment.type === "text",
    )
    .map((segment) => segment.content)
    .join("\n");
  const firstLine =
    text
      .split("\n")
      .map((line) => line.trim())
      .find((line) => line.length > 0) ?? "";
  if (!firstLine) return message.label;
  return firstLine.replace(/^#+\s*/, "").slice(0, 120);
}

function anchorOf(
  kind: ChatFlowItemKind,
  key: string,
  anchorKey: string,
): ChatFlowAnchor {
  return {
    "data-chat-flow-kind": kind,
    "data-chat-flow-key": key,
    "data-chat-anchor-key": anchorKey,
  };
}

/** DOM 锚点属性（§8.4 规则 4）：e2e 定位与「跳转到某次工具调用」共用一套命名。 */
export function flowAnchor(item: ChatFlowItem): ChatFlowAnchor {
  return anchorOf(item.kind, item.key, item.anchorKey);
}

/** 单节点锚点（渲染层逐节点渲染时使用）。 */
export function nodeAnchor(
  kind: ChatFlowItemKind,
  key: string,
  anchorKey: string,
): ChatFlowAnchor {
  return anchorOf(kind, key, anchorKey);
}

function nodeItem(
  node: ChatViewNode,
  anchorKey: string,
): ChatFlowItem {
  const kind = segmentFlowKind(node.segment);
  const key = `${kind}:${node.key}`;
  if (kind === "fallback") {
    return { kind: "fallback", key, anchorKey, raw: node.segment };
  }
  if (kind === "tool-call") {
    return { kind: "tool-call", key, anchorKey, node };
  }
  if (kind === "notice") {
    return { kind: "notice", key, anchorKey, tone: noticeTone(node.segment), node };
  }
  return { kind: "assistant-step", key, anchorKey, node };
}

/**
 * 展开单条消息为 N 个 item（§8.4 规则 1）。
 * 收起态：过程行不产出，只留 `turn-process` 统计行 + 最终回答 + `turn-tail`。
 */
export function projectMessageFlow(
  message: ChatMessage,
  options: ProjectChatFlowOptions = {},
): ChatFlowItem[] {
  const anchorKey = message.id;

  if (message.role === "user") {
    const kind = isSteeringMessage(message) ? "steering" : "user";
    return [{ kind, key: `${kind}:${message.id}`, anchorKey, message }];
  }

  if (isSystemPromptMessage(message)) {
    const nodes = projectChatView(message, { expanded: true }).nodes;
    return [
      {
        kind: "system-prompt",
        key: `system-prompt:${message.id}`,
        anchorKey,
        title: message.label,
        nodes,
      },
    ];
  }

  if (isToolReceiptMessage(message)) {
    const nodes = projectChatView(message, { expanded: true }).nodes;
    const toolNodes = nodes.filter((node) => node.kind === "tool");
    if (toolNodes.length > 0) {
      // §8.4 规则 1：tool 段 → 1 个 tool-call（历史回执同理，不再并入 context）。
      return toolNodes.map((node) => nodeItem(node, anchorKey));
    }
    // 降级：旧数据里没有可识别的 tool segment 时仍按上下文行呈现，不丢内容。
    return [
      {
        kind: "context",
        key: `context:${message.id}`,
        anchorKey,
        source: contextSource(message),
        title: message.label,
        nodes,
      },
    ];
  }

  if (isContextMessage(message)) {
    const nodes = projectChatView(message, { expanded: true }).nodes;
    return [
      {
        kind: "context",
        key: `context:${message.id}`,
        anchorKey,
        source: contextSource(message),
        title: message.label,
        nodes,
      },
    ];
  }

  const streaming = message.id === options.streamingMessageId;
  const processExpanded = (options.expandedMessageIds ?? []).includes(message.id);
  const view = projectChatView(message, {
    streaming,
    subagentCount: options.subagentCountByMessage?.[message.id],
  });

  const items: ChatFlowItem[] = [];
  if (view.collapsed && view.summary) {
    items.push({
      kind: "turn-process",
      key: `turn-process:${message.id}`,
      anchorKey,
      message,
      stats: {
        toolCalls: view.summary.tools,
        messages: view.summary.replies,
        subagents: view.summary.subagents,
      },
    });
    // 收起即隐藏过程行（§13 C2）；展开后过程行回到 24px 单行。
    if (processExpanded) {
      items.push(...view.hiddenNodes.map((node) => nodeItem(node, anchorKey)));
    }
  }

  items.push(...view.nodes.map((node) => nodeItem(node, anchorKey)));

  items.push({
    kind: "turn-tail",
    key: `turn-tail:${message.id}`,
    anchorKey,
    message,
  });
  return items;
}

/** 消息序列 → 扁平 flow（渲染层可逐 item 渲染为同级兄弟）。 */
export function projectChatFlow(
  messages: readonly ChatMessage[],
  options: ProjectChatFlowOptions = {},
): ChatFlowItem[] {
  return messages.flatMap((message) => projectMessageFlow(message, options));
}
