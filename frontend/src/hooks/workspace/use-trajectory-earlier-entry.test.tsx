// @vitest-environment jsdom

// 「加载更早」入口的核心契约是**引用稳定**：window 状态不变时下游不应收到新 prop，
// 否则渲染链上每次 render 都会认为入口变化（见 use-trajectory-earlier-entry.ts）。

import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import {
  useTrajectoryEarlierEntry,
  type TrajectoryEarlierSource,
} from "./use-trajectory-earlier-entry";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

type EarlierEntry = ReturnType<typeof useTrajectoryEarlierEntry>;

describe("useTrajectoryEarlierEntry", () => {
  let container: HTMLDivElement;
  let root: Root;
  let entryRef: { current: EarlierEntry | null };

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    entryRef = { current: null };
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function Harness(source: TrajectoryEarlierSource) {
    const entry = useTrajectoryEarlierEntry(source);
    useEffect(() => {
      entryRef.current = entry;
    }, [entry]);
    return null;
  }

  it("窗口状态不变时保持同一入口对象，回调变化时重建", () => {
    const load = () => {};
    act(() => {
      root.render(<Harness window={{ hasMore: true }} loadingEarlier={false} loadEarlier={load} />);
    });
    const initial = entryRef.current;
    expect(initial).toEqual({ hasEarlier: true, loading: false, onLoad: load });

    // 同值（不同字面量）重渲染：依赖是基本值 + 原回调，引用必须保持。
    act(() => {
      root.render(<Harness window={{ hasMore: true }} loadingEarlier={false} loadEarlier={load} />);
    });
    expect(entryRef.current).toBe(initial);

    act(() => {
      root.render(<Harness window={{ hasMore: false }} loadingEarlier loadEarlier={load} />);
    });
    expect(entryRef.current).not.toBe(initial);
    expect(entryRef.current).toEqual({ hasEarlier: false, loading: true, onLoad: load });
  });
});
