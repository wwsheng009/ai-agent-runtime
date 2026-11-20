// @vitest-environment jsdom
//
// P0-3：右侧栏页签栏 = 3 个一等公民 + 更多菜单。
// 这里守住：单行常驻页签、roving tabindex、更多菜单分组可达与禁用理由。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  WORKSPACE_PANEL_PRIMARY_SURFACE_IDS,
  WORKSPACE_PANEL_SECONDARY_GROUPS,
  WORKSPACE_PANEL_SURFACES,
  buildSurfaceTabIds,
  type WorkspacePanelSurfaceId,
} from "@/components/workspace/panel-registry";

import { ArtifactPanelSurfaceTabs } from "./surface-tabs";

const BASE_ID = "panel-test";
const TAB_IDS = Object.fromEntries(
  WORKSPACE_PANEL_SURFACES.map((spec) => [spec.id, buildSurfaceTabIds(BASE_ID, spec.id)]),
) as Record<WorkspacePanelSurfaceId, ReturnType<typeof buildSurfaceTabIds>>;

const SECONDARY_IDS = WORKSPACE_PANEL_SECONDARY_GROUPS.flatMap(
  (group) => group.surfaceIds,
) as WorkspacePanelSurfaceId[];

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
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
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
    return container.querySelector<HTMLButtonElement>(
      `[data-testid="artifact-panel-tab-${surfaceId}"]`,
    );
  }

  function moreItem(surfaceId: WorkspacePanelSurfaceId) {
    return container.querySelector<HTMLButtonElement>(
      `[data-testid="artifact-panel-more-item-${surfaceId}"]`,
    );
  }

  function openMore() {
    const trigger = container.querySelector<HTMLButtonElement>(
      '[data-testid="artifact-panel-more-trigger"]',
    );
    expect(trigger).not.toBeNull();
    act(() => {
      trigger?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    return trigger;
  }

  it("常驻页签只保留 3 个一等公民，其余 6 项在更多菜单可达", () => {
    render({});

    const tabs = container.querySelectorAll('[role="tab"]');
    expect(tabs).toHaveLength(WORKSPACE_PANEL_PRIMARY_SURFACE_IDS.length);
    expect(container.querySelector('[role="tablist"]')).not.toBeNull();

    for (const id of WORKSPACE_PANEL_PRIMARY_SURFACE_IDS) {
      const node = tab(id);
      expect(node).not.toBeNull();
      expect(node?.getAttribute("aria-controls")).toBe(TAB_IDS[id].panelId);
      expect(node?.id).toBe(TAB_IDS[id].tabId);
      expect(node?.textContent?.trim().length).toBeGreaterThan(0);
    }

    openMore();
    for (const id of SECONDARY_IDS) {
      expect(moreItem(id)).not.toBeNull();
    }
    expect(SECONDARY_IDS).toHaveLength(WORKSPACE_PANEL_SURFACES.length - 3);
  });

  it("保留常驻页签层级，并让更多菜单项使用界面字号 token", () => {
    render({});

    expect(tab("artifacts")?.classList.contains("text-base")).toBe(true);

    openMore();
    expect(moreItem("plan")?.classList.contains("app-text-12")).toBe(true);
    expect(moreItem("plan")?.classList.contains("text-xs")).toBe(false);
  });

  it("激活面 tabIndex=0，其余常驻页签为 -1（roving tabindex）", () => {
    render({ activeSurface: "files" });

    expect(tab("files")?.getAttribute("aria-selected")).toBe("true");
    expect(tab("files")?.tabIndex).toBe(0);
    expect(tab("artifacts")?.tabIndex).toBe(-1);
    expect(tab("git")?.tabIndex).toBe(-1);
  });

  it("无会话原因命中时更多菜单项禁用并给出可解释提示，点击不触发选择", () => {
    const onSelectSurface = vi.fn();
    render({
      disabledReasons: { plan: "需要先选择会话", checkpoints: "需要先选择会话" },
      onSelectSurface,
    });

    openMore();
    for (const id of ["plan", "checkpoints"] as const) {
      const node = moreItem(id);
      expect(node?.disabled).toBe(true);
      expect(node?.getAttribute("title")).toBe("需要先选择会话");
      act(() => {
        node?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      });
    }
    expect(onSelectSurface).not.toHaveBeenCalled();
    expect(tab("artifacts")?.disabled).toBe(false);
  });

  it("方向键在常驻页签之间循环移动并同步焦点", () => {
    const onSelectSurface = vi.fn();
    render({ activeSurface: "artifacts", onSelectSurface });
    tab("artifacts")?.focus();

    act(() => {
      tab("artifacts")?.dispatchEvent(
        new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true, cancelable: true }),
      );
    });
    expect(onSelectSurface).toHaveBeenLastCalledWith("files");
    expect(document.activeElement).toBe(tab("files"));

    act(() => {
      tab("files")?.dispatchEvent(
        new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true, cancelable: true }),
      );
    });
    expect(onSelectSurface).toHaveBeenLastCalledWith("artifacts");
    expect(document.activeElement).toBe(tab("artifacts"));
  });

  it("Home/End 跳到第一个/最后一个常驻页签；无匹配按键不拦截", () => {
    const onSelectSurface = vi.fn();
    render({ activeSurface: "files", onSelectSurface });

    act(() => {
      tab("files")?.dispatchEvent(
        new KeyboardEvent("keydown", { key: "End", bubbles: true, cancelable: true }),
      );
    });
    expect(onSelectSurface).toHaveBeenLastCalledWith("git");

    act(() => {
      tab("git")?.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Home", bubbles: true, cancelable: true }),
      );
    });
    expect(onSelectSurface).toHaveBeenLastCalledWith("artifacts");

    act(() => {
      tab("artifacts")?.dispatchEvent(
        new KeyboardEvent("keydown", { key: "a", bubbles: true, cancelable: true }),
      );
    });
    expect(onSelectSurface).toHaveBeenCalledTimes(2);
  });

  it("禁用的常驻页签被方向键跳过", () => {
    const onSelectSurface = vi.fn();
    render({
      activeSurface: "artifacts",
      disabledReasons: { files: "不可用" },
      onSelectSurface,
    });

    act(() => {
      tab("artifacts")?.dispatchEvent(
        new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true, cancelable: true }),
      );
    });

    expect(onSelectSurface).toHaveBeenLastCalledWith("git");
    expect(document.activeElement).toBe(tab("git"));
  });

  it("激活的次级面在触发按钮上显示名称，菜单内标记为当前项", () => {
    render({ activeSurface: "plan" });

    const trigger = container.querySelector<HTMLButtonElement>(
      '[data-testid="artifact-panel-more-trigger"]',
    );
    expect(trigger?.textContent).toContain("计划");
    expect(trigger?.getAttribute("aria-expanded")).toBe("false");

    openMore();
    const planItem = moreItem("plan");
    expect(planItem?.getAttribute("aria-current")).toBe("true");
  });
});