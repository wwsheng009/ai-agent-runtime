// @vitest-environment jsdom

import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import {
  afterEach,
  beforeEach,
  describe,
  expect,
  it,
  vi,
  type Mock,
} from "vitest";

import { emptyCreateForm } from "./form-state";
import type { CreateFormState } from "./form-state";
import {
  ScheduleFormDialog,
  type ScheduleFormDialogMode,
} from "./form-dialog";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function completeForm(
  overrides: Partial<CreateFormState> = {},
): CreateFormState {
  return {
    ...emptyCreateForm("session-1"),
    title: "每日汇总",
    prompt: "汇总今天的任务进展",
    ...overrides,
  };
}

type TestHarnessProps = {
  busy?: boolean;
  initialForm: CreateFormState;
  error?: string | null;
  mode?: ScheduleFormDialogMode;
  onClearError: () => void;
  onClose: () => void;
  onSubmit: () => void;
};

function TestHarness({
  busy = false,
  initialForm,
  error = null,
  mode = "create",
  onClearError,
  onClose,
  onSubmit,
}: TestHarnessProps) {
  const [form, setForm] = useState<CreateFormState | null>(initialForm);
  const [formError, setFormError] = useState<string | null>(error);
  if (!form) {
    return null;
  }
  return (
    <ScheduleFormDialog
      busy={busy}
      createForm={form}
      error={formError}
      mode={mode}
      onClearError={() => {
        onClearError();
        setFormError(null);
      }}
      onClose={onClose}
      onSubmit={onSubmit}
      setCreateForm={setForm}
    />
  );
}

describe("ScheduleFormDialog", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  let onClearError: Mock<() => void>;
  let onClose: Mock<() => void>;
  let onSubmit: Mock<() => void>;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    onClearError = vi.fn<() => void>();
    onClose = vi.fn<() => void>();
    onSubmit = vi.fn<() => void>();
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
      root = null;
    }
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderDialog(props: Partial<TestHarnessProps> = {}) {
    act(() => {
      root?.render(
        <TestHarness
          busy={false}
          error={null}
          initialForm={completeForm()}
          onClearError={onClearError}
          onClose={onClose}
          onSubmit={onSubmit}
          {...props}
        />,
      );
    });
  }

  function click(element: Element) {
    act(() => {
      (element as HTMLElement).click();
    });
  }

  it("在 portal 中渲染带章节分组的模态对话框", () => {
    renderDialog();

    const dialog = document.querySelector<HTMLElement>('[role="dialog"]');
    expect(dialog).not.toBeNull();
    expect(dialog?.getAttribute("aria-modal")).toBe("true");
    expect(dialog?.parentElement?.className).toContain("z-[120]");
    expect(document.body.textContent).toContain("任务内容");
    expect(document.body.textContent).toContain("执行上下文");
    expect(document.body.textContent).toContain("运行规则");
    expect(document.activeElement).toBe(
      document.querySelector('[data-testid="schedule-create-title"]'),
    );
  });

  it("切换执行上下文后显示工作区字段，并清除旧错误", () => {
    renderDialog({ error: "上一次提交失败" });
    expect(
      document.querySelector('input[placeholder="E:/projects/demo"]'),
    ).toBeNull();

    const workspaceRadio = document.querySelector<HTMLInputElement>(
      'input[name="schedule-target-kind"][value="workspace"]',
    );
    if (!workspaceRadio) {
      throw new Error("missing workspace target radio");
    }
    click(workspaceRadio);

    expect(onClearError).toHaveBeenCalled();
    expect(document.querySelector('[role="alert"]')).toBeNull();
    expect(
      document.querySelector('input[placeholder="E:/projects/demo"]'),
    ).not.toBeNull();
    expect(
      document.querySelector('input[placeholder="会话列表中复制的会话 ID"]'),
    ).toBeNull();
  });

  it("表单提交走原生 submit 事件并调用 onSubmit", () => {
    renderDialog();
    const form = document.querySelector<HTMLFormElement>("form");
    if (!form) {
      throw new Error("missing create form");
    }

    act(() => {
      form.dispatchEvent(
        new Event("submit", { bubbles: true, cancelable: true }),
      );
    });

    expect(onSubmit).toHaveBeenCalledTimes(1);
  });

  it("创建中禁用关闭与提交按钮，并显示进行中文案", () => {
    renderDialog({ busy: true });

    const submit = document.querySelector<HTMLButtonElement>(
      'button[type="submit"]',
    );
    const close = document.querySelector<HTMLButtonElement>(
      'button[aria-label="关闭新建任务对话框"]',
    );

    expect(submit?.disabled).toBe(true);
    expect(close?.disabled).toBe(true);
    expect(submit?.textContent).toContain("创建中…");
  });

  it("错误以 alert 展示并接收初始焦点", () => {
    renderDialog({ error: "创建失败：invalid_selector" });

    const alert = document.querySelector<HTMLElement>('[role="alert"]');
    expect(alert?.textContent).toContain("创建失败：invalid_selector");
    expect(document.activeElement).toBe(alert);
  });

  it("按 Escape 关闭对话框", () => {
    renderDialog();

    act(() => {
      window.dispatchEvent(
        new KeyboardEvent("keydown", {
          bubbles: true,
          cancelable: true,
          key: "Escape",
        }),
      );
    });

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("编辑模式使用编辑标题、关闭标签与保存中状态", () => {
    renderDialog({ busy: true, mode: "edit" });

    const dialog = document.querySelector<HTMLElement>(
      '[data-testid="schedule-edit-dialog"]',
    );
    expect(dialog?.dataset.mode).toBe("edit");
    expect(document.body.textContent).toContain("编辑任务");
    expect(
      document.querySelector('button[aria-label="关闭编辑任务对话框"]'),
    ).not.toBeNull();
    expect(
      document.querySelector('button[type="submit"]')?.textContent,
    ).toContain("保存中…");
  });
});