import { describe, expect, it } from "vitest";

import {
  canSubmitPlanModeDecision,
  formatPlanModeStatusLabel,
  shouldReloadRuntimePlanMode,
} from "@/hooks/workspace/use-runtime-plan-mode";
import { buildRuntimeEventReloadKey } from "@/hooks/workspace/use-runtime-checkpoints";
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
  it("reloads when the session changes, not merely because plan is null", () => {
    expect(
      shouldReloadRuntimePlanMode({
        loadedPlanSessionId: "",
        sessionId: "session-1",
      }),
    ).toBe(true);

    expect(
      shouldReloadRuntimePlanMode({
        loadedPlanSessionId: "session-1",
        sessionId: "session-1",
      }),
    ).toBe(false);

    expect(
      shouldReloadRuntimePlanMode({
        loadedPlanSessionId: "session-1",
        sessionId: "session-2",
      }),
    ).toBe(true);
  });

  it("reloads on new plan-related runtime events only once per event key", () => {
    const firstKey = buildRuntimeEventReloadKey("tool.completed", 1);
    const secondKey = buildRuntimeEventReloadKey("tool.completed", 2);

    expect(
      shouldReloadRuntimePlanMode({
        lastHandledEventKey: "",
        lastRuntimeEventKey: firstKey,
        lastRuntimeEventType: "tool.completed",
        loadedPlanSessionId: "session-1",
        sessionId: "session-1",
      }),
    ).toBe(true);

    expect(
      shouldReloadRuntimePlanMode({
        lastHandledEventKey: firstKey,
        lastRuntimeEventKey: firstKey,
        lastRuntimeEventType: "tool.completed",
        loadedPlanSessionId: "session-1",
        sessionId: "session-1",
      }),
    ).toBe(false);

    expect(
      shouldReloadRuntimePlanMode({
        lastHandledEventKey: firstKey,
        lastRuntimeEventKey: secondKey,
        lastRuntimeEventType: "tool.completed",
        loadedPlanSessionId: "session-1",
        sessionId: "session-1",
      }),
    ).toBe(true);

    expect(
      shouldReloadRuntimePlanMode({
        lastHandledEventKey: firstKey,
        lastRuntimeEventKey: buildRuntimeEventReloadKey("checkpoint_created", 3),
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
