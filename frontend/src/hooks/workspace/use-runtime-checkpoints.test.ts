import { describe, expect, it } from "vitest";

import {
  buildRuntimeEventReloadKey,
  resolveCheckpointDetailState,
  shouldReloadBacktrackAudit,
  shouldReloadRuntimeCheckpoints,
} from "@/hooks/workspace/use-runtime-checkpoints";

describe("useRuntimeCheckpoints helpers", () => {
  it("detects when checkpoint data should reload", () => {
    expect(
      shouldReloadRuntimeCheckpoints({
        loadedCheckpointSessionId: "",
        sessionId: "session-1",
      }),
    ).toBe(true);

    expect(
      shouldReloadRuntimeCheckpoints({
        loadedCheckpointSessionId: "session-1",
        sessionId: "session-1",
      }),
    ).toBe(false);

    expect(
      shouldReloadRuntimeCheckpoints({
        lastHandledEventKey: "",
        lastRuntimeEventKey: buildRuntimeEventReloadKey("checkpoint_created", 1),
        lastRuntimeEventType: "checkpoint_created",
        loadedCheckpointSessionId: "session-1",
        sessionId: "session-1",
      }),
    ).toBe(true);

    expect(
      shouldReloadRuntimeCheckpoints({
        lastHandledEventKey: buildRuntimeEventReloadKey("backtrack_finished", 2),
        lastRuntimeEventKey: buildRuntimeEventReloadKey("backtrack_finished", 2),
        lastRuntimeEventType: "backtrack_finished",
        loadedCheckpointSessionId: "session-1",
        sessionId: "session-1",
      }),
    ).toBe(false);

    expect(
      shouldReloadRuntimeCheckpoints({
        lastHandledEventKey: buildRuntimeEventReloadKey("backtrack_finished", 2),
        lastRuntimeEventKey: buildRuntimeEventReloadKey("backtrack_finished", 3),
        lastRuntimeEventType: "backtrack_finished",
        loadedCheckpointSessionId: "session-1",
        sessionId: "session-1",
      }),
    ).toBe(true);
  });

  it("detects when backtrack audit should reload without looping on the same event", () => {
    expect(
      shouldReloadBacktrackAudit({
        loadedAuditSessionId: "",
        sessionId: "session-1",
      }),
    ).toBe(true);

    expect(
      shouldReloadBacktrackAudit({
        lastHandledEventKey: buildRuntimeEventReloadKey("backtrack_finished", 4),
        lastRuntimeEventKey: buildRuntimeEventReloadKey("backtrack_finished", 4),
        lastRuntimeEventType: "backtrack_finished",
        loadedAuditSessionId: "session-1",
        sessionId: "session-1",
      }),
    ).toBe(false);

    expect(
      shouldReloadBacktrackAudit({
        lastHandledEventKey: buildRuntimeEventReloadKey("backtrack_finished", 4),
        lastRuntimeEventKey: buildRuntimeEventReloadKey("backtrack_finished", 5),
        lastRuntimeEventType: "backtrack_finished",
        loadedAuditSessionId: "session-1",
        sessionId: "session-1",
      }),
    ).toBe(true);

    expect(
      shouldReloadBacktrackAudit({
        lastRuntimeEventType: "tool.completed",
        loadedAuditSessionId: "session-1",
        sessionId: "session-1",
      }),
    ).toBe(false);
  });

  it("resolves checkpoint detail selection from preview files first", () => {
    const state = resolveCheckpointDetailState({
      checkpointFiles: [
        {
          checkpoint_id: "chk-1",
          id: "file-1",
          op: "write",
          path: "src/a.ts",
        },
      ],
      checkpointPreview: {
        checkpoint_id: "chk-1",
        mode: "both",
        preview_files: [
          {
            change: "update",
            diff_text: "@@ -1 +1 @@\n-old\n+new",
            path: "src/b.ts",
          },
        ],
      },
      selectedCheckpoint: {
        created_at: "2026-03-31T10:00:00Z",
        id: "chk-1",
        message_count: 3,
        provenance: {
          profile_resource_labels: ["memory:memory.json"],
        },
        session_id: "session-1",
      },
      selectedCheckpointFilePath: null,
    });

    expect(state.selectedCheckpointFilePath).toBe("src/b.ts");
    expect(state.checkpointPreviewFiles).toHaveLength(1);
    expect(state.checkpointProvenance).toEqual(["memory:memory.json"]);
    expect(state.checkpointFileCode).toMatchObject({
      language: "diff",
      title: "src/b.ts",
    });
  });
});
