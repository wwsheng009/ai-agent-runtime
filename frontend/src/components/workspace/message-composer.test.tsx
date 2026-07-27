// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { MessageComposer } from "./message-composer";

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
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

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
      onStop: vi.fn(),
      onSubmit: vi.fn(),
      providerOptions: ["provider-a"],
      runtimeModelsError: null,
      runtimeModelsLoading: false,
      selectedArtifactCount: 0,
      selectedModel: "model-a",
      selectedProvider: "provider-a",
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
});
