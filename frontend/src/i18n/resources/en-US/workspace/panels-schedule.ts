// P3：workspace.panels.schedule 英文文案模块；形状由 zh-CN 同名模块经 satisfies 编译期约束。
import type { DeepStringShape } from "../../shape";
import type { zhWorkspacePanelsSchedule } from "../../zh-CN/workspace/panels-schedule";

export const enWorkspacePanelsSchedule: DeepStringShape<typeof zhWorkspacePanelsSchedule> = {
  title: "Automation tasks",
  refresh: "Refresh automation tasks",
  empty: "No automation tasks yet; tasks created in sessions will appear here",
  activeSection: "Active",
  inactiveSection: "Not running",
  statusActive: "Active",
  statusInactive: "Not running",
  nextRun: "Next run",
  lastDelivery: "Last delivery",
  titleLabel: "Task name",
  promptLabel: "Task instruction",
  timeLabel: "Local time",
  zoneLabel: "Time zone",
  timePlaceholder: "HH:MM:SS",
  zonePlaceholder: "Asia/Shanghai",
  edit: "Edit",
  save: "Save",
  cancel: "Cancel",
  delete: "Delete task",
  confirmDelete: "Confirm delete",
  saved: "Saved",
  saveFailed: "Save failed: {{message}}",
  deleted: "Task deleted",
  deleteFailed: "Delete failed: {{message}}",
  kind: {
    after: "One-shot (delay)",
    at: "One-shot (absolute)",
    every: "Fixed interval",
    daily: "Daily",
    weekly: "Weekly",
    cron: "Cron",
  },
};
