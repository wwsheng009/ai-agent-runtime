// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { SettingsProvider } from "@/core/settings";
import { type Artifact } from "@/data/mock";

import { ArtifactDetailDialog } from "./artifact-detail-dialog";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function dispatchKeyDown(target: EventTarget, key: string) {
  act(() => {
    target.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key }));
  });
}

describe("ArtifactDetailDialog", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = null;
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;

    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      value: () => ({
        addEventListener() {},
        dispatchEvent() {
          return false;
        },
        matches: true,
        media: "(prefers-color-scheme: dark)",
        onchange: null,
        removeEventListener() {},
      }),
      writable: true,
    });
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

  function renderDialog(artifact: Artifact, open = true) {
    root = createRoot(container);

    act(() => {
      root?.render(
        <SettingsProvider>
          <ArtifactDetailDialog artifact={artifact} onClose={() => {}} open={open} />
        </SettingsProvider>,
      );
    });
  }

  it("renders source-only artifacts in a dialog", () => {
    const artifact: Artifact = {
      id: "artifact-1",
      name: "runtime-events.json",
      path: "runtime/runtime-events.json",
      summary: "Structured runtime evidence snapshot",
      kind: "json",
      language: "json",
      content: '{"events":[]}',
    };

    renderDialog(artifact);

    expect(document.body.textContent).toContain("Runtime evidence");
    expect(document.body.textContent).toContain("runtime-events.json");
    expect(document.body.textContent).toContain("Source reader");
    expect(document.body.querySelector('[data-artifact-detail-dialog="true"]')).toBeInstanceOf(
      HTMLElement,
    );
  });

  it("switches preview artifacts between preview and source tabs", () => {
    const artifact: Artifact = {
      id: "artifact-2",
      name: "preview-report.html",
      path: "outputs/preview-report.html",
      summary: "Generated preview output",
      kind: "html",
      language: "html",
      content: "<html><body>preview</body></html>",
      previewHtml: "<html><body>preview</body></html>",
    };

    renderDialog(artifact);

    const previewTab = Array.from(document.body.querySelectorAll('[role="tab"]')).find(
      (button) => button.textContent?.includes("Preview"),
    );
    const sourceTab = Array.from(document.body.querySelectorAll('[role="tab"]')).find(
      (button) => button.textContent?.includes("Source"),
    );

    expect(previewTab).toBeInstanceOf(HTMLButtonElement);
    expect(sourceTab).toBeInstanceOf(HTMLButtonElement);
    expect(previewTab?.getAttribute("aria-selected")).toBe("true");

    dispatchKeyDown(previewTab as HTMLButtonElement, "ArrowRight");

    expect(sourceTab?.getAttribute("aria-selected")).toBe("true");
    const sourcePanel = Array.from(document.body.querySelectorAll('[role="tabpanel"]')).find(
      (panel) => panel.textContent?.includes("Source reader"),
    );

    expect(sourcePanel).toBeInstanceOf(HTMLElement);
    expect((sourcePanel as HTMLElement).hidden).toBe(false);
  });
});
