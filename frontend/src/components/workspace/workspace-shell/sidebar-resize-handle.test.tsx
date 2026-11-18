// @vitest-environment jsdom

import { act, useRef, useState, type CSSProperties } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SidebarResizeHandle } from "./sidebar-resize-handle";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

describe("SidebarResizeHandle", () => {
  let container: HTMLDivElement;
  let root: Root;
  const onCommit = vi.fn<(widthPx: number) => void>();
  const onReset = vi.fn();

  function Harness() {
    const containerRef = useRef<HTMLDivElement | null>(null);
    const [width, setWidth] = useState(256);
    return (
      <div
        ref={containerRef}
        data-testid="grid"
        style={
          { "--workspace-sidebar-width": `${width}px` } as CSSProperties
        }
      >
        <aside aria-label="工作区导航" />
        <SidebarResizeHandle
          containerRef={containerRef}
          label="调整左侧栏宽度"
          maxWidthPx={480}
          onCommit={(nextWidth) => {
            onCommit(nextWidth);
            setWidth(nextWidth);
          }}
          onReset={onReset}
          reservedRightRailWidth={0}
          value={width}
          viewportWidth={1600}
        />
      </div>
    );
  }

  function render() {
    act(() => root.render(<Harness />));
    return {
      grid: container.querySelector<HTMLElement>('[data-testid="grid"]')!,
      handle: container.querySelector<HTMLDivElement>(
        '[data-testid="left-sidebar-resize-handle"]',
      )!,
    };
  }

  function pointerEvent(type: string, clientX: number) {
    const event = new MouseEvent(type, {
      bubbles: true,
      cancelable: true,
      button: 0,
      clientX,
    });
    Object.defineProperty(event, "pointerId", { value: 1 });
    Object.defineProperty(event, "pointerType", { value: "mouse" });
    return event;
  }

  beforeEach(() => {
    onCommit.mockClear();
    onReset.mockClear();
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("exposes an accessible separator with keyboard controls", () => {
    const { handle } = render();

    expect(handle.getAttribute("role")).toBe("separator");
    expect(handle.getAttribute("aria-orientation")).toBe("vertical");
    expect(handle.getAttribute("aria-valuemin")).toBe("208");
    expect(handle.getAttribute("aria-valuemax")).toBe("480");
    expect(handle.getAttribute("aria-valuenow")).toBe("256");
  });

  it("updates the grid during drag and commits the final width once", () => {
    const { grid, handle } = render();

    act(() => {
      handle.dispatchEvent(pointerEvent("pointerdown", 300));
      handle.dispatchEvent(pointerEvent("pointermove", 356));
    });
    expect(grid.style.getPropertyValue("--workspace-sidebar-width")).toBe(
      "312px",
    );
    expect(onCommit).not.toHaveBeenCalled();

    act(() => {
      handle.dispatchEvent(pointerEvent("pointerup", 356));
    });
    expect(onCommit).toHaveBeenCalledExactlyOnceWith(312);
  });

  it("supports keyboard resizing and double-click reset", () => {
    const { grid, handle } = render();

    act(() => {
      handle.dispatchEvent(
        new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }),
      );
    });
    expect(onCommit).toHaveBeenLastCalledWith(272);
    expect(grid.style.getPropertyValue("--workspace-sidebar-width")).toBe(
      "272px",
    );

    act(() => {
      handle.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    });
    expect(onReset).toHaveBeenCalledOnce();
  });

  it("clamps at the reserved main-area boundary", () => {
    const { handle, grid } = render();

    act(() => {
      handle.dispatchEvent(
        new KeyboardEvent("keydown", { key: "End", bubbles: true }),
      );
    });
    expect(onCommit).toHaveBeenLastCalledWith(480);
    expect(grid.style.getPropertyValue("--workspace-sidebar-width")).toBe(
      "480px",
    );
  });
});
