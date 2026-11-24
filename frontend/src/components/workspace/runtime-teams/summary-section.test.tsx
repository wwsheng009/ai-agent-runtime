import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string) => key,
  }),
}));

import { TeamsSummarySection } from "./summary-section";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

describe("TeamsSummarySection capability copy", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function render(capabilities: Record<string, string>) {
    act(() => {
      root.render(
        createElement(TeamsSummarySection, {
          activeTeamCount: 0,
          activeRunCount: 0,
          activeView: "teams",
          capabilities,
          error: null,
          isLoading: false,
          runCount: 0,
          selectedTeam: null,
          setActiveView: () => undefined,
          teams: [],
        }),
      );
    });
  }

  it("says draft editing is available when Runtime advertises it", () => {
    render({ run_draft_create: "supported" });

    expect(container.textContent).toContain(
      "panels.teamsPanels.summary.studioDraftSupported",
    );
    expect(container.textContent).not.toContain(
      "panels.teamsPanels.summary.studioDraftUnsupported",
    );
  });

  it("says draft editing is unavailable when Runtime rejects the capability", () => {
    render({ run_draft_create: "unsupported" });

    expect(container.textContent).toContain(
      "panels.teamsPanels.summary.studioDraftUnsupported",
    );
    expect(container.textContent).not.toContain(
      "panels.teamsPanels.summary.studioDraftSupported",
    );
  });

  it("does not assume draft editing support when Runtime omits the capability", () => {
    render({});

    expect(container.textContent).toContain(
      "panels.teamsPanels.summary.studioDraftUnknown",
    );
    expect(container.textContent).not.toContain(
      "panels.teamsPanels.summary.studioDraftUnsupported",
    );
  });
});
