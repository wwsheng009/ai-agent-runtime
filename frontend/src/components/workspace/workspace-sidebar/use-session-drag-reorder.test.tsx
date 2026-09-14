// @vitest-environment jsdom
// P2-6 子片 1/2：组内拖拽重排 + 跨组移动的接线测试
// （落点判定 / 结算 / 边界不落地 / 跨组落点与不可归属目标）。

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
  type SidebarSessionMoveTarget,
} from "./use-session-drag-reorder";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

type DragEventLike = Parameters<
  ReturnType<SidebarSessionDragController["dragPropsFor"]>["onDragOver"]
>[0];

type ReorderFn = (accountKey: string, order: readonly string[]) => void;
type MoveFn = (sessionId: string, target: SidebarSessionMoveTarget) => void;

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

/** 目录组头落点事件（无行几何：组头落点恒为「追加到末尾」）。 */
function groupEvent(contains = false) {
  const preventDefault = vi.fn();
  return {
    event: {
      currentTarget: { contains: () => contains },
      dataTransfer: { dropEffect: "", effectAllowed: "" },
      preventDefault,
      relatedTarget: null,
    } as unknown as DragEventLike,
    preventDefault,
  };
}

describe("useSidebarSessionDrag", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  let controller: SidebarSessionDragController | null;
  let onReorder: Mock<ReorderFn>;
  let onMoveSession: Mock<MoveFn>;
  const groups: SidebarSessionDragGroup[] = [
    { key: "dir-a", sessions: [{ id: "a" }, { id: "b" }, { id: "c" }] },
    { key: "dir-b", sessions: [{ id: "x" }] },
    { key: "dir-empty", sessions: [] },
  ];

  function setController(next: SidebarSessionDragController) {
    controller = next;
  }

  function Probe({
    canMoveToGroup,
    crossGroupEnabled = false,
    enabled,
    onReady,
  }: {
    canMoveToGroup?: (groupKey: string) => boolean;
    crossGroupEnabled?: boolean;
    enabled: boolean;
    onReady: (next: SidebarSessionDragController) => void;
  }) {
    const current = useSidebarSessionDrag({
      canMoveToGroup,
      crossGroupEnabled,
      describeMoved: (accountKey, sessionId) => `${accountKey}:${sessionId}`,
      describeMovedToGroup: (sessionId, groupKey) =>
        `moved:${sessionId}->${groupKey}`,
      enabled,
      groups,
      onMoveSession,
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

  function renderCrossGroup(
    enabled: boolean,
    options: {
      canMoveToGroup?: (groupKey: string) => boolean;
      crossGroupEnabled?: boolean;
    } = {},
  ) {
    act(() => {
      root?.render(
        <Probe
          canMoveToGroup={options.canMoveToGroup ?? (() => true)}
          crossGroupEnabled={options.crossGroupEnabled ?? true}
          enabled={enabled}
          onReady={setController}
        />,
      );
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
    onMoveSession = vi.fn<MoveFn>();
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

  it("跨组落点：目标组账目按末尾结算，并发出移动意图与跨组播报", () => {
    renderCrossGroup(true);
    act(() => {
      propsFor("dir-a", "a").onDragStart(rowEvent(105).event);
    });

    const over = groupEvent();
    act(() => {
      controller?.groupDropPropsFor("dir-b").onDragOver(over.event);
    });
    expect(over.preventDefault).toHaveBeenCalledTimes(1);
    expect(controller?.groupDropPropsFor("dir-b").dropActive).toBe(true);

    const drop = groupEvent();
    act(() => {
      controller?.groupDropPropsFor("dir-b").onDrop(drop.event);
    });

    expect(drop.preventDefault).toHaveBeenCalledTimes(1);
    // 目标组顺序先落账目（x 之后），移动意图再交回接线层做乐观归属与写回。
    expect(onReorder).toHaveBeenCalledTimes(1);
    expect(onReorder).toHaveBeenCalledWith("dir-b", ["x", "a"]);
    expect(onMoveSession).toHaveBeenCalledTimes(1);
    expect(onMoveSession).toHaveBeenCalledWith("a", {
      edge: "after",
      groupKey: "dir-b",
      sessionId: "x",
    });
    expect(controller?.announcement).toBe("moved:a->dir-b");
    expect(controller?.groupDropPropsFor("dir-b").dropActive).toBe(false);
  });

  it("目标不可归属时组头不接管落点（不写假落点、不发移动意图）", () => {
    renderCrossGroup(true, { canMoveToGroup: (groupKey) => groupKey !== "dir-b" });
    act(() => {
      propsFor("dir-a", "a").onDragStart(rowEvent(105).event);
    });

    const over = groupEvent();
    act(() => {
      controller?.groupDropPropsFor("dir-b").onDragOver(over.event);
    });
    expect(over.preventDefault).not.toHaveBeenCalled();
    expect(controller?.groupDropPropsFor("dir-b").dropActive).toBe(false);

    const drop = groupEvent();
    act(() => {
      controller?.groupDropPropsFor("dir-b").onDrop(drop.event);
    });
    expect(drop.preventDefault).not.toHaveBeenCalled();
    expect(onReorder).not.toHaveBeenCalled();
    expect(onMoveSession).not.toHaveBeenCalled();
    expect(controller?.announcement).toBe("");
  });

  it("平铺视图（跨组能力关闭）不接收组头落点", () => {
    renderCrossGroup(true, { crossGroupEnabled: false });
    act(() => {
      propsFor("dir-a", "a").onDragStart(rowEvent(105).event);
    });

    const over = groupEvent();
    act(() => {
      controller?.groupDropPropsFor("dir-b").onDragOver(over.event);
    });
    expect(over.preventDefault).not.toHaveBeenCalled();

    const drop = groupEvent();
    act(() => {
      controller?.groupDropPropsFor("dir-b").onDrop(drop.event);
    });
    expect(onMoveSession).not.toHaveBeenCalled();
  });

  it("源组自身的组头不作跨组落点（组内重排只认行落点）", () => {
    renderCrossGroup(true);
    act(() => {
      propsFor("dir-a", "a").onDragStart(rowEvent(105).event);
    });

    const drop = groupEvent();
    act(() => {
      controller?.groupDropPropsFor("dir-a").onDrop(drop.event);
    });
    expect(drop.preventDefault).not.toHaveBeenCalled();
    expect(onReorder).not.toHaveBeenCalled();
    expect(onMoveSession).not.toHaveBeenCalled();
  });

  it("目标组内无会话时不结算组头落点（不凭空造位置）", () => {
    renderCrossGroup(true);
    act(() => {
      propsFor("dir-a", "a").onDragStart(rowEvent(105).event);
    });

    const drop = groupEvent();
    act(() => {
      controller?.groupDropPropsFor("dir-empty").onDrop(drop.event);
    });
    expect(drop.preventDefault).not.toHaveBeenCalled();
    expect(onReorder).not.toHaveBeenCalled();
    expect(onMoveSession).not.toHaveBeenCalled();
  });
});
