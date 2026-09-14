/** P1-1/E1：会话视图投影内核 barrel（视图投影 + 扁平 flow 投影）。 */
export { findFinalAnswerStart, summarizeCollapsedEvidence } from "./collapse";
export {
  contextSource,
  flowAnchor,
  isContextMessage,
  isSteeringMessage,
  isToolReceiptMessage,
  nodeAnchor,
  projectChatFlow,
  projectMessageFlow,
  segmentFlowKind,
} from "./flow";
export type {
  ChatFlowAnchor,
  ChatFlowItem,
  ChatFlowItemKind,
  ProjectChatFlowOptions,
  TurnProcessStats,
} from "./flow";
export { buildNodes, isAnswerSegment, isEvidenceSegment } from "./nodes";
export { isSystemPromptMessage, projectChatView } from "./project";
export type {
  ChatViewNode,
  ChatViewNodeKind,
  ChatViewProjection,
  CollapsedSummaryPart,
  CollapsedTurnSummary,
  ProjectChatViewOptions,
} from "./types";
