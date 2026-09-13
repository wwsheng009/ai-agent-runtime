/**
 * P1-1：会话视图投影模型（Definition / Node）。
 *
 * 投影是「消息 + segments」的纯函数视图：不新增权威状态、不触碰 DOM，
 * 只给出稳定 node key 与折叠语义，供 Chat（message-list）与 Trajectory 渲染层消费。
 */
import type { MessageSegment } from "@/data/mock";

/** 节点类别：与 MessageSegment 的渲染分组一一对应。 */
export type ChatViewNodeKind = "text" | "reasoning" | "tool" | "rich" | "system";

/** 折叠摘要的展示片段（结构化，不含文案；文案由渲染层 i18n 决定）。 */
export type CollapsedSummaryPart = {
  kind: "tools" | "replies" | "subagents";
  count: number;
};

/** Turn 折叠摘要：零值段省略；全零时 empty = true（渲染层显示统一占位）。 */
export type CollapsedTurnSummary = {
  tools: number;
  /** 带回复消息数：有结果或错误回执的工具条数。 */
  replies: number;
  subagents: number;
  parts: CollapsedSummaryPart[];
  empty: boolean;
};

export type ChatViewNode = {
  /** 稳定投影 key：Turn 内唯一；追加新段不会改变既有 key。 */
  key: string;
  kind: ChatViewNodeKind;
  /** 证据原始 id（工具 callId / 图片 imageId 等），无则 undefined。 */
  id?: string;
  /** 关联跳转目标（artifact），无则 undefined。 */
  target?: string;
  /** 是否属于「过程证据」：Turn 结束后可被折叠。 */
  evidence: boolean;
  /** 原始 segment，渲染层直接消费（投影不复制内容）。 */
  segment: MessageSegment;
};

export type ChatViewProjection = {
  /** 可见节点（折叠时 = 最终回答；未折叠时 = 全部节点）。 */
  nodes: ChatViewNode[];
  /** 被折叠隐藏的过程证据节点（未折叠时为空数组）。 */
  hiddenNodes: ChatViewNode[];
  /** 是否处于折叠态。 */
  collapsed: boolean;
  /** 折叠摘要（未折叠为 null）。 */
  summary: CollapsedTurnSummary | null;
  /** 最终回答起始下标（相对全量节点；-1 = 无最终回答，不折叠）。 */
  finalAnswerStart: number;
  /** 是否为 system/message：渲染为请求前折叠的 System prompt 行。 */
  systemPrompt: boolean;
};

/** 投影输入：只需要消息渲染所需的最小面。 */
export type ProjectChatViewOptions = {
  /** 流式中的 Turn 不折叠（过程可见）。 */
  streaming?: boolean;
  /** 外部强制展开（用户手动展开折叠）。 */
  expanded?: boolean;
  /** 子代理数（来自轨迹/事件侧统计；chat segment 模型暂无对应段）。 */
  subagentCount?: number;
};
