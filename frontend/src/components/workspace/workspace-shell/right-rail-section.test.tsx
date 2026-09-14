// @vitest-environment jsdom

import { type TFunction } from "i18next";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Thread } from "@/data/mock";
import { i18n } from "@/i18n";

import { WorkspaceRightRailSection } from "./right-rail-section";

// 合并后的右栏只挂载一个 ArtifactPanel（内部含 条目/计划/还原/会话用量 页签）。
// 这里用桩验证：单一面板接线、折叠语义、以及面板抛错时的面板级错误边界。
vi.mock("./lazy-surfaces", () => ({
  ArtifactPanel: ({ sessionId }: { sessionId?: string }) => {
    if (sessionId === "session-boom") {
      throw new Error("artifact panel exploded");
    }
    return <div data-testid="artifact-panel-stub" />;
  },
  ArtifactPanelFallback: () => <div data-testid="artifact-panel-fallback" />,
}));

const thread: Thread = {
  id: "thread-1",
  title: "Review runtime changes",
  summary: "",
  updatedAt: "2026-07-27T00:00:00Z",
  status: "active",
  sessionId: "session-1",
  tags: [],
  prompts: [],
  messages: [],
  artifacts: [],
};

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

describe("WorkspaceRightRailSection（单一合并面板）", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    vi.spyOn(console, "error").mockImplementation(() => {});
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
    vi.restoreAllMocks();
  });

  function renderRightRail(
    overrides: {
      isNewThread?: boolean;
      rightRailOpen?: boolean;
      thread?: Thread;
    } = {},
  ) {
    const t = i18n.getFixedT(null, "workspace") as unknown as TFunction<"workspace">;

    act(() => {
      root.render(
        <WorkspaceRightRailSection
          handleOpenArtifact={vi.fn()}
          isNewThread={overrides.isNewThread ?? false}
          isResponding={false}
          rightRailOpen={overrides.rightRailOpen ?? true}
          selectedArtifactId={null}
          selectedThread={overrides.thread ?? thread}
          t={t}
        />,
      );
    });
  }

  it("展开时只渲染合并后的单一面板（会话用量不再是独立面板）", () => {
    renderRightRail();

    expect(container.querySelector('[data-testid="artifact-panel-stub"]')).not.toBeNull();
    expect(document.querySelector('[data-testid="session-usage-panel"]')).toBeNull();
  });

  it("会话用量折叠入口由顶栏开关统一控制：收起后整列不渲染", () => {
    renderRightRail({ rightRailOpen: false });

    expect(container.firstElementChild).toBeNull();
    expect(document.querySelector('[role="alert"]')).toBeNull();
  });

  it("新建会话不渲染右栏", () => {
    renderRightRail({ isNewThread: true });

    expect(container.firstElementChild).toBeNull();
  });

  it("面板抛错时就地显示面板错误面，右栏容器仍在", () => {
    renderRightRail({
      thread: { ...thread, sessionId: "session-boom" },
    });

    const alert = document.querySelector('[role="alert"]');
    expect(alert).not.toBeNull();
    expect(alert?.getAttribute("data-error-scope")).toBe("panel");
    expect(alert?.textContent).toContain(
      i18n.t("errors.boundary.panelTitle", { ns: "common" }),
    );
    expect(container.firstElementChild).not.toBeNull();
  });
});
