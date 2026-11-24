import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, values?: Record<string, unknown>) =>
      values ? `${key} ${Object.values(values).join(" ")}` : key,
  }),
}));

import type { TeamDetailsState } from "@/components/workspace/runtime-teams/shared";

import { RuntimeTeamResultsSection } from "./results-section";

describe("RuntimeTeamResultsSection", () => {
  it("shows current task outcome fields while keeping opaque refs non-downloadable", () => {
    const task = {
      id: "task-1",
      title: "Implement contract",
      status: "done",
      summary: "Contract tests pass.",
      attempt: 2,
      retry_count: 1,
      result_ref: "artifact:task-1",
    };
    const details: TeamDetailsState = {
      events: [],
      eventCursor: 0,
      latestEventSeq: 0,
      finalSummary: "",
      summaryState: "unknown",
      graph: null,
      mailbox: [],
      pathClaims: [],
      tasks: [task],
      teammates: [],
      preflight: null,
      isStale: false,
    };

    const markup = renderToStaticMarkup(
      <RuntimeTeamResultsSection
        details={details}
        onToggle={() => undefined}
        open
        visibleTasks={details.tasks}
      />,
    );

    expect(markup).toContain("Implement contract");
    expect(markup).toContain("Contract tests pass.");
    expect(markup).toContain("panels.teamsPanels.details.results.attempt 2");
    expect(markup).toContain("panels.teamsPanels.details.results.retries 1");
    expect(markup).toContain("artifact:task-1");
    expect(markup).toContain(
      "panels.teamsPanels.details.results.opaqueReference",
    );
    expect(markup).not.toContain("<a ");
  });
});
