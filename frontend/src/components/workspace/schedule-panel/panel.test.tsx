// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { RuntimeScheduleTask } from "@/types/runtime";

import { SchedulePanel } from "./panel";

const { useScheduleTasksMock } = vi.hoisted(() => ({
  useScheduleTasksMock: vi.fn(),
}));

vi.mock("@/hooks/workspace/use-schedule-tasks", () => ({
  useScheduleTasks: useScheduleTasksMock,
}));

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function task(overrides: Partial<RuntimeScheduleTask> = {}): RuntimeScheduleTask {
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

describe("SchedulePanel actions", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;

    const noop = vi.fn();
    useScheduleTasksMock.mockReturnValue({
      controlTask: noop,
      controllingAction: null,
      controllingId: "",
      createTask: vi.fn().mockResolvedValue({ ok: true }),
      creating: false,
      deletingId: "",
      error: null,
      loading: false,
      refresh: noop,
      removeTask: vi.fn(),
      saveTask: vi.fn(),
      savingId: "",
      tasks: [],
    });
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
      root = null;
    }
    document.body.innerHTML = "";
    vi.useRealTimers();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
    useScheduleTasksMock.mockReset();
  });

  it("通过独立对话框创建任务，表单不进入面板布局", () => {
    act(() => {
      root?.render(
        <SchedulePanel sessionId="session-1" />,
      );
    });

    const trigger = Array.from(
      container.querySelectorAll<HTMLButtonElement>("button"),
    ).find((button) => button.textContent?.includes("新建任务"));
    if (!trigger) {
      throw new Error("missing new task trigger");
    }

    expect(trigger.getAttribute("aria-haspopup")).toBe("dialog");
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    expect(container.querySelector("form")).toBeNull();

    act(() => {
      trigger.focus();
      trigger.click();
    });

    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    // portal 到 body：面板局部树中不应再出现表单，弹层也不应被滚动容器裁切。
    expect(container.querySelector("form")).toBeNull();
    const dialog = document.querySelector<HTMLElement>(
      '[data-testid="schedule-create-dialog"]',
    );
    expect(dialog).not.toBeNull();
    const form = dialog?.querySelector("form");
    if (!form) throw new Error("missing create form");
    act(() => {
      form.dispatchEvent(
        new Event("submit", { bubbles: true, cancelable: true }),
      );
    });
    expect(
      document.querySelector('[data-testid="schedule-create-title"]')?.getAttribute(
        "aria-invalid",
      ),
    ).toBe("true");
    expect(
      document.querySelector('[data-testid="schedule-create-prompt"]')?.getAttribute(
        "aria-invalid",
      ),
    ).toBe("true");

    const cancel = Array.from(
      dialog?.querySelectorAll<HTMLButtonElement>("button") ?? [],
    ).find((button) => button.textContent?.trim() === "取消");
    if (!cancel) {
      throw new Error("missing cancel action");
    }

    act(() => cancel.click());

    expect(document.querySelector('[data-testid="schedule-create-dialog"]')).toBeNull();
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    expect(document.activeElement).toBe(trigger);
  });

  it("任务行提供编辑、暂停/恢复动作并复用表单对话框", async () => {
    const controlTask = vi.fn().mockResolvedValue({ ok: true });
    const saveTask = vi.fn().mockResolvedValue({ ok: true });
    useScheduleTasksMock.mockReturnValue({
      controlTask,
      controllingAction: null,
      controllingId: "",
      createTask: vi.fn().mockResolvedValue({ ok: true }),
      creating: false,
      deletingId: "",
      error: null,
      loading: false,
      refresh: vi.fn(),
      removeTask: vi.fn(),
      saveTask,
      savingId: "",
      tasks: [task()],
    });

    act(() => {
      root?.render(<SchedulePanel sessionId="session-1" />);
    });

    const edit = Array.from(
      container.querySelectorAll<HTMLButtonElement>("button"),
    ).find((button) => button.textContent?.trim() === "编辑");
    const pause = Array.from(
      container.querySelectorAll<HTMLButtonElement>("button"),
    ).find((button) => button.textContent?.trim() === "暂停");
    if (!edit || !pause) {
      throw new Error("missing task actions");
    }

    await act(async () => {
      pause.click();
      await Promise.resolve();
    });
    expect(controlTask).toHaveBeenCalledWith("task-1", "pause");

    act(() => {
      edit.focus();
      edit.click();
    });
    expect(edit.getAttribute("aria-haspopup")).toBe("dialog");
    expect(edit.getAttribute("aria-expanded")).toBe("true");
    expect(
      document.querySelector('[data-testid="schedule-edit-dialog"]'),
    ).not.toBeNull();
    expect(document.body.textContent).toContain("编辑任务");

    const editDialog = document.querySelector<HTMLElement>(
      '[data-testid="schedule-edit-dialog"]',
    );
    const save = Array.from(
      editDialog?.querySelectorAll<HTMLButtonElement>("button") ?? [],
    ).find((button) => button.textContent?.trim() === "保存");
    if (!save) {
      throw new Error("missing save action");
    }

    await act(async () => {
      save.click();
      await Promise.resolve();
    });

    expect(saveTask).toHaveBeenCalledTimes(1);
    expect(saveTask).toHaveBeenCalledWith(
      expect.objectContaining({
        id: "task-1",
        title: "Ping",
        prompt: "ping",
        target_kind: "session",
        target_session_id: "session-1",
      }),
    );
    expect(
      document.querySelector('[data-testid="schedule-edit-dialog"]'),
    ).toBeNull();

    useScheduleTasksMock.mockReturnValue({
      controlTask,
      controllingAction: null,
      controllingId: "",
      createTask: vi.fn().mockResolvedValue({ ok: true }),
      creating: false,
      deletingId: "",
      error: null,
      loading: false,
      refresh: vi.fn(),
      removeTask: vi.fn(),
      saveTask,
      savingId: "",
      tasks: [task({ status: "paused" })],
    });
    act(() => {
      root?.render(<SchedulePanel sessionId="session-1" />);
    });

    const resume = Array.from(
      container.querySelectorAll<HTMLButtonElement>("button"),
    ).find((button) => button.textContent?.trim() === "恢复");
    if (!resume) {
      throw new Error("missing resume action");
    }
    await act(async () => {
      resume.click();
      await Promise.resolve();
    });
    expect(controlTask).toHaveBeenCalledWith("task-1", "resume");
  });

  it("未运行任务可重新启用且仍显示编辑入口", async () => {
    const controlTask = vi.fn().mockResolvedValue({ ok: true });
    useScheduleTasksMock.mockReturnValue({
      controlTask,
      controllingAction: null,
      controllingId: "",
      createTask: vi.fn().mockResolvedValue({ ok: true }),
      creating: false,
      deletingId: "",
      error: null,
      loading: false,
      refresh: vi.fn(),
      removeTask: vi.fn(),
      saveTask: vi.fn().mockResolvedValue({ ok: true }),
      savingId: "",
      tasks: [task({ status: "inactive" })],
    });

    act(() => {
      root?.render(<SchedulePanel sessionId="session-1" />);
    });

    const edit = Array.from(
      container.querySelectorAll<HTMLButtonElement>("button"),
    ).find((button) => button.textContent?.trim() === "编辑");
    const enable = Array.from(
      container.querySelectorAll<HTMLButtonElement>("button"),
    ).find((button) => button.textContent?.trim() === "启用");
    expect(edit).not.toBeUndefined();
    expect(edit?.getAttribute("aria-haspopup")).toBe("dialog");
    expect(enable).not.toBeUndefined();
    await act(async () => {
      enable?.click();
      await Promise.resolve();
    });
    expect(controlTask).toHaveBeenCalledWith("task-1", "resume");
  });

  it("删除等待窗口可撤销，撤销后不会调用删除接口", () => {
    vi.useFakeTimers();
    const removeTask = vi.fn().mockResolvedValue({ ok: true });
    useScheduleTasksMock.mockReturnValue({
      controlTask: vi.fn(),
      controllingAction: null,
      controllingId: "",
      createTask: vi.fn().mockResolvedValue({ ok: true }),
      creating: false,
      deletingId: "",
      error: null,
      loading: false,
      refresh: vi.fn(),
      removeTask,
      saveTask: vi.fn(),
      savingId: "",
      tasks: [task()],
    });
    act(() => {
      root?.render(<SchedulePanel sessionId="session-1" />);
    });

    const deleteButton = container.querySelector<HTMLButtonElement>(
      'button[aria-label="删除任务"]',
    );
    if (!deleteButton) throw new Error("missing delete button");
    act(() => deleteButton.click());
    const confirm = Array.from(
      container.querySelectorAll<HTMLButtonElement>("button"),
    ).find((button) => button.textContent?.trim() === "确认删除");
    if (!confirm) throw new Error("missing delete confirmation");
    act(() => confirm.click());

    expect(container.textContent).toContain("撤销删除");
    expect(container.textContent).not.toContain("Ping");
    const undo = Array.from(
      container.querySelectorAll<HTMLButtonElement>("button"),
    ).find((button) => button.textContent?.trim() === "撤销删除");
    if (!undo) throw new Error("missing undo action");
    act(() => undo.click());
    expect(container.textContent).toContain("Ping");
    act(() => vi.advanceTimersByTime(5_000));
    expect(removeTask).not.toHaveBeenCalled();
  });

  it("阻塞任务显示可操作的本地化错误，而不泄露内部英文原因", () => {
    useScheduleTasksMock.mockReturnValue({
      controlTask: vi.fn(),
      controllingAction: null,
      controllingId: "",
      createTask: vi.fn().mockResolvedValue({ ok: true }),
      creating: false,
      deletingId: "",
      error: null,
      loading: false,
      refresh: vi.fn(),
      removeTask: vi.fn(),
      saveTask: vi.fn().mockResolvedValue({ ok: true }),
      savingId: "",
      tasks: [
        task({
          status: "blocked",
          targetKind: "headless",
          sessionId: "",
          lastError: "no headless executor configured",
          lastErrorCode: "headless_executor_unavailable",
        }),
      ],
    });

    act(() => {
      root?.render(<SchedulePanel sessionId="session-1" />);
    });

    expect(container.textContent).toContain(
      "当前运行时未配置无人值守执行器。配置执行器后点击“重试”。",
    );
    expect(container.textContent).not.toContain("no headless executor configured");
  });

  it("投递死信进入需要处理分区，展示最近尝试并可重试无会话任务", async () => {
    const controlTask = vi.fn().mockResolvedValue({ ok: true });
    useScheduleTasksMock.mockReturnValue({
      controlTask,
      controllingAction: null,
      controllingId: "",
      createTask: vi.fn().mockResolvedValue({ ok: true }),
      creating: false,
      deletingId: "",
      error: null,
      loading: false,
      refresh: vi.fn(),
      removeTask: vi.fn(),
      saveTask: vi.fn().mockResolvedValue({ ok: true }),
      savingId: "",
      tasks: [
        task({
          scheduledAt: "2026-11-19T05:01:11.789Z",
          status: "blocked",
          targetKind: "headless",
          sessionId: "",
          createdBySessionId: undefined,
          lastError: "schedulehost: all delivery stages failed: peer(L2): unreachable",
          lastErrorCode: "delivery_failed",
          lastAttemptAt: "2026-11-19T05:06:13.000Z",
        }),
      ],
    });

    act(() => {
      root?.render(<SchedulePanel sessionId="session-1" />);
    });

    expect(container.textContent).toContain("需要处理");
    expect(container.textContent).toContain("原定运行");
    expect(container.textContent).toContain("最近尝试");
    expect(container.textContent).toContain("已停止自动重试");
    expect(container.textContent).not.toContain("all delivery stages failed");

    const retry = Array.from(
      container.querySelectorAll<HTMLButtonElement>("button"),
    ).find((button) => button.textContent?.trim() === "重试");
    if (!retry) throw new Error("missing retry action");
    await act(async () => {
      retry.click();
      await Promise.resolve();
    });
    expect(controlTask).toHaveBeenCalledWith("task-1", "resume");
  });

  it("目录加载错误显示本地化兜底而不展示后端原始错误", () => {
    useScheduleTasksMock.mockReturnValue({
      controlTask: vi.fn(),
      controllingAction: null,
      controllingId: "",
      createTask: vi.fn().mockResolvedValue({ ok: true }),
      creating: false,
      deletingId: "",
      error: "request_failed",
      loading: false,
      refresh: vi.fn(),
      removeTask: vi.fn(),
      saveTask: vi.fn().mockResolvedValue({ ok: true }),
      savingId: "",
      tasks: [],
    });

    act(() => {
      root?.render(<SchedulePanel sessionId="session-1" />);
    });

    expect(container.textContent).toContain("请求失败，请稍后重试。");
    expect(container.textContent).not.toContain("runtime request failed");
  });
});