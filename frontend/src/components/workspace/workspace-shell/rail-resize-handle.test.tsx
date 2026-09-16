// @vitest-environment jsdom
// P0-2：右栏宽度拖拽手柄的行为测试（指针时序 + 键盘 + 清理）。
//
// 设计规则（§4.2.1）：
// - pointermove 走 rAF 节流（一帧一次）并**直接写 DOM**，拖拽过程零 setState（这里用
//   onCommit 调用次数 + rAF 调度次数双向验证）；只有 pointerup 才一次性提交。
// - 键盘按 WAI-ARIA Window Splitter：←/→ = ±16px（Shift = ±64px）、Home/End = min/max、
//   Enter / 双击 = 回 auto。
// - 卸载必须恢复 body 样式并取消未执行帧（否则页面卡在 userSelect: none）。
//
// 回归红线：本组件不写右栏开合语义，只提交宽度；边界处按键不产生多余提交。

import { act, useRef, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import {
  afterEach,
  beforeEach,
  describe,
  expect,
  it,
  vi,
  type Mock,
} from "vitest";

import { RailResizeHandle } from "./rail-resize-handle";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const VIEWPORT_WIDTH = 1440;
// 1440 视口下：min = 320，max = min(832, 1440 − 256 − 512) = 672。
const MIN_WIDTH = 320;
const MAX_WIDTH = 672;

describe("RailResizeHandle", () => {
  let container: HTMLDivElement;
  let root: Root;
  let frames: Map<number, FrameRequestCallback>;
  let frameSeq: number;
  let onCommit: Mock<(px: number) => void>;
  let onReset: Mock<() => void>;

  /** 受控宿主：commit 后回写 value，模拟 workspace-shell 的真实受控形态。 */
  function Harness({ value }: { value: number }) {
    const railRef = useRef<HTMLDivElement | null>(null);
    const contentRef = useRef<HTMLDivElement | null>(null);
    const [width, setWidth] = useState(value);
    return (
      <div ref={railRef} data-testid="rail">
        <RailResizeHandle
          contentRef={contentRef}
          label="调整右侧栏宽度"
          maxWidthPx={MAX_WIDTH}
          minWidthPx={MIN_WIDTH}
          onCommit={(px) => {
            onCommit(px);
            setWidth(px);
          }}
          onReset={onReset}
          railRef={railRef}
          value={width}
          viewportWidth={VIEWPORT_WIDTH}
        />
        <div data-testid="content" ref={contentRef}>
          <button data-testid="inside" type="button">
            内容
          </button>
        </div>
      </div>
    );
  }

  function render(value = 288) {
    act(() => {
      root.render(<Harness value={value} />);
    });
    return {
      handle: container.querySelector(
        '[data-testid="right-rail-resize-handle"]',
      ) as HTMLDivElement,
      rail: container.querySelector('[data-testid="rail"]') as HTMLDivElement,
      content: container.querySelector('[data-testid="content"]') as HTMLDivElement,
    };
  }

  function railWidthVar(rail: HTMLElement) {
    return rail.style.getPropertyValue("--right-rail-width");
  }

  /** jsdom 没有 PointerEvent 构造器：用 MouseEvent 补齐指针字段。 */
  function pointerEvent(
    type: string,
    options: { clientX: number; pointerId?: number; button?: number },
  ) {
    const event = new MouseEvent(type, {
      bubbles: true,
      cancelable: true,
      button: options.button ?? 0,
      clientX: options.clientX,
    });
    Object.defineProperty(event, "pointerId", {
      value: options.pointerId ?? 1,
    });
    Object.defineProperty(event, "pointerType", { value: "mouse" });
    return event;
  }

  /** 只执行当前挂起的一帧（模拟浏览器每帧一次回调）。 */
  function flushFrame() {
    const callbacks = [...frames.values()];
    frames.clear();
    act(() => {
      for (const callback of callbacks) {
        callback(0);
      }
    });
    return callbacks.length;
  }

  beforeEach(() => {
    frames = new Map();
    frameSeq = 0;
    vi.stubGlobal(
      "requestAnimationFrame",
      (callback: FrameRequestCallback) => {
        frameSeq += 1;
        frames.set(frameSeq, callback);
        return frameSeq;
      },
    );
    vi.stubGlobal("cancelAnimationFrame", (id: number) => {
      frames.delete(id);
    });
    onCommit = vi.fn<(px: number) => void>();
    onReset = vi.fn<() => void>();
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
    vi.unstubAllGlobals();
  });

  it("按 WAI-ARIA Window Splitter 暴露 separator 语义", () => {
    const { handle } = render();

    expect(handle.getAttribute("role")).toBe("separator");
    expect(handle.getAttribute("aria-orientation")).toBe("vertical");
    expect(handle.getAttribute("aria-valuemin")).toBe(String(MIN_WIDTH));
    expect(handle.getAttribute("aria-valuemax")).toBe(String(MAX_WIDTH));
    expect(handle.getAttribute("aria-valuenow")).toBe("288");
    expect(handle.getAttribute("aria-label")).toBe("调整右侧栏宽度");
    expect(handle.tabIndex).toBe(0);
  });

  it("拖拽期间一帧只写一次 DOM，且全程零提交（pointerup 才提交一次）", () => {
    const { handle, rail } = render();

    act(() => {
      handle.dispatchEvent(pointerEvent("pointerdown", { clientX: 1000 }));
    });
    // 同一帧内的多次 pointermove：只调度一帧。
    act(() => {
      handle.dispatchEvent(pointerEvent("pointermove", { clientX: 980 }));
      handle.dispatchEvent(pointerEvent("pointermove", { clientX: 960 }));
      handle.dispatchEvent(pointerEvent("pointermove", { clientX: 940 }));
    });

    expect(frames.size).toBe(1);
    expect(railWidthVar(rail)).toBe("");
    expect(onCommit).not.toHaveBeenCalled();

    expect(flushFrame()).toBe(1);
    // 向左 60px → 右栏变宽 288 + 60 = 348px（以最后一帧的 clientX 为准）。
    expect(railWidthVar(rail)).toBe("348px");
    expect(onCommit).not.toHaveBeenCalled();

    act(() => {
      handle.dispatchEvent(pointerEvent("pointerup", { clientX: 940 }));
    });

    expect(onCommit).toHaveBeenCalledTimes(1);
    expect(onCommit).toHaveBeenCalledWith(348);
    expect(railWidthVar(rail)).toBe("348px");
  });

  it("拖拽中锁定 body 与内容层，释放后恢复（卸载也不残留）", () => {
    const { handle, content } = render();

    act(() => {
      handle.dispatchEvent(pointerEvent("pointerdown", { clientX: 1000 }));
    });
    expect(document.body.style.userSelect).toBe("none");
    expect(document.body.style.cursor).toBe("col-resize");
    expect(content.style.pointerEvents).toBe("none");

    act(() => {
      handle.dispatchEvent(pointerEvent("pointerup", { clientX: 1000 }));
    });
    expect(document.body.style.userSelect).toBe("");
    expect(document.body.style.cursor).toBe("");
    expect(content.style.pointerEvents).toBe("");
  });

  it("拖拽中卸载：恢复 body 样式并取消未执行的帧", () => {
    const { handle } = render();

    act(() => {
      handle.dispatchEvent(pointerEvent("pointerdown", { clientX: 1000 }));
      handle.dispatchEvent(pointerEvent("pointermove", { clientX: 900 }));
    });
    expect(frames.size).toBe(1);

    act(() => root.unmount());

    expect(frames.size).toBe(0);
    expect(document.body.style.userSelect).toBe("");
    expect(document.body.style.cursor).toBe("");
  });

  it("拖拽越界只收窄到 clamp 边界（min/max），不改写持久化意图以外的值", () => {
    const { handle, rail } = render();

    act(() => {
      handle.dispatchEvent(pointerEvent("pointerdown", { clientX: 1000 }));
    });
    act(() => {
      handle.dispatchEvent(pointerEvent("pointermove", { clientX: 1400 }));
    });
    expect(frames.size).toBe(1);
    act(() => {
      handle.dispatchEvent(pointerEvent("pointerup", { clientX: 1400 }));
    });

    expect(railWidthVar(rail)).toBe(`${MIN_WIDTH}px`);
    expect(onCommit).toHaveBeenCalledWith(MIN_WIDTH);

    onCommit.mockClear();
    act(() => {
      handle.dispatchEvent(pointerEvent("pointerdown", { clientX: 1000 }));
    });
    act(() => {
      handle.dispatchEvent(pointerEvent("pointermove", { clientX: 400 }));
    });
    expect(frames.size).toBe(1);
    act(() => {
      handle.dispatchEvent(pointerEvent("pointerup", { clientX: 400 }));
    });

    expect(railWidthVar(rail)).toBe(`${MAX_WIDTH}px`);
    expect(onCommit).toHaveBeenCalledWith(MAX_WIDTH);
  });

  it("键盘 ←/→ = ±16px（Shift = ±64px）", () => {
    const { handle, rail } = render(400);

    act(() => {
      handle.dispatchEvent(
        new KeyboardEvent("keydown", { bubbles: true, key: "ArrowLeft" }),
      );
    });
    expect(onCommit).toHaveBeenLastCalledWith(416);
    expect(railWidthVar(rail)).toBe("416px");

    act(() => {
      handle.dispatchEvent(
        new KeyboardEvent("keydown", {
          bubbles: true,
          key: "ArrowRight",
          shiftKey: true,
        }),
      );
    });
    expect(onCommit).toHaveBeenLastCalledWith(352);

    act(() => {
      handle.dispatchEvent(
        new KeyboardEvent("keydown", { bubbles: true, key: "ArrowRight" }),
      );
    });
    expect(onCommit).toHaveBeenLastCalledWith(336);
    expect(onCommit).toHaveBeenCalledTimes(3);
  });

  it("Home / End 直达 min / max，Enter 与双击回 auto", () => {
    const { handle } = render(400);

    act(() => {
      handle.dispatchEvent(
        new KeyboardEvent("keydown", { bubbles: true, key: "Home" }),
      );
    });
    expect(onCommit).toHaveBeenLastCalledWith(MIN_WIDTH);

    act(() => {
      handle.dispatchEvent(
        new KeyboardEvent("keydown", { bubbles: true, key: "End" }),
      );
    });
    expect(onCommit).toHaveBeenLastCalledWith(MAX_WIDTH);

    act(() => {
      handle.dispatchEvent(
        new KeyboardEvent("keydown", { bubbles: true, key: "Enter" }),
      );
    });
    expect(onReset).toHaveBeenCalledTimes(1);

    act(() => {
      handle.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    });
    expect(onReset).toHaveBeenCalledTimes(2);
  });

  it("已在边界时不产生提交（不会因一次按键退出 auto）", () => {
    const { handle } = render(MIN_WIDTH);

    act(() => {
      handle.dispatchEvent(
        new KeyboardEvent("keydown", { bubbles: true, key: "ArrowRight" }),
      );
    });

    expect(onCommit).not.toHaveBeenCalled();
    expect(onReset).not.toHaveBeenCalled();
  });

  it("轻点（无位移）不提交：避免误把 auto 切成 manual", () => {
    const { handle } = render(400);

    act(() => {
      handle.dispatchEvent(pointerEvent("pointerdown", { clientX: 1000 }));
    });
    act(() => {
      handle.dispatchEvent(pointerEvent("pointerup", { clientX: 1000 }));
    });

    expect(onCommit).not.toHaveBeenCalled();
  });
});
