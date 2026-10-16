import { describe, expect, it } from "vitest";

import {
  readSessionProfileIdentity,
  SESSION_PROFILE_NAME_CONTEXT_KEY,
  SESSION_PROFILE_REF_CONTEXT_KEY,
  SESSION_PROFILE_REF_LEGACY_CONTEXT_KEY,
} from "./session-profile";

describe("readSessionProfileIdentity", () => {
  it("reads canonical keys from session metadata context", () => {
    expect(
      readSessionProfileIdentity({
        [SESSION_PROFILE_REF_CONTEXT_KEY]: "reviewer",
        [SESSION_PROFILE_NAME_CONTEXT_KEY]: "Reviewer",
      }),
    ).toEqual({ ref: "reviewer", name: "Reviewer" });
  });

  it("falls back to legacy keys when canonical keys are absent", () => {
    expect(
      readSessionProfileIdentity({
        [SESSION_PROFILE_REF_LEGACY_CONTEXT_KEY]: "legacy-ref",
      }),
    ).toEqual({ ref: "legacy-ref", name: "legacy-ref" });
  });

  it("trims values and ignores empty ones", () => {
    expect(
      readSessionProfileIdentity({
        [SESSION_PROFILE_REF_CONTEXT_KEY]: "  coder  ",
        [SESSION_PROFILE_NAME_CONTEXT_KEY]: "   ",
      }),
    ).toEqual({ ref: "coder", name: "coder" });
  });

  it("returns empty identity for unbound sessions and non-object contexts", () => {
    expect(readSessionProfileIdentity(undefined)).toEqual({ ref: "", name: "" });
    expect(readSessionProfileIdentity("nope")).toEqual({ ref: "", name: "" });
    expect(readSessionProfileIdentity({})).toEqual({ ref: "", name: "" });
  });
});
