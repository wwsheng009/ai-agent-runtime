// P2-1A：后台任务（Jobs）弹层——P0-2 机械拆分后的稳定入口（barrel）。
// 组件/分区/行组件/行内状态类型已拆至 ./jobs-panel/*；本文件只做再导出，
// 保持既有导入路径 @/components/workspace/jobs-panel 与全部具名导出不变。

export { JobsPanel } from "./jobs-panel/panel";
export type { JobsPanelProps } from "./jobs-panel/panel";
