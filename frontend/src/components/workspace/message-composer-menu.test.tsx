// @vitest-environment jsdom

import { act } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  menu,
  press,
  renderComposer,
  textarea,
  typeText,
} from "./message-composer-menu-test-support";
import {
  composerTestContainer,
  installComposerTestLifecycle,
} from "./message-composer-test-support";

describe("MessageComposer trigger menu", () => {
  installComposerTestLifecycle();

  let container: HTMLDivElement;

  beforeEach(() => {
    container = composerTestContainer();
  });

  it("opens category launchers from the + button and drills into each leaf group", () => {
    renderComposer();

    const trigger = container.querySelector(
      "button[data-composer-menu-trigger]",
    ) as HTMLButtonElement;
    expect(trigger).not.toBeNull();
    expect(menu()).toBeNull();

    act(() => trigger.click());
    expect(menu()).not.toBeNull();
    expect(
      container.querySelector("[data-composer-menu-item]")?.className,
    ).toContain("text-base");
    expect(trigger.parentElement).toBe(
      container.querySelector("[data-composer-menu-anchor]"),
    );
    expect(trigger.parentElement?.querySelector("[data-composer-menu]")).toBe(
      menu(),
    );
    expect(menu()?.className).toContain("bottom-full");
    expect(menu()?.className).not.toContain("mb-2");
    expect(
      container.querySelector("[data-composer-menu-level='root']"),
    ).not.toBeNull();
    expect(
      container.querySelector("[data-composer-menu-launcher='actions']"),
    ).not.toBeNull();
    expect(
      container.querySelector("[data-composer-menu-launcher='references']"),
    ).not.toBeNull();
    expect(
      container.querySelector("[data-composer-menu-launcher='commands']"),
    ).not.toBeNull();
    expect(container.querySelector("[data-composer-attach]")).toBeNull();
    expect(
      container.querySelector("[data-composer-command-option='export']"),
    ).toBeNull();
    expect(
      container.querySelector("[data-composer-reference-option='']"),
    ).toBeNull();

    act(() =>
      (
        container.querySelector(
          "[data-composer-menu-launcher='actions']",
        ) as HTMLElement
      ).click(),
    );
    expect(
      container.querySelector("[data-composer-menu-level='group']"),
    ).not.toBeNull();
    expect(container.querySelector("[data-composer-attach]")).not.toBeNull();
    expect(
      container.querySelector("[data-composer-menu-back]"),
    ).not.toBeNull();

    act(() =>
      (
        container.querySelector("[data-composer-menu-back]") as HTMLElement
      ).click(),
    );
    expect(
      container.querySelector("[data-composer-menu-level='root']"),
    ).not.toBeNull();

    act(() =>
      (
        container.querySelector(
          "[data-composer-menu-launcher='references']",
        ) as HTMLElement
      ).click(),
    );
    expect(
      container.querySelector("[data-composer-reference-option='src/app.tsx']"),
    ).not.toBeNull();
    expect(
      container.querySelector("[data-composer-command-option='export']"),
    ).toBeNull();

    act(() =>
      (
        container.querySelector("[data-composer-menu-back]") as HTMLElement
      ).click(),
    );
    act(() =>
      (
        container.querySelector(
          "[data-composer-menu-launcher='commands']",
        ) as HTMLElement
      ).click(),
    );
    expect(
      container.querySelector("[data-composer-command-option='export']"),
    ).not.toBeNull();

    press("Escape");
    expect(
      container.querySelector("[data-composer-menu-level='root']"),
    ).not.toBeNull();
    press("Escape");
    expect(menu()).toBeNull();
  });

  it("selects a workspace from + without changing the draft and closes the menu", () => {
    const onDraftChange = vi.fn();
    const onWorkspaceSelect = vi.fn();
    renderComposer({
      draft: "keep this draft",
      isNewThread: true,
      onDraftChange,
      onWorkspaceSelect,
      workspaceSelection: {
        label: "Workspace",
        options: [
          { id: "repo", label: "Demo project", path: "E:/projects/demo" },
        ],
        selectedId: "repo",
        clearLabel: "No workspace",
      },
    });

    const trigger = container.querySelector(
      "button[data-composer-menu-trigger]",
    ) as HTMLButtonElement;
    act(() => trigger.click());

    act(() =>
      (
        container.querySelector(
          "[data-composer-menu-launcher='workspace']",
        ) as HTMLElement
      ).click(),
    );
    const selectedOption = container.querySelector(
      "[data-composer-workspace-option='repo']",
    ) as HTMLElement;
    expect(selectedOption.getAttribute("aria-selected")).toBe("true");
    expect(
      container.querySelector(
        "[data-composer-workspace-option='repo'][data-composer-workspace-selected='true']",
      ),
    ).not.toBeNull();

    act(() => selectedOption.click());

    expect(onWorkspaceSelect).toHaveBeenCalledWith("repo");
    expect(onDraftChange).not.toHaveBeenCalled();
    expect(menu()).toBeNull();
  });

  it("supports keyboard drill-down and returning from the second level", () => {
    renderComposer();
    const trigger = container.querySelector(
      "button[data-composer-menu-trigger]",
    ) as HTMLButtonElement;
    act(() => trigger.click());

    press("ArrowRight");
    expect(
      container.querySelector("[data-composer-menu-level='group']"),
    ).not.toBeNull();

    press("ArrowLeft");
    expect(
      container.querySelector("[data-composer-menu-level='root']"),
    ).not.toBeNull();

    press("Tab");
    expect(
      container.querySelector("[data-composer-menu-level='group']"),
    ).not.toBeNull();

    press("Escape");
    expect(
      container.querySelector("[data-composer-menu-level='root']"),
    ).not.toBeNull();
    press("Escape");
    expect(menu()).toBeNull();
  });

  it("drills into a reference group and completes by keyboard", () => {
    const onDraftChange = vi.fn();
    renderComposer({ onDraftChange });

    const element = typeText("@");
    expect(menu()).not.toBeNull();
    expect(element.getAttribute("aria-expanded")).toBe("true");
    expect(element.getAttribute("aria-activedescendant")).toBeTruthy();

    // 第一层是「文件」分组 launcher：Enter 下钻而非直接补全。
    press("Enter");
    expect(
      container.querySelector("[data-composer-reference-option='src/app.tsx']"),
    ).not.toBeNull();

    press("ArrowDown");
    press("Enter");
    expect(onDraftChange).toHaveBeenLastCalledWith("@reports/report.md");
    expect(menu()).toBeNull();
  });

  it("filters reference candidates by the typed query", () => {
    renderComposer();
    typeText("@app");

    const options = container.querySelectorAll(
      "[data-composer-reference-option]",
    );
    expect(
      Array.from(options).map((option) =>
        option.getAttribute("data-composer-reference-option"),
      ),
    ).toEqual(["src/app.tsx"]);
  });

  it("reports menu state changes (open/mode/query) to the host", () => {
    const onMenuStateChange = vi.fn();
    renderComposer({ onMenuStateChange });

    // 挂载即回调关闭态：宿主据此判定「不发请求」。
    expect(onMenuStateChange).toHaveBeenLastCalledWith({
      open: false,
      mode: "references",
      query: "",
    });

    typeText("@app");
    expect(onMenuStateChange).toHaveBeenLastCalledWith({
      open: true,
      mode: "references",
      query: "app",
    });

    // `+` 按钮进的是 all 模式：宿主不应把它当成 `@` 搜索。
    press("Escape");
    const trigger = container.querySelector(
      "button[data-composer-menu-trigger]",
    ) as HTMLButtonElement;
    act(() => trigger.click());
    expect(onMenuStateChange).toHaveBeenLastCalledWith({
      open: true,
      mode: "all",
      query: "",
    });

    press("Escape");
    // 关闭态下 manual 复位：mode 回落为 references（宿主只看 open，不据此发请求）。
    expect(onMenuStateChange).toHaveBeenLastCalledWith({
      open: false,
      mode: "references",
      query: "",
    });
  });

  it("blocks Enter when the @ menu has no candidates (no fallback to raw token)", () => {
    renderComposer();
    typeText("@zzz");

    const event = new KeyboardEvent("keydown", {
      key: "Enter",
      bubbles: true,
      cancelable: true,
    });
    act(() => {
      textarea().dispatchEvent(event);
    });

    expect(event.defaultPrevented).toBe(true);
    expect(container.querySelector("[data-composer-menu]")).not.toBeNull();
    expect(
      container.querySelector("[data-composer-menu-empty]"),
    ).not.toBeNull();
  });

  it("drills into a loading workspace group and renders the status note", () => {
    renderComposer({
      referenceGroups: [
        {
          id: "workspace-files",
          label: "工作区文件",
          items: [],
          status: "loading",
          statusText: "正在读取工作区文件…",
        },
      ],
    });
    typeText("@");
    press("Enter");

    const note = container.querySelector(
      "[data-composer-menu-group-note='workspace-files']",
    );
    expect(note?.textContent).toBe("正在读取工作区文件…");
    // 有分组状态行时不再落「无匹配候选」根空态。
    expect(container.querySelector("[data-composer-menu-empty]")).toBeNull();
  });

  it("renders the truncated footer for workspace groups", () => {
    renderComposer({
      referenceGroups: [
        {
          id: "workspace-files",
          label: "工作区文件",
          status: "ready",
          serverFiltered: true,
          truncated: true,
          truncatedText: "结果已截断，继续输入以缩小范围",
          items: [{ id: "p1", label: "index.ts", insertText: "index.ts" }],
        },
      ],
    });
    typeText("@");
    press("Enter");

    const footer = container.querySelector(
      "[data-composer-menu-group-truncated='workspace-files']",
    );
    expect(footer?.textContent).toBe("结果已截断，继续输入以缩小范围");
  });
});