// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { type Artifact } from "@/data/mock";

import { ArtifactPanel } from "./artifact-panel";

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
    },
  ) {
    root = createRoot(container);

    act(() => {
      root?.render(
        <ArtifactPanel
          artifacts={artifacts}
          onOpenArtifact={options?.onOpenArtifact ?? (() => {})}
          selectedArtifactId={options?.selectedArtifactId ?? null}
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

    expect(container.textContent).toContain("Artifacts");
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
      (button) => button.textContent?.includes("Items"),
    );
    const restorePointsTab = Array.from(container.querySelectorAll('[role="tab"]')).find(
      (button) => button.textContent?.includes("Restore"),
    );

    expect(container.querySelector('[role="tablist"]')).toBeInstanceOf(HTMLElement);
    expect(evidenceTab?.getAttribute("aria-selected")).toBe("true");
    expect(restorePointsTab).toBeInstanceOf(HTMLButtonElement);
    expect((restorePointsTab as HTMLButtonElement).disabled).toBe(true);
  });
});
