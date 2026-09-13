// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { MessageComposer } from "./message-composer";
import {
  readComposerTextareaMetrics,
  resolveComposerTextareaLayout,
} from "@/lib/composer-textarea";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

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
});
