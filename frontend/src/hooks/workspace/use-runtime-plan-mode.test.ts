import { describe, expect, it } from "vitest";

import {
  canSubmitPlanModeDecision,
  formatPlanModeStatusLabel,
  shouldReloadRuntimePlanMode,
} from "@/hooks/workspace/use-runtime-plan-mode";
import type { RuntimeSessionPlanMode } from "@/lib/runtime-api";

function buildPlan(
  overrides: Partial<RuntimeSessionPlanMode> = {},
): RuntimeSessionPlanMode {
  return {
    active: false,
    permission_mode: "default",
    plan_content: "",
    plan_content_available: false,
    session_id: "session-1",
    status: "inactive",
    ...overrides,
  };
}

describe("useRuntimePlanMode helpers", () => {
  it("reloads when the session changes or plan data is missing", () => {
    expect(
      shouldReloadRuntimePlanMode({
        hasPlan: false,
        loadedPlanSessionId: "",
        sessionId: "session-1",
      }),
    ).toBe(true);

    expect(
      shouldReloadRuntimePlanMode({
        hasPlan: true,
        loadedPlanSessionId: "session-1",
        sessionId: "session-1",
      }),
    ).toBe(false);

    expect(
      shouldReloadRuntimePlanMode({
        hasPlan: true,
        loadedPlanSessionId: "session-1",
        sessionId: "session-2",
      }),
    ).toBe(true);
  });

  it("reloads on new plan-related runtime events only once", () => {
    expect(
      shouldReloadRuntimePlanMode({
        hasPlan: true,
        lastHandledEventType: undefined,
        lastRuntimeEventType: "tool.completed",
        loadedPlanSessionId: "session-1",
        sessionId: "session-1",
      }),
    ).toBe(true);

    expect(
      shouldReloadRuntimePlanMode({
        hasPlan: true,
        lastHandledEventType: "tool.completed",
        lastRuntimeEventType: "tool.completed",
        loadedPlanSessionId: "session-1",
        sessionId: "session-1",
      }),
    ).toBe(false);

    expect(
      shouldReloadRuntimePlanMode({
        hasPlan: true,
        lastHandledEventType: "tool.completed",
        lastRuntimeEventType: "checkpoint_created",
        loadedPlanSessionId: "session-1",
        sessionId: "session-1",
      }),
    ).toBe(false);
  });

  it("formats status labels and decision availability", () => {
    expect(formatPlanModeStatusLabel(null)).toBe("Unavailable");
    expect(formatPlanModeStatusLabel(buildPlan({ active: true, status: "active" }))).toBe(
      "Active",
    );
    expect(formatPlanModeStatusLabel(buildPlan({ status: "exited" }))).toBe("Exited");
    expect(formatPlanModeStatusLabel(buildPlan())).toBe("Inactive");

    expect(canSubmitPlanModeDecision(null)).toBe(false);
    expect(canSubmitPlanModeDecision(buildPlan({ active: true }))).toBe(true);
    expect(canSubmitPlanModeDecision(buildPlan({ active: false }))).toBe(false);
  });
});
