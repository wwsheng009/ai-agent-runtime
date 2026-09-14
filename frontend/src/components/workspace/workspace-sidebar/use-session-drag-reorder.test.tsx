// @vitest-environment jsdom
// P2-6 子片 1：组内拖拽重排的接线测试（落点判定 / 结算 / 边界不落地）。

import { act, useEffect } from "react";
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

import {
  useSidebarSessionDrag,
  type SidebarSessionDragController,
  type SidebarSessionDragGroup,
} from "./use-session-drag-reorder";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

type DragEventLike = Parameters<
  ReturnType<SidebarSessionDragController["dragPropsFor"]>["onDragOver"]
>[0];

type ReorderFn = (accountKey: string, order: readonly string[]) => void;

/** 行高 20、行顶 100 的落点事件：上半 → before，下半 → after。 */
function rowEvent(clientY: number, contains = false) {
  const preventDefault = vi.fn();
  const setData = vi.fn();
  return {
    event: {
      clientY,
      currentTarget: {
        contains: () => contains,
        getBoundingClientRect: () => ({ height: 20, top: 100 }),
      },
      dataTransfer: { dropEffect: "", effectAllowed: "", setData },
      preventDefault,
      relatedTarget: null,
    } as unknown as DragEventLike,
    preventDefault,
    setData,
  };
}

describe("useSidebarSessionDrag", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  let controller: SidebarSessionDragController | null;
  let onReorder: Mock<ReorderFn>;
  const groups: SidebarSessionDragGroup[] = [
    { key: "dir-a", sessions: [{ id: "a" }, { id: "b" }, { id: "c" }] },
    { key: "dir-b", sessions: [{ id: "x" }] },
  ];

  function setController(next: SidebarSessionDragController) {
    controller = next;
  }

  function Probe({
    enabled,
    onReady,
  }: {
    enabled: boolean;
    onReady: (next: SidebarSessionDragController) => void;
  }) {
    const current = useSidebarSessionDrag({
      describeMoved: (accountKey, sessionId) => `${accountKey}:${sessionId}`,
      enabled,
      groups,
      onReorder,
    });
    useEffect(() => {
      onReady(current);
    }, [current, onReady]);
    return null;
  }

  function render(enabled: boolean) {
    act(() => {
      root?.render(<Probe enabled={enabled} onReady={setController} />);
    });
  }

  function propsFor(accountKey: string, sessionId: string) {
    return (controller as unknown as SidebarSessionDragController).dragPropsFor(
      accountKey,
      sessionId,
    );
  }

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    controller = null;
    onReorder = vi.fn<ReorderFn>();
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("手动模式可拖：拖到目标行下半段按 after 结算并播报", () => {
    render(true);
    expect(propsFor("dir-a", "a").dragEnabled).toBe(true);

    act(() => {
      propsFor("dir-a", "a").onDragStart(rowEvent(105).event);
    });
    const over = rowEvent(115);
    act(() => {
      propsFor("dir-a", "c").onDragOver(over.event);
    });
    expect(over.preventDefault).toHaveBeenCalledTimes(1);
    expect(propsFor("dir-a", "c").dropEdge).toBe("after");

    act(() => {
      propsFor("dir-a", "c").onDrop(rowEvent(115).event);
    });

    // a 移到 c 之后：[b, c, a]
    expect(onReorder).toHaveBeenCalledTimes(1);
    expect(onReorder).toHaveBeenCalledWith("dir-a", ["b", "c", "a"]);
    expect(controller?.announcement).toBe("dir-a:a");
    expect(propsFor("dir-a", "c").dropEdge).toBeNull();
  });

  it("上半段落点按 before 结算", () => {
    render(true);
    act(() => {
      propsFor("dir-a", "c").onDragStart(rowEvent(105).event);
    });
    act(() => {
      propsFor("dir-a", "a").onDragOver(rowEvent(105).event);
    });
    expect(propsFor("dir-a", "a").dropEdge).toBe("before");

    act(() => {
      propsFor("dir-a", "a").onDrop(rowEvent(105).event);
    });
    expect(onReorder).toHaveBeenCalledWith("dir-a", ["c", "a", "b"]);
  });

  it("跨组拖拽不接收落点，也不结算（不写假落点）", () => {
    render(true);
    act(() => {
      propsFor("dir-a", "a").onDragStart(rowEvent(105).event);
    });

    const over = rowEvent(115);
    act(() => {
      propsFor("dir-b", "x").onDragOver(over.event);
    });
    expect(over.preventDefault).not.toHaveBeenCalled();
    expect(propsFor("dir-b", "x").dropEdge).toBeNull();

    const drop = rowEvent(115);
    act(() => {
      propsFor("dir-b", "x").onDrop(drop.event);
    });
    expect(drop.preventDefault).not.toHaveBeenCalled();
    expect(onReorder).not.toHaveBeenCalled();
    expect(controller?.announcement).toBe("");
  });

  it("最近更新模式（未启用手动）整行不可拖，也不结算", () => {
    render(false);
    expect(propsFor("dir-a", "a").dragEnabled).toBe(false);

    act(() => {
      propsFor("dir-a", "a").onDragStart(rowEvent(105).event);
    });
    const over = rowEvent(115);
    act(() => {
      propsFor("dir-a", "b").onDragOver(over.event);
    });
    expect(over.preventDefault).not.toHaveBeenCalled();

    act(() => {
      propsFor("dir-a", "b").onDrop(rowEvent(115).event);
    });
    expect(onReorder).not.toHaveBeenCalled();
  });

  it("落点与现状等价时不写账目也不播报", () => {
    render(true);
    act(() => {
      propsFor("dir-a", "b").onDragStart(rowEvent(105).event);
    });
    act(() => {
      propsFor("dir-a", "a").onDragOver(rowEvent(115).event);
    });
    act(() => {
      propsFor("dir-a", "a").onDrop(rowEvent(115).event);
    });

    // [a, b, c] 把 b 放到 a 之后仍是原顺序：等价落点，不发通知。
    expect(onReorder).not.toHaveBeenCalled();
    expect(controller?.announcement).toBe("");
  });

  it("拖拽结束清空拖拽态（不留悬停指示）", () => {
    render(true);
    act(() => {
      propsFor("dir-a", "a").onDragStart(rowEvent(105).event);
    });
    act(() => {
      propsFor("dir-a", "c").onDragOver(rowEvent(115).event);
    });
    expect(propsFor("dir-a", "c").dropEdge).toBe("after");

    act(() => {
      propsFor("dir-a", "a").onDragEnd();
    });
    expect(propsFor("dir-a", "c").dropEdge).toBeNull();

    // 结束后再落点不再结算（拖拽会话已被清空）。
    act(() => {
      propsFor("dir-a", "c").onDrop(rowEvent(115).event);
    });
    expect(onReorder).not.toHaveBeenCalled();
  });

  it("行内子元素间移动不清落点指示，移出整行才清", () => {
    render(true);
    act(() => {
      propsFor("dir-a", "a").onDragStart(rowEvent(105).event);
    });
    act(() => {
      propsFor("dir-a", "c").onDragOver(rowEvent(115).event);
    });

    act(() => {
      propsFor("dir-a", "c").onDragLeave(rowEvent(115, true).event);
    });
    expect(propsFor("dir-a", "c").dropEdge).toBe("after");

    act(() => {
      propsFor("dir-a", "c").onDragLeave(rowEvent(115, false).event);
    });
    expect(propsFor("dir-a", "c").dropEdge).toBeNull();
  });
});
