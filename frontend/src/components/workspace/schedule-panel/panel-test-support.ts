// SchedulePanel 测试共用的任务 fixture 与控制器 mock 工厂。

import { vi } from "vitest";

import type { ScheduleTasksController } from "@/hooks/workspace/use-schedule-tasks";
import type { RuntimeScheduleTask } from "@/types/runtime";

export function task(
  overrides: Partial<RuntimeScheduleTask> = {},
): RuntimeScheduleTask {
  return {
    id: "task-1",
    kind: "every",
    title: "Ping",
    prompt: "ping",
    scheduledAt: "2099-10-11T12:05:00Z",
    everySeconds: 300,
    sessionId: "session-1",
    status: "active",
    ...overrides,
  };
}

export function scheduleController(
  overrides: Partial<ScheduleTasksController> = {},
): ScheduleTasksController {
  return {
    activeCount: 0,
    controlTask: vi.fn(),
    controllingAction: null,
    controllingId: "",
    createTask: vi.fn().mockResolvedValue({ ok: true }),
    creating: false,
    deletingId: "",
    error: null,
    hasActiveTasks: false,
    loading: false,
    refresh: vi.fn(),
    removeTask: vi.fn(),
    saveTask: vi.fn().mockResolvedValue({ ok: true }),
    savingId: "",
    tasks: [],
    ...overrides,
  };
}