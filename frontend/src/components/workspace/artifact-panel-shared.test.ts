import { describe, expect, it } from "vitest";

import {
  buildCheckpointConversationSummary,
  buildCheckpointFileCode,
  formatBacktrackAuditIdentity,
  formatBacktrackAuditMeta,
  formatBacktrackAuditSummary,
  formatBacktrackAuditTitle,
  formatCheckpointFileChangeLabel,
  formatCheckpointMeta,
  formatCheckpointProvenance,
  formatCheckpointProvenanceSummary,
  formatCheckpointReason,
  formatCheckpointTitle,
  isCheckpointDetailLoading,
  pickInitialBacktrackAuditId,
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

  it("formats backtrack audit tombstone titles, meta, and selection", () => {
    const entry = {
      id: "tomb-1",
      created_at: "2026-07-26T12:00:00Z",
      mode: "both",
      reason: "user requested rewind",
      user_turn_index: 2,
      message_index: 5,
      message_id: "msg-abcdef123456",
      anchor_preview: "Please rewrite the plan for the next phase",
      truncated_to_message_count: 4,
      removed_message_count: 6,
      removed_user_turns: 2,
      edited: true,
    };

    expect(formatBacktrackAuditTitle(entry)).toBe(
      "Please rewrite the plan for the next phase",
    );
    expect(formatBacktrackAuditMeta(entry)).toBe(
      "turn 2 · msg 5 · −6 msgs · −2 turns · both · edited",
    );
    expect(formatBacktrackAuditSummary(entry)).toBe("user requested rewind");
    expect(formatBacktrackAuditIdentity(entry)).toBe("msg-abcdef123456");
    expect(
      pickInitialBacktrackAuditId(
        [
          entry,
          {
            ...entry,
            id: "tomb-2",
            message_id: undefined,
            anchor_preview: undefined,
          },
        ],
        "tomb-2",
      ),
    ).toBe("tomb-2");
    expect(pickInitialBacktrackAuditId([entry], "missing")).toBe("tomb-1");
    expect(
      formatBacktrackAuditTitle({
        ...entry,
        anchor_preview: undefined,
        message_id: undefined,
      }),
    ).toBe("User turn 2");
    expect(
      formatBacktrackAuditSummary({
        ...entry,
        reason: undefined,
      }),
    ).toBe("Truncated to 4 messages; removed 2 user turn(s).");
  });
});
