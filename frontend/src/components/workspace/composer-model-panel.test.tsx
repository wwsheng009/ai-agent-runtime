// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ComposerModelPanel } from "./composer-model-panel";

type PanelSectionId = "provider" | "model" | "reasoning";

function dispatchPointerDown(target: EventTarget) {
  act(() => {
    target.dispatchEvent(new Event("pointerdown", { bubbles: true }));
  });
}

function dispatchClick(target: EventTarget) {
  act(() => {
    target.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

function dispatchKeyDown(target: EventTarget, key: string) {
  act(() => {
    target.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key }));
  });
}

describe("ComposerModelPanel", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);

    Object.defineProperty(window, "innerWidth", {
      configurable: true,
      value: 1280,
      writable: true,
    });
    Object.defineProperty(window, "innerHeight", {
      configurable: true,
      value: 720,
      writable: true,
    });

    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      callback(0);
      return 0;
    });
  });

  afterEach(() => {
    if (root) {
      act(() => {
        root?.unmount();
      });
    }
    container.remove();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    document.body.innerHTML = "";
  });

  function renderPanel(
    overrides: Partial<React.ComponentProps<typeof ComposerModelPanel>> = {},
  ) {
    const props: React.ComponentProps<typeof ComposerModelPanel> = {
      modelOptions: ["model-a", "model-b"],
      onModelChange: vi.fn(),
      onProviderChange: vi.fn(),
      onReasoningEffortChange: vi.fn(),
      providerOptions: ["provider-a", "provider-b"],
      reasoningEffortDefault: "medium",
      reasoningEffortOptions: ["low", "high"],
      selectedModel: "model-a",
      selectedProvider: "provider-a",
      selectedReasoningEffort: "",
      ...overrides,
    };

    act(() => {
      root?.render(<ComposerModelPanel {...props} />);
    });

    return props;
  }

  function queryTrigger(): HTMLButtonElement | null {
    return container.querySelector("[data-composer-model-panel-trigger]");
  }

  function queryPanel(): HTMLElement | null {
    return document.body.querySelector("[data-composer-model-panel]");
  }

  function queryLevel(): string | null {
    return queryPanel()?.getAttribute("data-composer-model-panel-level") ?? null;
  }

  function queryTitle(): string {
    return (
      document.body.querySelector("[data-composer-model-panel-title]")
        ?.textContent ?? ""
    );
  }

  function querySummary(): string {
    return (
      container.querySelector("[data-composer-model-panel-summary]")
        ?.textContent ?? ""
    );
  }

  function queryPendingDot(): Element | null {
    return container.querySelector("[data-composer-model-panel-pending]");
  }

  function queryRow(id: PanelSectionId): HTMLButtonElement | null {
    return document.body.querySelector(
      `[data-composer-model-panel-row="${id}"]`,
    );
  }

  function isRowPending(id: PanelSectionId): boolean {
    return (
      queryRow(id)?.getAttribute("data-composer-model-panel-row-pending") ===
      "true"
    );
  }

  function queryBack(): HTMLButtonElement | null {
    return document.body.querySelector("[data-composer-model-panel-back]");
  }

  function querySection(id: PanelSectionId): HTMLElement | null {
    return document.body.querySelector(
      `[data-composer-model-panel-section="${id}"]`,
    );
  }

  function queryOptions(id: PanelSectionId): HTMLButtonElement[] {
    return Array.from(
      document.body.querySelectorAll<HTMLButtonElement>(
        `[data-composer-model-panel-option="${id}"]`,
      ),
    );
  }

  function openPanel(): HTMLElement {
    dispatchClick(queryTrigger() as HTMLButtonElement);
    const panel = queryPanel();
    expect(panel).toBeInstanceOf(HTMLDivElement);
    return panel as HTMLElement;
  }

  /** 一级行 → 二级候选列表。 */
  function drillInto(id: PanelSectionId) {
    dispatchClick(queryRow(id) as HTMLButtonElement);
    expect(querySection(id)).not.toBeNull();
  }

  it("summarizes provider / model / reasoning on the trigger and opens one portal panel", () => {
    renderPanel();

    const trigger = queryTrigger();
    expect(trigger).toBeInstanceOf(HTMLButtonElement);
    expect(querySummary()).toBe("provider-a · model-a · 默认（medium）");
    expect(queryTrigger()?.getAttribute("aria-expanded")).toBe("false");

    // 关闭时只有触发器，没有残留弹层。
    expect(queryPanel()).toBeNull();

    const panel = openPanel();

    expect(queryTrigger()?.getAttribute("aria-expanded")).toBe("true");
    expect(panel.parentElement).toBe(document.body);
    expect(panel.style.position).toBe("fixed");
    expect(panel.style.bottom).not.toBe("");
    expect(panel.style.minWidth).not.toBe("");
    expect(panel.getAttribute("role")).toBe("dialog");
    // 打开先落在一级：三个座位各一行摘要。
    expect(queryLevel()).toBe("root");
  });

  it("keeps provider / model / reasoning as top-level rows and shows candidates one level down", () => {
    renderPanel();

    openPanel();

    expect(queryRow("provider")?.textContent).toContain("provider-a");
    expect(queryRow("model")?.textContent).toContain("model-a");
    expect(queryRow("reasoning")?.textContent).toContain("默认（medium）");
    // 一级只列座位，不铺候选。
    expect(queryOptions("provider")).toHaveLength(0);
    expect(querySection("model")).toBeNull();
    expect(queryTitle()).toBe("模型与推理");

    drillInto("model");

    expect(queryLevel()).toBe("section");
    expect(queryTitle()).toBe("Model");
    expect(queryOptions("model")).toHaveLength(2);
    expect(queryOptions("model")[0]?.getAttribute("aria-selected")).toBe("true");
    // 二级只渲染当前这一项。
    expect(querySection("provider")).toBeNull();
    expect(querySection("reasoning")).toBeNull();
  });

  it("applies a picked model through the host handler and keeps the list open", () => {
    const onModelChange = vi.fn();
    renderPanel({ onModelChange });

    openPanel();
    drillInto("model");
    dispatchClick(queryOptions("model")[1] as HTMLButtonElement);

    expect(onModelChange).toHaveBeenCalledTimes(1);
    expect(onModelChange).toHaveBeenCalledWith("model-b");
    // 普通换模型不关面板，也不跳走：用户还要继续比较。
    expect(queryPanel()).not.toBeNull();
    expect(queryLevel()).toBe("section");
    expect(querySection("model")).not.toBeNull();
  });

  it("marks model and reasoning for reconfirmation after a provider switch and walks to the model list", () => {
    const onProviderChange = vi.fn();
    renderPanel({ onProviderChange });

    openPanel();
    drillInto("provider");
    dispatchClick(queryOptions("provider")[1] as HTMLButtonElement);

    expect(onProviderChange).toHaveBeenCalledWith("provider-b");
    // 换供应商后自动下钻到模型列表：模型必须重新确认。
    expect(queryLevel()).toBe("section");
    expect(querySection("model")).not.toBeNull();

    dispatchClick(queryBack() as HTMLButtonElement);

    expect(queryLevel()).toBe("root");
    expect(isRowPending("model")).toBe(true);
    expect(isRowPending("reasoning")).toBe(true);
    expect(isRowPending("provider")).toBe(false);
    // 关着面板也要能看出「还欠一次确认」。
    expect(queryPendingDot()).not.toBeNull();
    expect(queryTrigger()?.title).toContain("已切换供应商");
  });

  it("walks a provider switch through model then reasoning and clears the reconfirmation", () => {
    const onProviderChange = vi.fn();
    const onModelChange = vi.fn();
    const onReasoningEffortChange = vi.fn();
    renderPanel({ onModelChange, onProviderChange, onReasoningEffortChange });

    openPanel();
    drillInto("provider");
    dispatchClick(queryOptions("provider")[1] as HTMLButtonElement);

    // 选完模型自动进入推理列表：换供应商后这一档也要重新确认。
    dispatchClick(queryOptions("model")[1] as HTMLButtonElement);
    expect(onModelChange).toHaveBeenCalledWith("model-b");
    expect(queryLevel()).toBe("section");
    expect(querySection("reasoning")).not.toBeNull();

    dispatchClick(queryOptions("reasoning")[2] as HTMLButtonElement);
    expect(onReasoningEffortChange).toHaveBeenCalledWith("high");

    // 两项都确认完 → 回到一级、提醒消失。
    expect(queryLevel()).toBe("root");
    expect(isRowPending("model")).toBe(false);
    expect(isRowPending("reasoning")).toBe(false);
    expect(queryPendingDot()).toBeNull();
  });

  it("lets the follow-default reasoning row close the reconfirmation flow", () => {
    const onReasoningEffortChange = vi.fn();
    renderPanel({ onReasoningEffortChange });

    openPanel();
    drillInto("provider");
    dispatchClick(queryOptions("provider")[1] as HTMLButtonElement);
    dispatchClick(queryOptions("model")[0] as HTMLButtonElement);
    // 「跟随默认」也是一次显式选择。
    dispatchClick(queryOptions("reasoning")[0] as HTMLButtonElement);

    expect(onReasoningEffortChange).toHaveBeenCalledWith("");
    expect(queryLevel()).toBe("root");
    expect(queryPendingDot()).toBeNull();
  });

  it("skips the reasoning step when the current model declares no reasoning options", () => {
    const onModelChange = vi.fn();
    renderPanel({ modelOptions: ["model-a", "model-b"], onModelChange, reasoningEffortOptions: [] });

    openPanel();
    expect(queryRow("reasoning")).toBeNull();

    drillInto("provider");
    dispatchClick(queryOptions("provider")[1] as HTMLButtonElement);
    dispatchClick(queryOptions("model")[1] as HTMLButtonElement);

    expect(onModelChange).toHaveBeenCalledWith("model-b");
    // 没有推理段可重选 → 直接回到一级，不留待确认。
    expect(queryLevel()).toBe("root");
    expect(isRowPending("model")).toBe(false);
    expect(queryPendingDot()).toBeNull();
  });

  it("does not invalidate anything when the same provider is picked again", () => {
    const onProviderChange = vi.fn();
    renderPanel({ onProviderChange });

    openPanel();
    drillInto("provider");
    dispatchClick(queryOptions("provider")[0] as HTMLButtonElement);

    expect(onProviderChange).toHaveBeenCalledWith("provider-a");
    expect(queryLevel()).toBe("root");
    expect(isRowPending("model")).toBe(false);
    expect(isRowPending("reasoning")).toBe(false);
    expect(queryPendingDot()).toBeNull();
  });

  it("goes back one level with the back button, ArrowLeft and Escape, and closes from the top level", () => {
    renderPanel();

    const trigger = queryTrigger() as HTMLButtonElement;
    let panel = openPanel();

    drillInto("provider");
    dispatchClick(queryBack() as HTMLButtonElement);
    expect(queryLevel()).toBe("root");

    drillInto("provider");
    dispatchKeyDown(panel, "ArrowLeft");
    expect(queryLevel()).toBe("root");

    drillInto("provider");
    dispatchKeyDown(panel, "Escape");
    // 一次 Esc 只退一层：面板还在。
    expect(queryLevel()).toBe("root");
    expect(queryPanel()).not.toBeNull();

    dispatchKeyDown(panel, "Escape");
    expect(queryPanel()).toBeNull();
    expect(document.activeElement).toBe(trigger);

    // 重新打开回到一级，不保留上次的二级位置。
    panel = openPanel();
    expect(queryLevel()).toBe("root");
    expect(querySection("provider")).toBeNull();
  });

  it("jumps back to the top level when a candidate list disappears under the open level", () => {
    renderPanel();

    openPanel();
    drillInto("reasoning");
    expect(querySection("reasoning")).not.toBeNull();

    // 宿主投影的新候选里没有推理段（例如换成不支持推理的模型）。
    act(() => {
      root?.render(
        <ComposerModelPanel
          modelOptions={["model-a", "model-b"]}
          onModelChange={vi.fn()}
          onProviderChange={vi.fn()}
          onReasoningEffortChange={vi.fn()}
          providerOptions={["provider-a", "provider-b"]}
          reasoningEffortDefault="medium"
          reasoningEffortOptions={[]}
          selectedModel="model-a"
          selectedProvider="provider-a"
          selectedReasoningEffort=""
        />,
      );
    });

    expect(queryLevel()).toBe("root");
    expect(queryRow("reasoning")).toBeNull();
    expect(queryRow("model")).not.toBeNull();
  });

  it("closes on outside pointerdown and on the close button", () => {
    renderPanel();

    openPanel();
    dispatchPointerDown(document.body);
    expect(queryPanel()).toBeNull();

    openPanel();
    const closeButton = document.body.querySelector(
      "[data-composer-model-panel-close]",
    );
    dispatchClick(closeButton as HTMLButtonElement);
    expect(queryPanel()).toBeNull();
  });

  it("moves focus between the entries of the current level with the arrow keys", () => {
    renderPanel();

    const panel = openPanel();

    dispatchKeyDown(panel, "ArrowDown");
    expect(document.activeElement).toBe(queryRow("provider"));

    dispatchKeyDown(panel, "ArrowDown");
    expect(document.activeElement).toBe(queryRow("model"));

    dispatchKeyDown(panel, "ArrowUp");
    expect(document.activeElement).toBe(queryRow("provider"));

    // 从一级首行再向上：绕到最后一行，而不是丢焦点。
    dispatchKeyDown(panel, "ArrowUp");
    expect(document.activeElement).toBe(queryRow("reasoning"));

    // 二级里的上下键只在当前候选列表内移动，且从当前选中项开始。
    drillInto("model");
    expect(document.activeElement).toBe(queryOptions("model")[0]);

    dispatchKeyDown(panel, "ArrowDown");
    expect(document.activeElement).toBe(queryOptions("model")[1]);

    dispatchKeyDown(panel, "ArrowUp");
    expect(document.activeElement).toBe(queryOptions("model")[0]);
  });

  it("hides a seat with no candidates and drops it from the summary", () => {
    renderPanel({
      providerOptions: ["provider-a"],
      reasoningEffortOptions: [],
      selectedReasoningEffort: "",
    });

    expect(querySummary()).toBe("model-a");

    openPanel();

    expect(queryRow("provider")).toBeNull();
    expect(queryRow("reasoning")).toBeNull();
    expect(queryRow("model")).not.toBeNull();
  });

  it("renders nothing when there are no candidates at all", () => {
    renderPanel({
      modelOptions: [],
      providerOptions: [],
      reasoningEffortOptions: [],
      selectedModel: "",
      selectedProvider: "",
    });

    expect(queryTrigger()).toBeNull();
    expect(queryPanel()).toBeNull();
  });

  it("keeps the disabled reason visible and refuses to open while disabled", () => {
    renderPanel({ disabled: true, disabledReason: "响应中不可切换模型" });

    const trigger = queryTrigger() as HTMLButtonElement;
    expect(trigger.disabled).toBe(true);
    expect(trigger.title).toBe("响应中不可切换模型");

    dispatchClick(trigger);
    expect(queryPanel()).toBeNull();
  });
});
