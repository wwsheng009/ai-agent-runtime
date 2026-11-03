import { describe, expect, it } from "vitest";

import { createRequestFrom, emptyCreateForm } from "./form-state";

describe("createRequestFrom targets", () => {
  it("session target requires a session id", () => {
    const form = {
      ...emptyCreateForm(),
      title: "t",
      prompt: "p",
      targetKind: "session" as const,
    };
    expect(createRequestFrom(form)).toBeNull();
  });

  it("headless target creates without session id and carries goal/plan", () => {
    const request = createRequestFrom({
      ...emptyCreateForm(),
      title: "nightly",
      prompt: "run checks",
      targetKind: "headless",
      goal: "keep the system healthy",
      plan: "check disk\ncheck memory",
    });
    expect(request).toMatchObject({
      sessionId: "",
      target_kind: "headless",
      goal: "keep the system healthy",
      plan: ["check disk", "check memory"],
      after_seconds: 1800,
    });
  });

  it("workspace target requires an absolute workspace path", () => {
    const base = {
      ...emptyCreateForm(),
      title: "workspace",
      prompt: "run checks",
      targetKind: "workspace" as const,
      goal: "keep the workspace healthy",
    };
    expect(createRequestFrom(base)).toBeNull();
    expect(
      createRequestFrom({
        ...base,
        targetWorkspace: "E:/projects/demo",
        targetProfile: "scheduled",
      }),
    ).toMatchObject({
      target_kind: "workspace",
      target_workspace: "E:/projects/demo",
      target_profile: "scheduled",
    });
  });
});
