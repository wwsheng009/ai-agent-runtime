// @vitest-environment jsdom

// ESC 中断方案阶段 A：运行中 Esc 与 Stop 按钮等价。
// 覆盖：响应中触发、空闲忽略、模态让位、单回合只投递一次、下一回合重新武装。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useEscapeStopResponding } from "./use-escape-stop-responding";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

type HarnessProps = {
  responding: boolean;
  onStop: () => void;
};

function Harness({ responding, onStop }: HarnessProps) {
  useEscapeStopResponding({ responding, onStop });
  return null;
}

describe("useEscapeStopResponding", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function render(props: HarnessProps) {
    act(() => {
      root.render(<Harness {...props} />);
    });
  }

  function dispatchEscape(init: KeyboardEventInit = {}): KeyboardEvent {
    const event = new KeyboardEvent("keydown", {
      key: "Escape",
      bubbles: true,
      cancelable: true,
      ...init,
    });
    act(() => {
      window.dispatchEvent(event);
    });
    return event;
  }

  it("stops the turn on Escape while responding", () => {
    const onStop = vi.fn();
    render({ responding: true, onStop });

    const event = dispatchEscape();

    expect(onStop).toHaveBeenCalledTimes(1);
    expect(event.defaultPrevented).toBe(true);
  });

  it("ignores Escape when not responding", () => {
    const onStop = vi.fn();
    render({ responding: false, onStop });

    const event = dispatchEscape();

    expect(onStop).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);
  });

  it("lets modal dialogs own Escape", () => {
    const onStop = vi.fn();
    render({ responding: true, onStop });
    const modal = document.createElement("div");
    modal.setAttribute("aria-modal", "true");
    document.body.appendChild(modal);

    const event = dispatchEscape();

    expect(onStop).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);
  });

  it("delivers at most one stop per running turn", () => {
    const onStop = vi.fn();
    render({ responding: true, onStop });

    dispatchEscape();
    dispatchEscape();

    expect(onStop).toHaveBeenCalledTimes(1);
  });

  it("arms again for the next turn", () => {
    const onStop = vi.fn();
    render({ responding: true, onStop });
    dispatchEscape();

    render({ responding: false, onStop });
    render({ responding: true, onStop });
    dispatchEscape();

    expect(onStop).toHaveBeenCalledTimes(2);
  });

  it("ignores modified or already handled Escape", () => {
    const onStop = vi.fn();
    render({ responding: true, onStop });

    expect(dispatchEscape({ ctrlKey: true }).defaultPrevented).toBe(false);

    const handled = new KeyboardEvent("keydown", {
      key: "Escape",
      bubbles: true,
      cancelable: true,
    });
    handled.preventDefault();
    act(() => {
      window.dispatchEvent(handled);
    });

    expect(onStop).not.toHaveBeenCalled();
  });
});
