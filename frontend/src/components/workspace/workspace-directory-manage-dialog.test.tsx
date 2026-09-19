// @vitest-environment jsdom

// Phase 2（合并方案 §3.5-D）：平铺模式「管理目录」弹层单测。
// 覆盖：空态 / 会话数按 id 映射 / 目录内新建会话请求形状与忙碌态 /
// 行内重命名（提交与空值取消）/ 移除请求 / 缺目录告警 / 添加入口 / Esc 关闭 / open=false 不渲染。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { type RuntimeWorkspaceDirectory } from "@/lib/runtime-api";

import {
  WorkspaceDirectoryManageDialog,
  type WorkspaceDirectoryManageDialogProps,
} from "./workspace-directory-manage-dialog";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

// 文案于 2c05eaac 精简（"在该目录下新建会话"→"新建会话" 等），此处与词典保持同步。
const NEW_CHAT_LABEL = "新建会话";
const RENAME_LABEL = "重命名";
const REMOVE_LABEL = "移除";

function flush() {
  return Promise.resolve().then(() => Promise.resolve());
}

function makeDirectory(
  overrides: Partial<RuntimeWorkspaceDirectory> = {},
): RuntimeWorkspaceDirectory {
  return { id: "dir-1", path: "E:\\projects\\demo", ...overrides };
}

function setInputValue(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    "value",
  )?.set;
  setter?.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

describe("WorkspaceDirectoryManageDialog", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  const onClose = vi.fn();
  const onRequestAdd = vi.fn();
  const onCreateSession = vi.fn();
  const onRenameDirectory = vi.fn();
  const onRequestRemove = vi.fn();

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    onClose.mockReset();
    onRequestAdd.mockReset();
    onCreateSession.mockReset();
    onRenameDirectory.mockReset();
    onRequestRemove.mockReset();
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
      root = null;
    }
    container.remove();
  });

  function renderDialog(
    props: Partial<WorkspaceDirectoryManageDialogProps> = {},
  ) {
    act(() => {
      root?.render(
        <WorkspaceDirectoryManageDialog
          directories={[]}
          sessionCounts={{}}
          onClose={onClose}
          onCreateSession={onCreateSession}
          onRenameDirectory={onRenameDirectory}
          onRequestAdd={onRequestAdd}
          onRequestRemove={onRequestRemove}
          open
          {...props}
        />,
      );
    });
  }

  function rows(): HTMLElement[] {
    return Array.from(
      document.querySelectorAll<HTMLElement>(
        '[data-testid="directory-manage-row"]',
      ),
    );
  }

  function actionButton(row: HTMLElement, label: string): HTMLButtonElement {
    const button = row.querySelector<HTMLButtonElement>(
      `button[aria-label="${label}"]`,
    );
    if (!button) {
      throw new Error(`missing action button: ${label}`);
    }
    return button;
  }

  it("open=false 时不渲染弹层", () => {
    renderDialog({ open: false, directories: [makeDirectory()] });

    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(rows()).toHaveLength(0);
  });

  it("空列表显示 manageEmpty 提示", () => {
    renderDialog();

    expect(
      document.querySelector('[data-testid="directory-manage-empty"]')
        ?.textContent,
    ).toBe("还没有注册的工作目录。");
    expect(rows()).toHaveLength(0);
  });

  it("按目录 id 显示会话数（缺省为 0）", () => {
    renderDialog({
      directories: [
        makeDirectory({ id: "dir-a", path: "E:\\projects\\alpha" }),
        makeDirectory({ id: "dir-b", path: "E:\\projects\\beta" }),
      ],
      sessionCounts: { "dir-b": 5 },
    });

    const rendered = rows();
    expect(rendered).toHaveLength(2);
    expect(
      rendered[0]?.querySelector('[data-testid="directory-manage-count"]')
        ?.textContent,
    ).toBe("0");
    expect(
      rendered[1]?.querySelector('[data-testid="directory-manage-count"]')
        ?.textContent,
    ).toBe("5");
  });

  it("别名取 name，缺省回退路径末段，并展示路径与告警", () => {
    renderDialog({
      directories: [
        makeDirectory({ id: "dir-1", path: "E:\\projects\\alpha" }),
        makeDirectory({
          id: "dir-2",
          path: "/srv/data/beta",
          name: "  别名  ",
          exists: false,
        }),
      ],
    });

    const rendered = rows();
    expect(rendered[0]?.textContent).toContain("alpha");
    expect(rendered[1]?.textContent).toContain("别名");
    expect(rendered[1]?.textContent).toContain("/srv/data/beta");
    expect(
      rendered[0]?.querySelector('[data-testid="directory-manage-warning"]'),
    ).toBeNull();
    expect(
      rendered[1]?.querySelector('[data-testid="directory-manage-warning"]')
        ?.getAttribute("title"),
    ).toBe("目录在运行时主机上不存在");
  });

  it("点击「在该目录下新建会话」发出 {path, directoryId, label}", async () => {
    renderDialog({
      directories: [
        makeDirectory({ id: "dir-9", path: "E:\\projects\\gamma" }),
      ],
      sessionCounts: { "dir-9": 3 },
    });

    const button = actionButton(rows()[0]!, NEW_CHAT_LABEL);
    await act(async () => {
      button.click();
      await flush();
    });

    expect(onCreateSession).toHaveBeenCalledTimes(1);
    expect(onCreateSession).toHaveBeenCalledWith({
      path: "E:\\projects\\gamma",
      directoryId: "dir-9",
      label: "gamma",
    });
  });

  it("新建会话进行中禁用按钮并显示加载图标", async () => {
    let resolveCreate: (() => void) | null = null;
    onCreateSession.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          resolveCreate = resolve;
        }),
    );
    renderDialog({ directories: [makeDirectory()] });

    const button = actionButton(rows()[0]!, NEW_CHAT_LABEL);
    act(() => {
      button.click();
    });

    expect(actionButton(rows()[0]!, NEW_CHAT_LABEL).disabled).toBe(true);
    expect(rows()[0]?.querySelector(".animate-spin")).not.toBeNull();

    await act(async () => {
      resolveCreate?.();
      await flush();
    });

    expect(actionButton(rows()[0]!, NEW_CHAT_LABEL).disabled).toBe(false);
  });

  it("行内重命名回车提交 (id, name)", async () => {
    renderDialog({ directories: [makeDirectory({ name: "旧别名" })] });

    act(() => {
      actionButton(rows()[0]!, RENAME_LABEL).click();
    });

    const input = document.querySelector<HTMLInputElement>(
      `input[aria-label="${RENAME_LABEL}"]`,
    );
    expect(input?.value).toBe("旧别名");
    expect(input?.placeholder).toBe("新的会话标题");

    await act(async () => {
      setInputValue(input!, "  新别名  ");
      input!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
      await flush();
    });

    expect(onRenameDirectory).toHaveBeenCalledTimes(1);
    expect(onRenameDirectory).toHaveBeenCalledWith("dir-1", "新别名");
    expect(
      document.querySelector(`input[aria-label="${RENAME_LABEL}"]`),
    ).toBeNull();
  });

  it("重命名输入空串不提交并收起编辑器", async () => {
    renderDialog({ directories: [makeDirectory()] });

    act(() => {
      actionButton(rows()[0]!, RENAME_LABEL).click();
    });

    const input = document.querySelector<HTMLInputElement>(
      `input[aria-label="${RENAME_LABEL}"]`,
    );
    await act(async () => {
      setInputValue(input!, "   ");
      input!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
      await flush();
    });

    expect(onRenameDirectory).not.toHaveBeenCalled();
    expect(
      document.querySelector(`input[aria-label="${RENAME_LABEL}"]`),
    ).toBeNull();
  });

  it("点击移除按钮把目录对象交回接线层", () => {
    const directory = makeDirectory({ id: "dir-7" });
    renderDialog({ directories: [directory] });

    act(() => {
      actionButton(rows()[0]!, REMOVE_LABEL).click();
    });

    expect(onRequestRemove).toHaveBeenCalledTimes(1);
    expect(onRequestRemove).toHaveBeenCalledWith(directory);
  });

  it("底部「添加目录」按钮请求接线层打开添加弹窗", () => {
    renderDialog();

    const addButton = Array.from(
      document.querySelectorAll<HTMLButtonElement>("button"),
    ).find((button) => button.textContent?.includes("添加目录"));
    expect(addButton).toBeDefined();

    act(() => {
      addButton!.click();
    });

    expect(onRequestAdd).toHaveBeenCalledTimes(1);
  });

  it("Esc 关闭弹层", () => {
    renderDialog({ directories: [makeDirectory()] });

    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    });

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  // 方案 §12-A：会话派生的目录在管理弹层里可见并可一键注册。
  const UNREGISTERED = [
    { path: "E:/work/gamma", label: "gamma", sessionCount: 2 },
  ];

  function unregisteredRows(): HTMLElement[] {
    return Array.from(
      document.querySelectorAll<HTMLElement>(
        '[data-testid="directory-manage-unregistered-row"]',
      ),
    );
  }

  /** §15 之后未注册行有两个按钮（组合动作 + 纯注册），按 testid 精确定位。 */
  function registerButton(row: HTMLElement): HTMLButtonElement {
    const button = row.querySelector<HTMLButtonElement>(
      '[data-testid="directory-manage-register"]',
    );
    if (!button) {
      throw new Error("missing register button");
    }
    return button;
  }

  function registerAndCreateButton(row: HTMLElement): HTMLButtonElement {
    const button = row.querySelector<HTMLButtonElement>(
      '[data-testid="directory-manage-register-and-create"]',
    );
    if (!button) {
      throw new Error("missing register-and-create button");
    }
    return button;
  }

  it("列出来自会话的未注册目录，注册表为空时不出空态", () => {
    renderDialog({
      unregisteredDirectories: UNREGISTERED,
      onRegisterDirectory: vi.fn(),
    });

    expect(
      document.querySelector('[data-testid="directory-manage-empty"]'),
    ).toBeNull();
    expect(unregisteredRows()).toHaveLength(1);
    expect(unregisteredRows()[0]?.dataset.directoryPath).toBe("E:/work/gamma");
    expect(
      unregisteredRows()[0]?.querySelector(
        '[data-testid="directory-manage-unregistered-count"]',
      )?.textContent,
    ).toBe("2");
    expect(document.body.textContent).toContain("未注册（来自会话）");
  });

  it("不传 unregisteredDirectories 时不渲染未注册分区", () => {
    renderDialog({ directories: [makeDirectory()] });

    expect(
      document.querySelector('[data-testid="directory-manage-unregistered"]'),
    ).toBeNull();
  });

  it("点击「注册」把路径交回接线层并自持忙碌态", async () => {
    let resolveRegister: (() => void) | null = null;
    const onRegisterDirectory = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          resolveRegister = resolve;
        }),
    );
    renderDialog({
      unregisteredDirectories: UNREGISTERED,
      onRegisterDirectory,
    });

    act(() => {
      registerButton(unregisteredRows()[0]!).click();
    });

    expect(onRegisterDirectory).toHaveBeenCalledWith("E:/work/gamma");
    expect(registerButton(unregisteredRows()[0]!).disabled).toBe(true);
    expect(
      unregisteredRows()[0]?.querySelector(".animate-spin"),
    ).not.toBeNull();

    await act(async () => {
      resolveRegister?.();
      await flush();
    });

    expect(registerButton(unregisteredRows()[0]!).disabled).toBe(false);
  });

  it("注册失败就地提示且不收起弹层", async () => {
    const onRegisterDirectory = vi
      .fn()
      .mockRejectedValue(new Error("目录不存在"));
    renderDialog({
      unregisteredDirectories: UNREGISTERED,
      onRegisterDirectory,
    });

    await act(async () => {
      registerButton(unregisteredRows()[0]!).click();
      await flush();
    });

    expect(
      document.querySelector('[data-testid="directory-manage-register-error"]')
        ?.textContent,
    ).toBe("目录不存在");
    expect(unregisteredRows()).toHaveLength(1);
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();
  });

  // 方案 §15：「用即注册」——注册并新建会话合并为一个动作（少一跳）。
  it("「注册并新建会话」把路径与标签交回接线层，成功后收起弹层", async () => {
    const onRegisterAndCreateSession = vi.fn().mockResolvedValue(undefined);
    renderDialog({
      unregisteredDirectories: UNREGISTERED,
      onRegisterAndCreateSession,
    });

    await act(async () => {
      registerAndCreateButton(unregisteredRows()[0]!).click();
      await flush();
    });

    // 标签沿用派生组名（新会话标题口径与「在该目录下新建会话」一致）。
    expect(onRegisterAndCreateSession).toHaveBeenCalledWith(
      "E:/work/gamma",
      "gamma",
    );
    // 成功即收起：新会话会被选中并跳转，弹层留着会挡住会话。
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("「注册并新建会话」自持忙碌态，并禁用同行的纯注册按钮", async () => {
    let resolveCombined: (() => void) | null = null;
    const onRegisterAndCreateSession = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          resolveCombined = resolve;
        }),
    );
    renderDialog({
      unregisteredDirectories: UNREGISTERED,
      onRegisterAndCreateSession,
    });

    act(() => {
      registerAndCreateButton(unregisteredRows()[0]!).click();
    });

    expect(registerAndCreateButton(unregisteredRows()[0]!).disabled).toBe(true);
    expect(registerButton(unregisteredRows()[0]!).disabled).toBe(true);
    expect(unregisteredRows()[0]?.querySelector(".animate-spin")).not.toBeNull();

    await act(async () => {
      resolveCombined?.();
      await flush();
    });

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("「注册并新建会话」失败就地提示、不收起弹层", async () => {
    const onRegisterAndCreateSession = vi
      .fn()
      .mockRejectedValue(new Error("目录不存在"));
    renderDialog({
      unregisteredDirectories: UNREGISTERED,
      onRegisterAndCreateSession,
    });

    await act(async () => {
      registerAndCreateButton(unregisteredRows()[0]!).click();
      await flush();
    });

    expect(
      document.querySelector('[data-testid="directory-manage-register-error"]')
        ?.textContent,
    ).toBe("目录不存在");
    expect(onClose).not.toHaveBeenCalled();
    expect(unregisteredRows()).toHaveLength(1);
  });

  it("不传 onRegisterAndCreateSession 时组合动作按钮禁用（纯注册仍可用）", () => {
    renderDialog({
      unregisteredDirectories: UNREGISTERED,
      onRegisterDirectory: vi.fn(),
    });

    expect(registerAndCreateButton(unregisteredRows()[0]!).disabled).toBe(true);
    expect(registerButton(unregisteredRows()[0]!).disabled).toBe(false);
  });
});
