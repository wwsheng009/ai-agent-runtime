// @vitest-environment jsdom

import { type TFunction } from "i18next";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Thread } from "@/data/mock";
import { i18n } from "@/i18n";

import { WorkspaceRightRailSection } from "./right-rail-section";

// 面板级边界接线：会话用量面板抛错时，右栏应就地显示面板错误面，
// 而不是把整棵工作台组件树一起带崩。
vi.mock("@/components/workspace/session-usage-panel", () => ({
  SessionUsagePanel: (): never => {
    throw new Error("usage panel exploded");
  },
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

describe("WorkspaceRightRailSection（面板级错误边界接线）", () => {
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

  it("面板抛错时就地显示面板错误面，右栏容器仍在", () => {
    const t = i18n.getFixedT(null, "workspace") as unknown as TFunction<"workspace">;

    act(() => {
      root.render(
        <WorkspaceRightRailSection
          artifactRailOpen={false}
          handleOpenArtifact={vi.fn()}
          isNewThread={false}
          isResponding={false}
          selectedArtifactId={null}
          selectedThread={thread}
          t={t}
        />,
      );
    });

    const alert = document.querySelector('[role="alert"]');
    expect(alert).not.toBeNull();
    expect(alert?.getAttribute("data-error-scope")).toBe("panel");
    expect(alert?.textContent).toContain(i18n.t("usagePanel.title", { ns: "workspace" }));
    expect(container.firstElementChild).not.toBeNull();
  });
});
