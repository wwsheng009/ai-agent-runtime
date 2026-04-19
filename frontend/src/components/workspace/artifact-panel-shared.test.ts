import { describe, expect, it } from "vitest";

import {
  buildCheckpointConversationSummary,
  buildCheckpointFileCode,
  formatCheckpointFileChangeLabel,
  formatCheckpointMeta,
  formatCheckpointProvenance,
  formatCheckpointProvenanceSummary,
  formatCheckpointReason,
  formatCheckpointTitle,
  isCheckpointDetailLoading,
  pickInitialCheckpointFilePath,
  resolveCheckpointFileEntries,
} from "@/components/workspace/artifact-panel-shared";

describe("artifact panel helpers", () => {
  it("formats checkpoint titles and meta from available fields", () => {
    expect(
      formatCheckpointTitle({
        id: "chk-1234567890abcdef",
        session_id: "session-1",
        message_count: 3,
        created_at: "2026-03-31T10:00:00Z",
        reason: "shell mutation before write",
      }),
    ).toBe("shell mutation before write");

    expect(
      formatCheckpointMeta({
        id: "chk-1",
        session_id: "session-1",
        message_count: 2,
        conversation_exact: true,
        task_id: "task-1234567890",
        created_at: "2026-03-31T10:00:00Z",
      }),
    ).toBe("2 messages · exact conversation · task task-1234567");

    expect(
      formatCheckpointReason({
        id: "chk-1",
        session_id: "session-1",
        message_count: 2,
        created_at: "2026-03-31T10:00:00Z",
      }),
    ).toBe("Runtime snapshot captured for later inspection.");
  });

  it("prefers provenance labels and preview files when present", () => {
    const checkpoint = {
      id: "chk-1",
      session_id: "session-1",
      message_count: 2,
      created_at: "2026-03-31T10:00:00Z",
    } as const;
    const checkpointFiles = [
      { checkpoint_id: "chk-1", id: "file-1", op: "write", path: "src/a.ts" },
    ];
    const checkpointPreviewFiles = [{ change: "update", path: "src/b.ts" }];

    expect(
      formatCheckpointProvenance({
        profile_resource_count: 2,
        profile_resource_labels: ["memory:memory.json", "notes:notes.md"],
      }),
    ).toEqual(["memory:memory.json", "notes:notes.md"]);

    expect(
      formatCheckpointProvenanceSummary({
        source_refs: ["thread://1", "thread://2"],
        profile_memory_count: 2,
        profile_notes_count: 1,
        profile_resource_count: 3,
      }),
    ).toEqual(["2 source refs", "2 memories", "1 notes", "3 profile resources"]);

    expect(
      pickInitialCheckpointFilePath(
        checkpointFiles,
        checkpointPreviewFiles,
      ),
    ).toBe("src/b.ts");
    expect(
      resolveCheckpointFileEntries(checkpointPreviewFiles, checkpointFiles),
    ).toEqual(checkpointPreviewFiles);
    expect(resolveCheckpointFileEntries([], checkpointFiles)).toEqual(checkpointFiles);
    expect(isCheckpointDetailLoading(checkpoint, "chk-1")).toBe(true);
    expect(isCheckpointDetailLoading(checkpoint, "chk-2")).toBe(false);
  });

  it("builds file detail blocks from preview diffs or raw file metadata", () => {
    expect(
      buildCheckpointFileCode(
        undefined,
        {
          change: "update",
          diff_text: "@@ -1 +1 @@\n-old\n+new",
          path: "src/app.ts",
        },
      ),
    ).toMatchObject({
      language: "diff",
      title: "src/app.ts",
    });

    expect(
      buildCheckpointFileCode(
        {
          checkpoint_id: "chk-1",
          id: "file-1",
          op: "write",
          path: "src/app.ts",
        },
        undefined,
      ),
    ).toMatchObject({
      language: "json",
      title: "src/app.ts",
    });

    expect(
      formatCheckpointFileChangeLabel({
        change: "create_file",
        path: "src/app.ts",
      }),
    ).toBe("create file");

    expect(
      buildCheckpointConversationSummary([
        { role: "assistant", content: "A".repeat(200) },
        { role: "user", content: "Keep this short" },
      ]),
    ).toEqual([
      {
        role: "assistant",
        content: "A".repeat(200),
      },
      {
        role: "user",
        content: "Keep this short",
      },
    ]);
  });
});
