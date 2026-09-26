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
  const attachments = overrides.attachments ?? [];
  return {
    attachments,
    isDragOver: false,
    rejectedCount: 0,
    uploadingCount: attachments.filter((item) => item.status === "uploading").length,
    uploadedCount: attachments.filter((item) => item.status === "uploaded").length,
    unsettledCount: attachments.filter((item) => item.status !== "uploaded").length,
    uploadedPaths: attachments
      .filter((item) => item.status === "uploaded" && item.remotePath)
      .map((item) => item.remotePath as string),
    addFiles: vi.fn(),
    removeAttachment: vi.fn(),
    retryAttachment: vi.fn(),
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

  // 生产故障（2026-09-18 现场）：同会话仍有在途回合时 submitPrompt 静默返回
  // false（同会话单飞闸门），按钮看起来「点了没反应」。拦下必须可见。
  it("提交被拦下时给出可见提示，成功提交后不再提示", () => {
    const blocked = vi.fn(() => false);
    renderComposer({
      draft: "ls files",
      hasSession: true,
      isNewThread: false,
      onSubmit: blocked,
    });

    const submitButton = container.querySelector(
      'button[aria-label="发送回合"]',
    );
    expect(submitButton).not.toBeNull();
    act(() => {
      submitButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(blocked).toHaveBeenCalledTimes(1);
    const notice = container.querySelector("[data-composer-submit-blocked]");
    expect(notice).not.toBeNull();
    expect(notice?.textContent).toContain("消息未发送");

    act(() => {
      notice
        ?.querySelector("[data-composer-submit-blocked-dismiss]")
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(
      container.querySelector("[data-composer-submit-blocked]"),
    ).toBeNull();

    // 成功提交（返回 true）不产生提示。
    renderComposer({
      draft: "ls files",
      hasSession: true,
      isNewThread: false,
      onSubmit: vi.fn(() => true),
    });
    act(() => {
      container
        .querySelector('button[aria-label="发送回合"]')
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(
      container.querySelector("[data-composer-submit-blocked]"),
    ).toBeNull();
  });

  it("combines provider / model / reasoning into one panel trigger", () => {
    renderComposer({
      providerOptions: ["provider-a", "provider-b"],
      reasoningEffortDefault: "medium",
      reasoningEffortOptions: ["low", "high"],
    });

    const trigger = container.querySelector(
      "[data-composer-model-panel-trigger]",
    ) as HTMLButtonElement | null;
    expect(trigger).not.toBeNull();
    expect(trigger?.textContent).toContain(
      "provider-a · model-a · 默认（medium）",
    );
    expect(trigger?.disabled).toBe(false);

    // 三项不再各自占用工具条座位。
    expect(container.querySelector('button[aria-label="Provider"]')).toBeNull();
    expect(container.querySelector('button[aria-label="Model"]')).toBeNull();
    expect(container.querySelector('button[aria-label="推理强度"]')).toBeNull();

    act(() => {
      trigger?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(document.body.querySelector("[data-composer-model-panel]")).not.toBeNull();
    // 一级只列座位，候选在二级列表里。
    expect(
      document.body.querySelector(
        '[data-composer-model-panel-row="provider"]',
      ),
    ).not.toBeNull();
    expect(
      document.body.querySelector('[data-composer-model-panel-row="model"]'),
    ).not.toBeNull();
    expect(
      document.body.querySelector(
        '[data-composer-model-panel-row="reasoning"]',
      ),
    ).not.toBeNull();
  });

  it("applies a model picked from the combined panel and keeps it open", () => {
    const onModelChange = vi.fn();
    renderComposer({ modelOptions: ["model-a", "model-b"], onModelChange });

    const trigger = container.querySelector(
      "[data-composer-model-panel-trigger]",
    ) as HTMLButtonElement | null;
    act(() => {
      trigger?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    const modelRow = document.body.querySelector(
      '[data-composer-model-panel-row="model"]',
    );
    act(() => {
      modelRow?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    const modelOption = Array.from(
      document.body.querySelectorAll<HTMLButtonElement>(
        '[data-composer-model-panel-option="model"]',
      ),
    ).find((node) => node.textContent?.includes("model-b"));
    expect(modelOption).not.toBeUndefined();

    act(() => {
      modelOption?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(onModelChange).toHaveBeenCalledWith("model-b");
    expect(document.body.querySelector("[data-composer-model-panel]")).not.toBeNull();
  });

  it("hides the reasoning section when the model declares none", () => {
    renderComposer({ reasoningEffortOptions: [] });

    const trigger = container.querySelector(
      "[data-composer-model-panel-trigger]",
    ) as HTMLButtonElement | null;
    expect(trigger).not.toBeNull();

    act(() => {
      trigger?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(
      document.body.querySelector(
        '[data-composer-model-panel-row="reasoning"]',
      ),
    ).toBeNull();
    expect(
      document.body.querySelector('[data-composer-model-panel-row="model"]'),
    ).not.toBeNull();
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

  it("keeps unsettled attachments visible and blocks submit until they are settled", () => {
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
      container.querySelector(
        '[data-composer-attachment][data-attachment-status="uploading"]',
      ),
    ).not.toBeNull();
    expect(
      container.querySelector("[data-composer-attachment-preview]"),
    ).not.toBeNull();
    expect(container.textContent).toContain("1 个附件上传中");
    expect(container.textContent).toContain("1 个附件未上传完成");

    const submitButton = container.querySelector(
      'button[aria-label="开始新线程"]',
    ) as HTMLButtonElement | null;
    expect(submitButton?.disabled).toBe(true);
    act(() => {
      submitButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onSubmit).not.toHaveBeenCalled();
    // 键盘捷径也不能绕过闸门（按钮 disabled 之外还有一条提交路径）。
    act(() => {
      container.querySelector("textarea")?.dispatchEvent(
        new KeyboardEvent("keydown", {
          key: "Enter",
          ctrlKey: true,
          bubbles: true,
          cancelable: true,
        }),
      );
    });
    expect(onSubmit).not.toHaveBeenCalled();
    expect(
      container.querySelector('[data-composer-submit-blocked="attachments"]'),
    ).not.toBeNull();

    act(() => {
      container
        .querySelector('[data-composer-attachment-remove="a-1"]')
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(removeAttachment).toHaveBeenCalledWith("a-1");
  });

  it("sends only uploaded paths and never fabricates an images payload", () => {
    const onSubmit = vi.fn(() => true);
    const settled = {
      ...createComposerAttachment(
        new File([new Uint8Array(4)], "shot.png", { type: "image/png" }),
        { id: "a-1" },
      ),
      status: "uploaded" as const,
      remotePath: "/srv/uploads/shot.png",
    };
    const failed = {
      ...createComposerAttachment(
        new File([new Uint8Array(4)], "broken.png", { type: "image/png" }),
        { id: "a-2" },
      ),
      status: "error" as const,
      error: "不是可识别的图片",
      errorKind: "rejected" as const,
    };

    // 有失败项：闸门拦下，一张图都不发。
    renderComposer({
      attachments: createAttachmentsStub({ attachments: [settled, failed] }),
      draft: "ship it",
      hasSession: true,
      isNewThread: false,
      onSubmit,
    });
    act(() => {
      container
        .querySelector('button[aria-label="发送"]')
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onSubmit).not.toHaveBeenCalled();
    expect(
      container.querySelector('[data-composer-attachment-error="rejected"]'),
    ).not.toBeNull();

    // 全部落定：只把服务端路径交给宿主。
    renderComposer({
      attachments: createAttachmentsStub({ attachments: [settled] }),
      draft: "ship it",
      hasSession: true,
      isNewThread: false,
      onSubmit,
    });
    act(() => {
      container
        .querySelector('button[aria-label="发送回合"]')
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onSubmit).toHaveBeenCalledWith({ images: ["/srv/uploads/shot.png"] });
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
