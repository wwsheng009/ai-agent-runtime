// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { Select } from "./select";

type RenderSelectOptions = {
  align?: "start" | "end";
  onChange?: (value: string) => void;
  side?: "top" | "bottom";
  value?: string;
};

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

describe("Select", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = null;

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

    HTMLElement.prototype.scrollIntoView = vi.fn();
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

  function renderSelect({
    align = "start",
    onChange = vi.fn(),
    side = "bottom",
    value = "gpt-5.4",
  }: RenderSelectOptions = {}) {
    root = createRoot(container);

    act(() => {
      root?.render(
        <Select
          ariaLabel="Model"
          align={align}
          side={side}
          value={value}
          onChange={onChange}
          options={[
            { value: "gpt-5.4", label: "GPT-5.4" },
            { value: "gpt-5.4-mini", label: "GPT-5.4 Mini" },
            { value: "gpt-5.3", label: "GPT-5.3" },
          ]}
          triggerClassName="w-full"
        />,
      );
    });

    const trigger = container.querySelector('button[aria-label="Model"]');
    expect(trigger).toBeInstanceOf(HTMLButtonElement);

    return {
      onChange,
      trigger: trigger as HTMLButtonElement,
    };
  }

  it("renders the menu in a portal and closes on outside pointerdown", () => {
    const { trigger } = renderSelect();

    dispatchClick(trigger);

    expect(container.querySelector('[role="listbox"]')).toBeNull();

    const listbox = document.body.querySelector('[role="listbox"]');
    expect(listbox).toBeInstanceOf(HTMLDivElement);
    expect((listbox as HTMLDivElement).style.position).toBe("fixed");

    dispatchPointerDown(document.body);

    expect(document.body.querySelector('[role="listbox"]')).toBeNull();
  });

  it("selects an option by click and calls onChange", () => {
    const onChange = vi.fn();
    const { trigger } = renderSelect({ onChange });

    dispatchClick(trigger);

    const options = document.body.querySelectorAll('[role="option"]');
    expect(options).toHaveLength(3);

    dispatchClick(options[1] as HTMLDivElement);

    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith("gpt-5.4-mini");
    expect(document.body.querySelector('[role="listbox"]')).toBeNull();
  });

  it("supports keyboard navigation and selection", () => {
    const onChange = vi.fn();
    const { trigger } = renderSelect({ onChange });

    dispatchKeyDown(trigger, "ArrowDown");

    const listbox = document.body.querySelector('[role="listbox"]');
    expect(listbox).toBeInstanceOf(HTMLDivElement);
    expect(
      (listbox as HTMLDivElement).getAttribute("aria-activedescendant"),
    ).toContain("option-0");

    dispatchKeyDown(listbox as HTMLDivElement, "ArrowDown");
    expect(
      (listbox as HTMLDivElement).getAttribute("aria-activedescendant"),
    ).toContain("option-1");

    dispatchKeyDown(listbox as HTMLDivElement, "Enter");

    expect(onChange).toHaveBeenCalledWith("gpt-5.4-mini");
    expect(document.body.querySelector('[role="listbox"]')).toBeNull();
  });

  it("positions the menu above the trigger when side is top and align is end", () => {
    const { trigger } = renderSelect({
      align: "end",
      side: "top",
    });

    dispatchClick(trigger);

    const listbox = document.body.querySelector('[role="listbox"]');
    expect(listbox).toBeInstanceOf(HTMLDivElement);
    expect((listbox as HTMLDivElement).style.position).toBe("fixed");
    expect((listbox as HTMLDivElement).style.bottom).not.toBe("");
    expect((listbox as HTMLDivElement).style.right).not.toBe("");
  });
});
