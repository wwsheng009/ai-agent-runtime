import { describe, expect, it } from "vitest";

import { normalizeSessionId } from "./session-id";

describe("normalizeSessionId", () => {
  it("keeps canonical ids unchanged", () => {
    expect(normalizeSessionId("session_20260826142907_TZplKI8C")).toBe(
      "session_20260826142907_TZplKI8C",
    );
  });

  it("converges whitespace, trailing-slash and path-prefix variants", () => {
    expect(normalizeSessionId("abc")).toBe("abc");
    expect(normalizeSessionId("  abc  ")).toBe("abc");
    expect(normalizeSessionId("abc/")).toBe("abc");
    expect(normalizeSessionId("abc//")).toBe("abc");
    expect(normalizeSessionId("dir/abc")).toBe("abc");
    expect(normalizeSessionId("dir/sub/abc")).toBe("abc");
    expect(normalizeSessionId("a\\b\\abc")).toBe("abc");
    expect(normalizeSessionId(" session_20260826142907_TZplKI8C ")).toBe(
      "session_20260826142907_TZplKI8C",
    );
  });

  it("returns empty string for empty, blank or separator-only input", () => {
    expect(normalizeSessionId(undefined)).toBe("");
    expect(normalizeSessionId(null)).toBe("");
    expect(normalizeSessionId("")).toBe("");
    expect(normalizeSessionId("   ")).toBe("");
    expect(normalizeSessionId("/")).toBe("");
    expect(normalizeSessionId("\\")).toBe("");
  });

  it("is idempotent", () => {
    for (const input of ["abc/", "dir/abc", "  abc  ", "session_x", "a/b/c/"]) {
      expect(normalizeSessionId(normalizeSessionId(input))).toBe(
        normalizeSessionId(input),
      );
    }
  });
});
