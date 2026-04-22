import { describe, expect, it } from "vitest";

import { shouldIgnoreTerminalStreamError } from "@/hooks/workspace/use-workspace-agent-chat-turn";

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
