// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { MessageComposer } from "./message-composer";
import { type ComposerAttachmentsController } from "@/hooks/workspace/composer/use-composer-attachments";
import { createComposerAttachment } from "@/lib/composer-attachments";
import {
  readComposerTextareaMetrics,
  resolveComposerTextareaLayout,
} from "@/lib/composer-textarea";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

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

describe("MessageComposer", () => {
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
    Reflect.deleteProperty(HTMLTextAreaElement.prototype, "scrollHeight");
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  // jsdom 无排版：直接桩住 scrollHeight，验证组件的「度量 → 布局 → 样式」接线。
  function stubTextareaScrollHeight(value: number) {
    Object.defineProperty(HTMLTextAreaElement.prototype, "scrollHeight", {
      configurable: true,
      get: () => value,
    });
  }

  function renderComposer(
    overrides: Partial<React.ComponentProps<typeof MessageComposer>> = {},
  ) {
    const props: React.ComponentProps<typeof MessageComposer> = {
      attachments: createAttachmentsStub(),
      density: "comfortable",
      draft: "",
      hasSession: false,
      isNewThread: true,
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
      runtimeModelsError: null,
      runtimeModelsLoading: false,
      selectedArtifactCount: 0,
      selectedModel: "model-a",
      selectedProvider: "provider-a",
      selectedReasoningEffort: "",
      ...overrides,
    };

    act(() => root?.render(<MessageComposer {...props} />));
    return props;
  }

  it("keeps the idle new-thread composer focused on input and actions", () => {
    renderComposer();

    expect(container.textContent).not.toContain("0 个文件");
    expect(container.textContent).not.toContain("新会话");
    expect(container.textContent).not.toContain("提示建议");
    expect(container.textContent).not.toContain("模型 model-a");
    expect(container.textContent).not.toContain("Ctrl/Cmd + Enter");

    const textarea = container.querySelector("textarea");
    const submitButton = container.querySelector(
      'button[aria-label="开始新线程"]',
    ) as HTMLButtonElement | null;

    expect(textarea?.className).toContain("min-h-[7rem]");
    expect(submitButton?.disabled).toBe(true);
    expect(submitButton?.title).toContain("Ctrl/Cmd + Enter");
  });

  it("shows only changing status and preserves submit and stop actions", () => {
    const onSubmit = vi.fn();
    const onStop = vi.fn();

    renderComposer({
      draft: "Implement the change",
      onStop,
      onSubmit,
      runtimeModelsError: "catalog offline",
      selectedArtifactCount: 2,
      transport: "error",
    });

    expect(container.textContent).toContain("运行时错误");
    expect(container.textContent).toContain("2 个文件");
    expect(container.textContent).toContain("catalog offline");

    const submitButton = container.querySelector(
      'button[aria-label="开始新线程"]',
    );
    act(() => {
      submitButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onSubmit).toHaveBeenCalledTimes(1);

    renderComposer({
      draft: "Implement the change",
      isResponding: true,
      onStop,
      onSubmit,
    });

    expect(container.textContent).toContain("响应中");
    const stopButton = container.querySelector(
      'button[aria-label="停止响应"]',
    );
    act(() => {
      stopButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onStop).toHaveBeenCalledTimes(1);
  });

  it("renders the reasoning effort picker with the config default label", () => {
    const onReasoningEffortChange = vi.fn();
    renderComposer({
      onReasoningEffortChange,
      reasoningEffortDefault: "medium",
      reasoningEffortOptions: ["low", "high"],
    });

    const trigger = container.querySelector(
      'button[aria-label="推理强度"]',
    ) as HTMLButtonElement | null;
    expect(trigger).not.toBeNull();
    expect(trigger?.textContent).toContain("默认（medium）");
    expect(trigger?.disabled).toBe(false);
  });

  it("hides the reasoning effort picker when the model declares none", () => {
    renderComposer({ reasoningEffortOptions: [] });

    expect(container.querySelector('button[aria-label="推理强度"]')).toBeNull();
  });

  it("caps the textarea at 14 lines and scrolls inside when the draft overflows", () => {
    stubTextareaScrollHeight(900);
    renderComposer({
      draft: Array.from({ length: 30 }, (_, index) => `line ${index + 1}`).join(
        "\n",
      ),
    });

    const textarea = container.querySelector("textarea") as HTMLTextAreaElement;
    const expected = resolveComposerTextareaLayout(
      readComposerTextareaMetrics(textarea),
    );

    expect(expected.capped).toBe(true);
    expect(textarea.style.height).toBe(`${expected.height}px`);
    expect(textarea.style.overflowY).toBe("auto");
  });

  it("grows with the draft while it stays under the cap", () => {
    stubTextareaScrollHeight(60);
    renderComposer({ draft: "one line" });

    const textarea = container.querySelector("textarea") as HTMLTextAreaElement;

    expect(textarea.style.height).toBe("60px");
    expect(textarea.style.overflowY).toBe("hidden");
  });

  it("focuses the input on mount, on session switch and after submit", () => {
    renderComposer({ draft: "ready to send" });
    const textarea = container.querySelector("textarea") as HTMLTextAreaElement;

    expect(document.activeElement).toBe(textarea);

    act(() => textarea.blur());
    expect(document.activeElement).not.toBe(textarea);

    const { onSubmit } = renderComposer({
      draft: "ready to send",
      focusKey: "session-b",
    });
    expect(document.activeElement).toBe(textarea);

    act(() => textarea.blur());
    const submitButton = container.querySelector(
      'button[aria-label="开始新线程"]',
    );
    act(() => {
      submitButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onSubmit).toHaveBeenCalledTimes(1);
    expect(document.activeElement).toBe(textarea);
  });

  it("routes the file input and paste payloads through the attachment controller", () => {
    const attachments = createAttachmentsStub();
    renderComposer({ attachments });

    const input = container.querySelector(
      "input[data-composer-file-input]",
    ) as HTMLInputElement;
    expect(input).not.toBeNull();

    const clickSpy = vi.spyOn(input, "click").mockImplementation(() => {});
    // `+` 按钮与 `/`、`@` 同源：先开菜单，再从菜单里选「添加附件」。
    act(() => {
      container
        .querySelector("button[data-composer-menu-trigger]")
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    act(() => {
      container
        .querySelector("[data-composer-attach]")
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(clickSpy).toHaveBeenCalledTimes(1);

    const file = new File([new Uint8Array(4)], "notes.txt", {
      type: "text/plain",
    });
    Object.defineProperty(input, "files", {
      value: [file],
      configurable: true,
    });
    act(() => {
      input.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(attachments.addFiles).toHaveBeenCalledTimes(1);

    const textarea = container.querySelector("textarea") as HTMLTextAreaElement;
    const pasteEvent = new Event("paste", { bubbles: true, cancelable: true });
    Object.defineProperty(pasteEvent, "clipboardData", {
      value: { files: [file] },
    });
    act(() => {
      textarea.dispatchEvent(pasteEvent);
    });
    expect(attachments.addFiles).toHaveBeenCalledTimes(2);
  });

  it("keeps pending attachments visible and blocks submit until they are removed", () => {
    const onSubmit = vi.fn();
    const removeAttachment = vi.fn();
    const attachment = createComposerAttachment(
      new File([new Uint8Array(4)], "shot.png", { type: "image/png" }),
      { id: "a-1", previewUrl: "blob:preview" },
    );
    renderComposer({
      attachments: createAttachmentsStub({
        attachments: [attachment],
        removeAttachment,
      }),
      draft: "ship it",
      onSubmit,
    });

    expect(
      container.querySelector("[data-composer-attachment-rail]"),
    ).not.toBeNull();
    expect(
      container.querySelector('[data-composer-attachment][data-attachment-status="pending"]'),
    ).not.toBeNull();
    expect(
      container.querySelector("[data-composer-attachment-preview]"),
    ).not.toBeNull();
    expect(container.textContent).toContain("1 个附件待发送");
    expect(container.textContent).toContain("附件上传接口未就绪");

    const submitButton = container.querySelector(
      'button[aria-label="开始新线程"]',
    ) as HTMLButtonElement | null;
    expect(submitButton?.disabled).toBe(true);
    act(() => {
      submitButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onSubmit).not.toHaveBeenCalled();

    act(() => {
      container
        .querySelector('[data-composer-attachment-remove="a-1"]')
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(removeAttachment).toHaveBeenCalledWith("a-1");
  });

  it("announces rejected files and clears the notice on acknowledgement", () => {
    const acknowledgeRejections = vi.fn();
    renderComposer({
      attachments: createAttachmentsStub({
        rejectedCount: 2,
        acknowledgeRejections,
      }),
    });

    expect(container.textContent).toContain("已忽略 2 个文件");
    act(() => {
      container
        .querySelector("[data-composer-attachments-rejected]")
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(acknowledgeRejections).toHaveBeenCalledTimes(1);
  });

  it("shows the full-viewport drop invitation only while files hover", () => {
    renderComposer({
      attachments: createAttachmentsStub({ isDragOver: true }),
    });
    expect(
      container.querySelector("[data-composer-drop-invitation]"),
    ).not.toBeNull();
    expect(container.textContent).toContain("松开以添加附件");

    renderComposer({
      attachments: createAttachmentsStub({ isDragOver: false }),
    });
    expect(
      container.querySelector("[data-composer-drop-invitation]"),
    ).toBeNull();
  });
});
