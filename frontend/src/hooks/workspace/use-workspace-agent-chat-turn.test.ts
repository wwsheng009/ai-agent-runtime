import { describe, expect, it } from "vitest";

import {
  resolveChatTurnWorkspacePath,
  shouldIgnoreTerminalStreamError,
} from "@/hooks/workspace/use-workspace-agent-chat-turn";

describe("workspace agent chat turn terminal stream guards", () => {
  it("ignores trailing stream errors after the turn has already finalized", () => {
    expect(
      shouldIgnoreTerminalStreamError({
        finalized: true,
        aborted: false,
      }),
    ).toBe(true);
  });

  it("ignores trailing stream errors after the request was aborted", () => {
    expect(
      shouldIgnoreTerminalStreamError({
        finalized: false,
        aborted: true,
      }),
    ).toBe(true);
  });

  it("still surfaces genuine errors before finalize", () => {
    expect(
      shouldIgnoreTerminalStreamError({
        finalized: false,
        aborted: false,
      }),
    ).toBe(false);
  });
});

describe("workspace agent chat turn workspace_path resolution", () => {
  it("omits workspace_path for materialized sessions so the backend binding applies", () => {
    expect(resolveChatTurnWorkspacePath("session-1", "E:\\temp")).toBeUndefined();
  });

  it("sends the identity workspace path for draft threads to bind on first turn", () => {
    expect(resolveChatTurnWorkspacePath(undefined, "E:\\projects\\demo")).toBe(
      "E:\\projects\\demo",
    );
    expect(resolveChatTurnWorkspacePath(undefined, "   ")).toBeUndefined();
    expect(resolveChatTurnWorkspacePath(null, undefined)).toBeUndefined();
  });
});
