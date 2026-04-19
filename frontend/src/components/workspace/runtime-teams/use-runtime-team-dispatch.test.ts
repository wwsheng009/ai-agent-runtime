import { describe, expect, it } from "vitest";

import {
  buildDispatchMailboxPreview,
  buildDispatchTaskRequest,
  resolveSelectedDispatchTeamIds,
} from "@/components/workspace/runtime-teams/use-runtime-team-dispatch";

describe("useRuntimeTeamDispatch helpers", () => {
  it("normalizes dispatch request drafts into a ready task payload", () => {
    const result = buildDispatchTaskRequest({
      deliverablesDraft: " report.md \n\n demo.mp4 ",
      goalDraft: "  Ship runtime team dispatch  ",
      inputsDraft: " spec.md \n notes.txt ",
      priorityDraft: "not-a-number",
      titleDraft: " ",
    });

    expect(result.error).toBeNull();
    expect(result.request).toEqual({
      deliverables: ["report.md", "demo.mp4"],
      goal: "Ship runtime team dispatch",
      inputs: ["spec.md", "notes.txt"],
      priority: 50,
      status: "ready",
      title: "Ship runtime team dispatch",
    });
  });

  it("rejects empty dispatch drafts and resolves default team selection safely", () => {
    expect(
      buildDispatchTaskRequest({
        deliverablesDraft: "",
        goalDraft: " ",
        inputsDraft: "",
        priorityDraft: "80",
        titleDraft: " ",
      }),
    ).toEqual({
      error: "enter a task title or goal before dispatching",
      request: null,
    });

    expect(
      resolveSelectedDispatchTeamIds([], [
        { id: "team-1" },
        { id: "team-2" },
      ]),
    ).toEqual(["team-1", "team-2"]);

    expect(
      resolveSelectedDispatchTeamIds(["team-2", "missing"], [
        { id: "team-1" },
        { id: "team-2" },
      ]),
    ).toEqual(["team-2"]);
  });

  it("keeps mailbox preview markdown intact and falls back to kind when body is empty", () => {
    expect(
      buildDispatchMailboxPreview([
        {
          body: "- first item\n- second item\n\n`code`",
          kind: "info",
        },
        {
          body: "   ",
          kind: "warning",
        },
        {
          body: "ignored third message",
          kind: "info",
        },
      ]),
    ).toEqual([
      "- first item\n- second item\n\n`code`",
      "warning",
    ]);
  });
});
