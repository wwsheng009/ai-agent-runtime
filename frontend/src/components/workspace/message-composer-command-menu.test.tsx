// @vitest-environment jsdom

import { act } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  menu,
  press,
  renderComposer,
  typeText,
} from "./message-composer-menu-test-support";
import {
  COMMANDS,
  composerTestContainer,
  installComposerTestLifecycle,
} from "./message-composer-test-support";

describe("MessageComposer command menu", () => {
  installComposerTestLifecycle();

  let container: HTMLDivElement;

  beforeEach(() => {
    container = composerTestContainer();
  });

  it("drills into a popupSelect command's own options and dispatches the picked value", () => {
    const onCommand = vi.fn(
      (
        command: { name: string; kind: string },
        args: string,
        source: "pick" | "submit",
      ) => {
        void command;
        void args;
        void source;
        return true;
      },
    );
    const onDraftChange = vi.fn();
    renderComposer({
      commands: [
        {
          name: "model",
          kind: "popupSelect",
          options: [
            {
              value: "deepseek-chat",
              label: "deepseek-chat",
              description: "deepseek",
            },
            { value: "gpt-5", label: "gpt-5", description: "openai" },
          ],
        },
      ],
      onCommand,
      onDraftChange,
    });

    typeText("/model");
    // 真实打字每次按键都会带着 keydown/keyup，React 的选区追踪据此对齐；
    // 这里的 `typeText()` 用原生 setter 伪造输入，补一次无副作用按键等价对齐，
    // 否则 Enter 会额外触发一次 onSelect（选区陈旧）把候选层打回根层。
    press("Shift");
    // 第一层只有命令本身：Enter 先补全命令名并展开专属候选，而不是直接执行。
    press("Enter");
    expect(onCommand).not.toHaveBeenCalled();
    expect(
      container.querySelectorAll("[data-composer-command-option-value]"),
    ).toHaveLength(2);

    // 候选层：方向键虚拟高亮 + Enter 点选即派发（source=pick），命令行清空。
    press("ArrowDown");
    press("Enter");
    expect(onCommand).toHaveBeenCalledTimes(1);
    expect(onCommand.mock.calls[0][0]).toMatchObject({
      name: "model",
      kind: "popupSelect",
    });
    expect(onCommand.mock.calls[0][1]).toBe("gpt-5");
    expect(onCommand.mock.calls[0][2]).toBe("pick");
    expect(onDraftChange).toHaveBeenLastCalledWith("");
    expect(menu()).toBeNull();
  });

  it("blocks sending an unknown command line and surfaces the reason", () => {
    const onSubmit = vi.fn();
    renderComposer({ draft: "/nope", onSubmit });

    const submit = container.querySelector(
      'button[title*="Ctrl/Cmd"]',
    ) as HTMLButtonElement;
    act(() => submit.click());

    expect(onSubmit).not.toHaveBeenCalled();
    const notice = container.querySelector(
      "[data-composer-command-notice='unknown-command']",
    );
    expect(notice).not.toBeNull();
    expect(notice?.textContent).toContain("/nope");

    act(() =>
      (
        container.querySelector(
          "[data-composer-command-notice-dismiss]",
        ) as HTMLButtonElement
      ).click(),
    );
    expect(
      container.querySelector("[data-composer-command-notice]"),
    ).toBeNull();
  });

  it("dispatches a known command to the host instead of sending a prompt", () => {
    const onSubmit = vi.fn();
    const onCommand = vi.fn(
      (
        command: { name: string; kind: string },
        args: string,
        source: "pick" | "submit",
      ) => {
        void command;
        void args;
        void source;
        return true;
      },
    );
    renderComposer({
      commands: COMMANDS,
      draft: "/export --json",
      onSubmit,
      onCommand,
    });

    const submit = container.querySelector(
      'button[title*="Ctrl/Cmd"]',
    ) as HTMLButtonElement;
    act(() => submit.click());

    expect(onSubmit).not.toHaveBeenCalled();
    expect(onCommand).toHaveBeenCalledTimes(1);
    expect(onCommand.mock.calls[0][0]).toMatchObject({
      name: "export",
      kind: "action",
    });
    expect(onCommand.mock.calls[0][1]).toBe("--json");
    expect(onCommand.mock.calls[0][2]).toBe("submit");
  });

  it("reports a command without executor instead of silently sending it", () => {
    const onSubmit = vi.fn();
    renderComposer({ draft: "/export", onSubmit });

    const submit = container.querySelector(
      'button[title*="Ctrl/Cmd"]',
    ) as HTMLButtonElement;
    act(() => submit.click());

    expect(onSubmit).not.toHaveBeenCalled();
    expect(
      container.querySelector("[data-composer-command-notice='no-executor']"),
    ).not.toBeNull();
  });

  it("clears the command line once the host claims the command", () => {
    const onDraftChange = vi.fn();
    const onCommand = vi.fn(() => true);
    renderComposer({
      commands: COMMANDS,
      draft: "/export",
      onCommand,
      onDraftChange,
    });

    const submit = container.querySelector(
      'button[title*="Ctrl/Cmd"]',
    ) as HTMLButtonElement;
    act(() => submit.click());

    expect(onCommand).toHaveBeenCalledTimes(1);
    expect(onDraftChange).toHaveBeenLastCalledWith("");
  });

  it("keeps the command line as a draft when the host does not claim it", () => {
    const onDraftChange = vi.fn();
    renderComposer({
      commands: COMMANDS,
      draft: "/export",
      onCommand: () => false,
      onDraftChange,
    });

    const submit = container.querySelector(
      'button[title*="Ctrl/Cmd"]',
    ) as HTMLButtonElement;
    act(() => submit.click());

    expect(onDraftChange).not.toHaveBeenCalled();
    expect(
      container.querySelector("[data-composer-command-notice='no-executor']"),
    ).not.toBeNull();
  });

  it("keeps plain prose flowing to the normal submit path", () => {
    const onSubmit = vi.fn();
    renderComposer({ draft: "hello world", onSubmit });

    const submit = container.querySelector(
      'button[title*="Ctrl/Cmd"]',
    ) as HTMLButtonElement;
    act(() => submit.click());

    expect(onSubmit).toHaveBeenCalledTimes(1);
    expect(
      container.querySelector("[data-composer-command-notice]"),
    ).toBeNull();
  });
});