/** P1-1：会话视图投影内核 barrel。 */
export { findFinalAnswerStart, summarizeCollapsedEvidence } from "./collapse";
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
