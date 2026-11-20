// 右侧栏「自动化任务」面：复用全局 SchedulePanel，并注入仅右栏需要的
// 「全局自动化」入口。不要在 SchedulePanel 内部默认显示该入口，否则全局页
// 和独立日程页会链接到自身。
import type { WorkspacePanelSurfaceProps } from "@/components/workspace/panel-registry";

import { SchedulePanel } from "./panel";

export function RightRailSchedulePanel({
  lastRuntimeEventType,
  runtimeEventCount,
  sessionId,
  workspacePath,
}: WorkspacePanelSurfaceProps) {
  return (
    <SchedulePanel
      lastRuntimeEventType={lastRuntimeEventType}
      runtimeEventCount={runtimeEventCount}
      sessionId={sessionId}
      showGlobalAutomationLink
      workspacePath={workspacePath}
    />
  );
}
