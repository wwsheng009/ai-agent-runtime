// P1-6 工具行 barrel：对外只暴露纯逻辑与类型，组件从各自模块直接引入。

export type { ToolDiffStats, ToolSegmentDetails } from "./details";
export {
  countContentLines,
  countDiffLines,
  extractToolDetails,
  matchToolFilePath,
  parseToolDetailsFromArgsText,
  resolveToolSegmentDetails,
} from "./details";

export type { ToolCardKind } from "./kind";
export { resolveToolCardKind } from "./kind";

export type {
  ToolRowPresentation,
  ToolRowSummary,
  ToolRowSummaryPart,
  ToolStatus,
  ToolStatusTone,
} from "./state";
export {
  isToolRowExpandable,
  resolveToolRowPresentation,
  STATUS_LABEL_KEY,
  STATUS_TONE,
} from "./state";

export type { FilePathLink } from "./link";
export { isExternalUrl } from "./link";
