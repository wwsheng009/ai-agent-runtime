// P3：自动化任务（Schedule）面板——P0-2 机械拆分后的稳定入口（barrel）。
// 组件与内部子组件/纯函数已拆至 ./schedule-panel/*；本文件只做再导出，
// 保持既有导入路径 @/components/workspace/schedule-panel 与全部具名导出不变。

export { SchedulePanel } from "./schedule-panel/panel";
export type { SchedulePanelProps } from "./schedule-panel/panel";
