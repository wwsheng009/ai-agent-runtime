// @vitest-environment jsdom
//
// P0-1：右侧栏页签栏的 a11y 契约（roving tabindex + 方向键 + 禁用面不可达）。
// 这里只测页签栏本身；面板体内各面由各自测试覆盖。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  WORKSPACE_PANEL_SURFACES,
  buildSurfaceTabIds,
  type WorkspacePanelSurfaceId,
} from "@/components/workspace/panel-registry";

import { ArtifactPanelSurfaceTabs } from "./surface-tabs";

const BASE_ID = "panel-test";
const TAB_IDS = Object.fromEntries(
  WORKSPACE_PANEL_SURFACES.map((spec) => [spec.id, buildSurfaceTabIds(BASE_ID, spec.id)]),
) as Record<WorkspacePanelSurfaceId, ReturnType<typeof buildSurfaceTabIds>>;

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

describe("ArtifactPanelSurfaceTabs", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = null;
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => {
        root?.unmount();
      });
    }
    container.remove();
  });

  function render(props: {
    activeSurface?: WorkspacePanelSurfaceId;
    disabledReasons?: Partial<Record<WorkspacePanelSurfaceId, string>>;
    onSelectSurface?: (surface: WorkspacePanelSurfaceId) => void;
  }) {
    const onSelectSurface = props.onSelectSurface ?? vi.fn();
    root = createRoot(container);
    act(() => {
      root?.render(
        <ArtifactPanelSurfaceTabs
          activeSurface={props.activeSurface ?? "artifacts"}
          disabledReasons={props.disabledReasons}
          onSelectSurface={onSelectSurface}
          tabIds={TAB_IDS}
          titleId={`${BASE_ID}-title`}
        />,
      );
    });
    return { onSelectSurface };
  }

  function tab(surfaceId: WorkspacePanelSurfaceId) {
    return container.querySelector<HTMLButtonElement>(`[data-testid="artifact-panel-tab-${surfaceId}"]`);
  }

  it("每个注册面都有一个页签，且与面板 id 正确配对", () => {
    render({});

    const tabs = container.querySelectorAll('[role="tab"]');
    expect(tabs).toHaveLength(WORKSPACE_PANEL_SURFACES.length);
    expect(container.querySelector('[role="tablist"]')).not.toBeNull();

    for (const spec of WORKSPACE_PANEL_SURFACES) {
      const node = tab(spec.id);
      expect(node).not.toBeNull();
      expect(node?.getAttribute("aria-controls")).toBe(TAB_IDS[spec.id].panelId);
      expect(node?.id).toBe(TAB_IDS[spec.id].tabId);
      expect(node?.textContent?.trim().length).toBeGreaterThan(0);
    }
  });

  it("激活面 tabIndex=0，其余为 -1（roving tabindex）", () => {
    render({ activeSurface: "files" });

    expect(tab("files")?.getAttribute("aria-selected")).toBe("true");
    expect(tab("files")?.tabIndex).toBe(0);
    for (const spec of WORKSPACE_PANEL_SURFACES) {
      if (spec.id !== "files") {
        expect(tab(spec.id)?.tabIndex).toBe(-1);
      }
    }
  });

  it("无会话原因命中时页签禁用并给出可解释提示，点击不触发选择", () => {
    const onSelectSurface = vi.fn();
    render({
      disabledReasons: { plan: "需要先选择会话", checkpoints: "需要先选择会话" },
      onSelectSurface,
    });

    for (const id of ["plan", "checkpoints"] as const) {
      const node = tab(id);
      expect(node?.disabled).toBe(true);
      expect(node?.getAttribute("title")).toBe("需要先选择会话");
      act(() => {
        node?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      });
    }
    expect(onSelectSurface).not.toHaveBeenCalled();
    expect(tab("artifacts")?.disabled).toBe(false);
  });

  it("方向键在可用面之间循环移动并同步焦点", () => {
    const onSelectSurface = vi.fn();
    render({ activeSurface: "artifacts", onSelectSurface });
    tab("artifacts")?.focus();

    act(() => {
      tab("artifacts")?.dispatchEvent(
        new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true, cancelable: true }),
      );
    });
    expect(onSelectSurface).toHaveBeenLastCalledWith("plan");
    expect(document.activeElement).toBe(tab("plan"));

    act(() => {
      tab("plan")?.dispatchEvent(
        new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true, cancelable: true }),
      );
    });
    expect(onSelectSurface).toHaveBeenLastCalledWith("artifacts");
    expect(document.activeElement).toBe(tab("artifacts"));
  });

  it("Home/End 跳到第一个/最后一个面；无匹配按键不拦截", () => {
    const onSelectSurface = vi.fn();
    render({ activeSurface: "plan", onSelectSurface });

    act(() => {
      tab("plan")?.dispatchEvent(
        new KeyboardEvent("keydown", { key: "End", bubbles: true, cancelable: true }),
      );
    });
    const last = WORKSPACE_PANEL_SURFACES[WORKSPACE_PANEL_SURFACES.length - 1];
    expect(onSelectSurface).toHaveBeenLastCalledWith(last?.id);

    act(() => {
      tab(last!.id)?.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Home", bubbles: true, cancelable: true }),
      );
    });
    expect(onSelectSurface).toHaveBeenLastCalledWith(WORKSPACE_PANEL_SURFACES[0]?.id);

    act(() => {
      tab("artifacts")?.dispatchEvent(
        new KeyboardEvent("keydown", { key: "a", bubbles: true, cancelable: true }),
      );
    });
    expect(onSelectSurface).toHaveBeenCalledTimes(2);
  });

  it("禁用面被方向键跳过（不会把焦点移到 disabled 页签）", () => {
    const onSelectSurface = vi.fn();
    render({
      activeSurface: "artifacts",
      disabledReasons: { plan: "需要先选择会话" },
      onSelectSurface,
    });

    act(() => {
      tab("artifacts")?.dispatchEvent(
        new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true, cancelable: true }),
      );
    });

    expect(onSelectSurface).toHaveBeenLastCalledWith("checkpoints");
    expect(document.activeElement).toBe(tab("checkpoints"));
  });
});
