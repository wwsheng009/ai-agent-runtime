import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, values?: Record<string, unknown>) =>
      values ? `${key} ${Object.values(values).join(" ")}` : key,
  }),
}));

import type { TeamDetailsState } from "@/components/workspace/runtime-teams/shared";

import { RuntimeTeamPreflightSection } from "./preflight-section";

describe("RuntimeTeamPreflightSection", () => {
  it("renders a preflight report when the optional route preview list is omitted", () => {
    const details: TeamDetailsState = {
      events: [],
      eventCursor: 0,
      latestEventSeq: 0,
      finalSummary: "",
      summaryState: "unknown",
      graph: null,
      mailbox: [],
      pathClaims: [],
      tasks: [],
      teammates: [],
      preflight: {
        team_id: "team-1",
        readiness: false,
        preflight: {
          team_id: "team-1",
          team_exists: true,
          ready: false,
          blocker_count: 1,
          warning_count: 0,
          info_count: 0,
          findings: [
            {
              severity: "blocker",
              code: "team_tasks_empty",
              message: "The Team has no tasks to execute.",
              task_id: "task-1",
              depends_on_id: "missing-task",
              path: "src/output",
              remediation: "Add a task and create the missing dependency.",
            },
          ],
        },
      },
      isStale: false,
    };

    const markup = renderToStaticMarkup(
      <RuntimeTeamPreflightSection
        details={details}
        onToggle={() => undefined}
        open
      />,
    );

    expect(markup).toContain("team_tasks_empty");
    expect(markup).toContain("The Team has no tasks to execute.");
    expect(markup).toContain("Add a task and create the missing dependency.");
    expect(markup).toContain("panels.teamsPanels.details.preflight.task");
    expect(markup).toContain("panels.teamsPanels.details.preflight.dependsOn");
    expect(markup).toContain("panels.teamsPanels.details.preflight.path");
    expect(markup).not.toContain("routePreviews");
  });

  it("shows resolved route details, fallback, and warnings", () => {
    const details: TeamDetailsState = {
      events: [],
      eventCursor: 0,
      latestEventSeq: 0,
      finalSummary: "",
      summaryState: "unknown",
      graph: null,
      mailbox: [],
      pathClaims: [],
      tasks: [],
      teammates: [],
      preflight: {
        team_id: "team-1",
        readiness: true,
        capabilities: { worker_session_liveness: "unsupported" },
        preflight: {
          team_id: "team-1",
          team_exists: true,
          ready: true,
          blocker_count: 0,
          warning_count: 0,
          info_count: 0,
          route_previews: [
            {
              task_id: "task-1",
              teammate_id: "worker-1",
              capability: "resolved",
              route: {
                provider: "openai",
                model: "gpt-5",
                reasoning_effort: "medium",
                source: "team policy",
                permission_mode: "readonly",
                tool_profile: "reviewer",
                route_reasoning_effort: "low",
                fallback_used: true,
                fallback_reason: "primary route unavailable",
                warnings: ["Using fallback route."],
              },
            },
          ],
        },
      },
      isStale: false,
    };

    const markup = renderToStaticMarkup(
      <RuntimeTeamPreflightSection
        details={details}
        onToggle={() => undefined}
        open
      />,
    );

    expect(markup).toContain("openai");
    expect(markup).toContain("gpt-5");
    expect(markup).toContain("medium");
    expect(markup).toContain("readonly");
    expect(markup).toContain("reviewer");
    expect(markup).toContain("low");
    expect(markup).toContain("primary route unavailable");
    expect(markup).toContain("Using fallback route.");
    expect(markup).toContain(
      "workerSessionLivenessBoundary unsupported",
    );
    expect(markup).toContain(
      "panels.teamsPanels.details.preflight.routeCapability.resolved",
    );
  });
});
