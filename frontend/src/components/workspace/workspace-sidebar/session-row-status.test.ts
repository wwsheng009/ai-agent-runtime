import { describe, expect, it } from "vitest";

import {
  buildSidebarSessionActivity,
  isArchivedRuntimeSession,
  isClosedRuntimeSession,
  normalizeRuntimeSessionState,
  resolveSidebarSessionRowState,
  resolveSidebarSessionRowStatus,
  splitRuntimeSessionsByVisibility,
} from "./session-row-status";

function session(state?: string, id = "s-1") {
  return { id, state };
}

describe("session-row-status", () => {
  it("normalizes session state strings", () => {
    expect(normalizeRuntimeSessionState(session(" Archived "))).toBe("archived");
    expect(normalizeRuntimeSessionState(session(undefined))).toBe("");
    expect(isArchivedRuntimeSession(session("archived"))).toBe(true);
    expect(isArchivedRuntimeSession(session("active"))).toBe(false);
    expect(isClosedRuntimeSession(session("closed"))).toBe(true);
    expect(isClosedRuntimeSession(session("archived"))).toBe(false);
  });

  it("resolves row state with active as default", () => {
    expect(resolveSidebarSessionRowState(session("archived"))).toBe("archived");
    expect(resolveSidebarSessionRowState(session("closed"))).toBe("closed");
    expect(resolveSidebarSessionRowState(session("active"))).toBe("active");
    expect(resolveSidebarSessionRowState(session(undefined))).toBe("active");
  });

  it("prioritizes waiting statuses above running and subagents", () => {
    expect(
      resolveSidebarSessionRowStatus(session("active"), {
        pendingApprovals: 1,
      }),
    ).toEqual({ kind: "waitingApproval", waiting: true });

    expect(
      resolveSidebarSessionRowStatus(session("active"), {
        planPending: true,
        waitingAnswer: true,
        running: true,
      }),
    ).toEqual({ kind: "planPending", waiting: true });

    expect(
      resolveSidebarSessionRowStatus(session("active"), {
        waitingAnswer: true,
        running: true,
        runningAgents: 3,
      }),
    ).toEqual({ kind: "waitingAnswer", waiting: true });

    expect(
      resolveSidebarSessionRowStatus(session("active"), {
        running: true,
        runningAgents: 2,
      }),
    ).toEqual({ kind: "running", waiting: false });

    expect(
      resolveSidebarSessionRowStatus(session("active"), { runningAgents: 2 }),
    ).toEqual({ kind: "subagents", waiting: false });

    expect(resolveSidebarSessionRowStatus(session("active"))).toEqual({
      kind: "idle",
      waiting: false,
    });
  });

  it("lets snapshot archived/closed override stale local activity", () => {
    expect(
      resolveSidebarSessionRowStatus(session("archived"), {
        pendingApprovals: 2,
        running: true,
      }),
    ).toEqual({ kind: "archived", waiting: false });

    expect(
      resolveSidebarSessionRowStatus(session("closed"), { running: true }),
    ).toEqual({ kind: "closed", waiting: false });
  });

  it("hides archived sessions by default and reports the hidden count", () => {
    const sessions = [
      session("active", "a"),
      session("archived", "b"),
      session("archived", "c"),
      session("closed", "d"),
    ];

    const hidden = splitRuntimeSessionsByVisibility(sessions, {
      showArchived: false,
    });
    expect(hidden.visible.map((item) => item.id)).toEqual(["a", "d"]);
    expect(hidden.archived.map((item) => item.id)).toEqual(["b", "c"]);
    expect(hidden.hiddenArchivedCount).toBe(2);

    const shown = splitRuntimeSessionsByVisibility(sessions, {
      showArchived: true,
    });
    expect(shown.visible.map((item) => item.id)).toEqual(["a", "b", "c", "d"]);
    expect(shown.hiddenArchivedCount).toBe(0);
  });

  it("projects local signals into sidebar activity without fabricating status", () => {
    expect(buildSidebarSessionActivity({})).toEqual({});
    expect(
      buildSidebarSessionActivity({ sessionId: "  ", responding: true }),
    ).toEqual({});
    expect(
      buildSidebarSessionActivity({ sessionId: "s-1", responding: false }),
    ).toEqual({});

    expect(
      buildSidebarSessionActivity({
        sessionId: "s-1",
        pendingInteractionKind: "approval",
      }),
    ).toEqual({ "s-1": { pendingApprovals: 1 } });

    expect(
      buildSidebarSessionActivity({
        sessionId: "s-1",
        pendingInteractionKind: "plan_review",
        responding: true,
      }),
    ).toEqual({ "s-1": { planPending: true, running: true } });

    expect(
      buildSidebarSessionActivity({
        sessionId: "s-2",
        pendingInteractionKind: "question",
        runningAgents: 2,
      }),
    ).toEqual({ "s-2": { waitingAnswer: true, runningAgents: 2 } });
  });
});
