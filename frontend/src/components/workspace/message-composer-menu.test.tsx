// @vitest-environment jsdom

import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { MessageComposer } from "./message-composer";
import { type ComposerAttachmentsController } from "@/hooks/workspace/composer/use-composer-attachments";
import { type ComposerCommandDefinition } from "@/lib/composer-commands";
import { type ComposerReferenceGroup } from "@/lib/composer-menu";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const COMMANDS: readonly ComposerCommandDefinition[] = [
  { name: "export", kind: "action" },
  { name: "model", kind: "popupSelect" },
];

const REFERENCE_GROUPS: readonly ComposerReferenceGroup[] = [
  {
    id: "artifacts",
    label: "文件",
    items: [
      { id: "a1", label: "app.tsx", insertText: "src/app.tsx" },
      { id: "a2", label: "report.md", insertText: "reports/report.md" },
    ],
  },
];

function createAttachmentsStub(
  overrides: Partial<ComposerAttachmentsController> = {},
): ComposerAttachmentsController {
  return {
    attachments: [],
    isDragOver: false,
    rejectedCount: 0,
    addFiles: vi.fn(),
    removeAttachment: vi.fn(),
    clearAttachments: vi.fn(),
    acknowledgeRejections: vi.fn(),
    ...overrides,
  };
}

describe("MessageComposer trigger menu", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderComposer(
    overrides: Partial<React.ComponentProps<typeof MessageComposer>> = {},
  ) {
    const props: React.ComponentProps<typeof MessageComposer> = {
      attachments: createAttachmentsStub(),
      commands: COMMANDS,
      density: "comfortable",
      draft: "",
      hasSession: true,
      isResponding: false,
      modelOptions: ["model-a"],
      onDraftChange: vi.fn(),
      onModelChange: vi.fn(),
      onProviderChange: vi.fn(),
      onReasoningEffortChange: vi.fn(),
      onStop: vi.fn(),
      onSubmit: vi.fn(),
      providerOptions: ["provider-a"],
      reasoningEffortDefault: "",
      reasoningEffortError: null,
      reasoningEffortOptions: [],
      referenceGroups: REFERENCE_GROUPS,
      runtimeModelsError: null,
      runtimeModelsLoading: false,
      selectedArtifactCount: 0,
      selectedModel: "model-a",
      selectedProvider: "provider-a",
      selectedReasoningEffort: "",
      ...overrides,
    };

    act(() => root?.render(<ComposerHarness props={props} />));
    return props;
  }

  // 受控宿主：草稿更新回灌成 props，模拟真实页面（否则 hook 的 value 与 DOM 脱节）。
  function ComposerHarness({ props }: { props: React.ComponentProps<typeof MessageComposer> }) {
    const [draft, setDraft] = useState(props.draft);
    return (
      <MessageComposer
        {...props}
        draft={draft}
        onDraftChange={(next) => {
          props.onDraftChange(next);
          setDraft(next);
        }}
      />
    );
  }

  function textarea(): HTMLTextAreaElement {
    return container.querySelector("textarea") as HTMLTextAreaElement;
  }

  function menu(): HTMLElement | null {
    return container.querySelector("[data-composer-menu]");
  }

  function type(value: string) {
    const element = textarea();
    const setter = Object.getOwnPropertyDescriptor(
      HTMLTextAreaElement.prototype,
      "value",
    )?.set;
    act(() => {
      setter?.call(element, value);
      element.setSelectionRange(value.length, value.length);
      element.dispatchEvent(new Event("input", { bubbles: true }));
    });
    return element;
  }

  function press(key: string) {
    act(() => {
      textarea().dispatchEvent(
        new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }),
      );
    });
  }

  it("opens the same menu from the + button for attach and references", () => {
    renderComposer();

    const trigger = container.querySelector(
      "button[data-composer-menu-trigger]",
    ) as HTMLButtonElement;
    expect(trigger).not.toBeNull();
    expect(menu()).toBeNull();

    act(() => trigger.click());
    expect(menu()).not.toBeNull();
    expect(container.querySelector("[data-composer-attach]")).not.toBeNull();
    expect(container.querySelector("[data-composer-command-option='export']")).not.toBeNull();
    expect(container.querySelector("[data-composer-reference-option='']")).not.toBeNull();

    press("Escape");
    expect(menu()).toBeNull();
  });

  it("drills into a reference group and completes by keyboard", () => {
    const onDraftChange = vi.fn();
    renderComposer({ onDraftChange });

    const element = type("@");
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
    type("@app");

    const options = container.querySelectorAll("[data-composer-reference-option]");
    expect(Array.from(options).map((option) => option.getAttribute("data-composer-reference-option"))).toEqual([
      "src/app.tsx",
    ]);
  });

  it("drills into a popupSelect command's own options and dispatches the picked value", () => {
    const onCommand = vi.fn(
      (command: { name: string; kind: string }, args: string, source: "pick" | "submit") => {
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
            { value: "deepseek-chat", label: "deepseek-chat", description: "deepseek" },
            { value: "gpt-5", label: "gpt-5", description: "openai" },
          ],
        },
      ],
      onCommand,
      onDraftChange,
    });

    type("/model");
    // 真实打字每次按键都会带着 keydown/keyup，React 的选区追踪据此对齐；
    // 这里的 `type()` 用原生 setter 伪造输入，补一次无副作用按键等价对齐，
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
    expect(container.querySelector("[data-composer-command-notice]")).toBeNull();
  });

  it("dispatches a known command to the host instead of sending a prompt", () => {
    const onSubmit = vi.fn();
    const onCommand = vi.fn(
      (command: { name: string; kind: string }, args: string, source: "pick" | "submit") => {
        void command;
        void args;
        void source;
        return true;
      },
    );
    renderComposer({ commands: COMMANDS, draft: "/export --json", onSubmit, onCommand });

    const submit = container.querySelector(
      'button[title*="Ctrl/Cmd"]',
    ) as HTMLButtonElement;
    act(() => submit.click());

    expect(onSubmit).not.toHaveBeenCalled();
    expect(onCommand).toHaveBeenCalledTimes(1);
    expect(onCommand.mock.calls[0][0]).toMatchObject({ name: "export", kind: "action" });
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
    expect(container.querySelector("[data-composer-command-notice]")).toBeNull();
  });
});
