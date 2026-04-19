import { describe, expect, it } from "vitest";

import {
  resolveCheckpointDetailState,
  shouldReloadRuntimeCheckpoints,
} from "@/hooks/workspace/use-runtime-checkpoints";

describe("useRuntimeCheckpoints helpers", () => {
  it("detects when checkpoint data should reload", () => {
    expect(
      shouldReloadRuntimeCheckpoints({
        checkpointsCount: 0,
        loadedCheckpointSessionId: "",
        sessionId: "session-1",
      }),
    ).toBe(true);

    expect(
      shouldReloadRuntimeCheckpoints({
        checkpointsCount: 2,
        loadedCheckpointSessionId: "session-1",
        sessionId: "session-1",
      }),
    ).toBe(false);

    expect(
      shouldReloadRuntimeCheckpoints({
        checkpointsCount: 2,
        lastRuntimeEventType: "checkpoint_created",
        loadedCheckpointSessionId: "session-1",
        sessionId: "session-1",
      }),
    ).toBe(true);
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
