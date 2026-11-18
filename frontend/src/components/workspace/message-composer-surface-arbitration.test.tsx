// @vitest-environment jsdom
//
// P1-1 / P1-3：composer 浮层互斥与模型控件命中稳定性的专项回归。
// 与 message-composer-menu.test.tsx 分开，避免单文件超行数门禁。

import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { MessageComposer } from "./message-composer";
import { type ComposerAttachmentsController } from "@/hooks/workspace/composer/use-composer-attachments";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function createAttachmentsStub(): ComposerAttachmentsController {
  return {
    attachments: [],
    isDragOver: false,
    rejectedCount: 0,
    uploadingCount: 0,
    uploadedCount: 0,
    unsettledCount: 0,
    uploadedPaths: [],
    addFiles: vi.fn(),
    removeAttachment: vi.fn(),
    retryAttachment: vi.fn(),
    clearAttachments: vi.fn(),
    acknowledgeRejections: vi.fn(),
  };
}

describe("MessageComposer surface arbitration", () => {
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

  function renderComposer() {
    const props: React.ComponentProps<typeof MessageComposer> = {
      attachments: createAttachmentsStub(),
      density: "comfortable",
      draft: "",
      hasSession: true,
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
      selectedModel: "model-a",
      selectedProvider: "provider-a",
      selectedReasoningEffort: "",
    };

    act(() =>
      root?.render(<ComposerHarness props={props} />),
    );
  }

  function ComposerHarness({
    props,
  }: {
    props: React.ComponentProps<typeof MessageComposer>;
  }) {
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

  function menu() {
    return container.querySelector("[data-composer-menu]");
  }

  function modelPanel() {
    return document.querySelector("[data-composer-model-panel]");
  }

  it("keeps at most one composer surface expanded (menu yields to model panel)", () => {
    renderComposer();

    const menuTrigger = container.querySelector(
      "button[data-composer-menu-trigger]",
    ) as HTMLButtonElement;
    act(() => menuTrigger.click());
    expect(menu()).not.toBeNull();

    const modelTrigger = container.querySelector(
      "button[data-composer-model-panel-trigger]",
    ) as HTMLButtonElement;
    expect(modelTrigger).not.toBeNull();
    act(() => {
      modelTrigger.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(menu()).toBeNull();
    expect(modelPanel()).not.toBeNull();
  });

  it("adds a scrim for the trigger menu and closes it on scrim pointer down", () => {
    renderComposer();

    const trailingActions = container.querySelector(
      "[data-composer-trailing-actions]",
    );
    expect(trailingActions?.className).toContain("flex-nowrap");
    expect(trailingActions?.className).toContain("shrink-0");

    expect(container.querySelector("[data-composer-centered-placeholder]")).not.toBeNull();
    const menuTrigger = container.querySelector(
      "button[data-composer-menu-trigger]",
    ) as HTMLButtonElement;
    act(() => menuTrigger.click());

    const scrim = document.querySelector(
      "[data-composer-menu-scrim]",
    ) as HTMLDivElement | null;
    expect(scrim).not.toBeNull();
    expect(scrim?.getAttribute("aria-hidden")).toBe("true");

    act(() => {
      scrim?.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    });

    expect(menu()).toBeNull();
    expect(document.querySelector("[data-composer-menu-scrim]")).toBeNull();
  });

  it("responds to repeated model trigger clicks without swallowing input", () => {
    renderComposer();

    const modelTrigger = container.querySelector(
      "button[data-composer-model-panel-trigger]",
    ) as HTMLButtonElement;

    for (let click = 1; click <= 5; click += 1) {
      act(() => {
        modelTrigger.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      });
      expect(Boolean(modelPanel())).toBe(click % 2 === 1);
    }
  });
});