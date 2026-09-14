// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { type Artifact } from "@/data/mock";

import { ArtifactPanel } from "./artifact-panel";

// 会话用量页签只验证接线，面板本体由 session-usage-panel.test.tsx 覆盖。
vi.mock("@/components/workspace/session-usage-panel", () => ({
  SessionUsagePanel: ({ sessionId }: { sessionId: string }) => (
    <div data-testid="session-usage-panel-stub">usage:{sessionId}</div>
  ),
}));

// 附着会话后计划/还原/回溯审计会各自拉一次数据；这里静默返回空态，避免测试触发真实网络。
vi.mock("@/lib/runtime-api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/runtime-api")>();
  return {
    ...actual,
    getSessionPlanMode: vi.fn().mockResolvedValue({
      session_id: "session-usage-1",
      active: false,
      status: "off",
      permission_mode: "default",
      plan_content: "",
      plan_content_available: false,
    }),
    listSessionBacktrackAudit: vi.fn().mockResolvedValue({ entries: [] }),
    listSessionCheckpoints: vi.fn().mockResolvedValue({ checkpoints: [] }),
  };
});

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

describe("ArtifactPanel", () => {
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

  function renderArtifactPanel(
    artifacts: Artifact[],
    options?: {
      onOpenArtifact?: (artifactId: string) => void;
      selectedArtifactId?: string | null;
      sessionId?: string;
    },
  ) {
    root = createRoot(container);

    act(() => {
      root?.render(
        <ArtifactPanel
          artifacts={artifacts}
          onOpenArtifact={options?.onOpenArtifact ?? (() => {})}
          selectedArtifactId={options?.selectedArtifactId ?? null}
          sessionId={options?.sessionId}
        />,
      );
    });
  }

  it("renders a compact artifact rail without inline detail copy", () => {
    const artifacts: Artifact[] = [
      {
        id: "artifact-1",
        name: "runtime-summary.json",
        path: "runtime/runtime-summary.json",
        summary: "Structured runtime evidence snapshot",
        kind: "json",
        language: "json",
        content: '{"ok":true}',
      },
    ];

    renderArtifactPanel(artifacts, {
      selectedArtifactId: "artifact-1",
    });

    expect(container.textContent).toContain("文件产物");
    expect(container.textContent).toContain("runtime-summary.json");
    expect(container.textContent).not.toContain("Thin artifact rail");
    expect(container.textContent).not.toContain("打开详情对话框");
    expect(container.textContent).not.toContain("last event:");
    expect(container.textContent).not.toContain("session ");
    expect(container.textContent).not.toContain("Source reader");
  });

  it("opens artifacts through the callback when an item is clicked", () => {
    const onOpenArtifact = vi.fn();
    const artifacts: Artifact[] = [
      {
        id: "artifact-2",
        name: "preview-report.html",
        path: "outputs/preview-report.html",
        summary: "Generated preview output",
        kind: "html",
        language: "html",
        content: "<html><body>preview</body></html>",
        previewHtml: "<html><body>preview</body></html>",
      },
    ];

    renderArtifactPanel(artifacts, {
      onOpenArtifact,
      selectedArtifactId: "artifact-2",
    });

    const artifactButton = Array.from(container.querySelectorAll("button")).find((button) =>
      button.textContent?.includes("preview-report.html"),
    );

    expect(artifactButton).toBeInstanceOf(HTMLButtonElement);

    act(() => {
      artifactButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(onOpenArtifact).toHaveBeenCalledWith("artifact-2");
  });

  it("exposes the surface switcher as tabs", () => {
    const artifacts: Artifact[] = [
      {
        id: "artifact-3",
        name: "runtime-log.json",
        path: "runtime/runtime-log.json",
        summary: "Structured runtime evidence log",
        kind: "json",
        language: "json",
        content: '{"ok":true}',
      },
    ];

    renderArtifactPanel(artifacts, {
      selectedArtifactId: "artifact-3",
    });

    const evidenceTab = Array.from(container.querySelectorAll('[role="tab"]')).find(
      (button) => button.textContent?.includes("条目"),
    );
    const planTab = Array.from(container.querySelectorAll('[role="tab"]')).find(
      (button) => button.textContent?.includes("计划"),
    );
    const restorePointsTab = Array.from(container.querySelectorAll('[role="tab"]')).find(
      (button) => button.textContent?.includes("还原"),
    );
    const usageTab = Array.from(container.querySelectorAll('[role="tab"]')).find(
      (button) => button.textContent?.includes("会话用量"),
    );

    expect(container.querySelector('[role="tablist"]')).toBeInstanceOf(HTMLElement);
    expect(evidenceTab?.getAttribute("aria-selected")).toBe("true");
    expect(planTab).toBeInstanceOf(HTMLButtonElement);
    expect((planTab as HTMLButtonElement).disabled).toBe(true);
    expect(restorePointsTab).toBeInstanceOf(HTMLButtonElement);
    expect((restorePointsTab as HTMLButtonElement).disabled).toBe(true);
    expect(usageTab).toBeInstanceOf(HTMLButtonElement);
    expect((usageTab as HTMLButtonElement).disabled).toBe(true);
  });

  it("在合并面板内以页签呈现会话用量，而不是独立面板", async () => {
    const artifacts: Artifact[] = [];

    renderArtifactPanel(artifacts, { sessionId: "session-usage-1" });
    await act(async () => {
      await Promise.resolve();
    });

    const usageTab = container.querySelector<HTMLButtonElement>(
      '[data-testid="artifact-panel-tab-usage"]',
    );
    expect(usageTab).toBeInstanceOf(HTMLButtonElement);
    expect(usageTab?.disabled).toBe(false);
    expect(usageTab?.getAttribute("aria-selected")).toBe("false");
    // 未选中页签时不挂载用量面板，避免不必要的用量请求。
    expect(
      container.querySelector('[data-testid="session-usage-panel-stub"]'),
    ).toBeNull();

    act(() => {
      usageTab?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(usageTab?.getAttribute("aria-selected")).toBe("true");
    expect(
      container.querySelector('[data-testid="session-usage-panel-stub"]')
        ?.textContent,
    ).toBe("usage:session-usage-1");
  });
});
