import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  createRuntimeTeamDefinition,
  createRuntimeTeamRunDraft,
  getRuntimeTeamDefinition,
  getRuntimeTeamRun,
  getRuntimeTeam,
  getRuntimeTeamPreflight,
  getRuntimeTeamReadiness,
  getRuntimeTeamRunPreflight,
  isRuntimeTeamProductCapabilitySupported,
  listRuntimeTeamDefinitions,
  listRuntimeTeamEvents,
  listRuntimeTeamRuns,
  listRuntimeTeams,
  startRuntimeTeamRun,
  updateRuntimeTeamDefinition,
} from "@/api/runtime/teams";
import * as runtimeTeamApi from "@/api/runtime/teams";

describe("getRuntimeTeam", () => {
  const originalFetch = globalThis.fetch;
  let calls: Array<{ url: string; init?: RequestInit }> = [];

  beforeEach(() => {
    calls = [];
    globalThis.fetch = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        calls.push({ url: String(input), init });
        const isStartRequest = String(input).includes("/start");
        return new Response(
          JSON.stringify(
            isStartRequest
              ? {
                  run: {
                    id: "run/one",
                    status: "active",
                  },
                  capabilities: { run_start: "supported" },
                  request_id: "request-start-1",
                }
              : {
                  team: {
                    id: "team/one",
                    workspace_id: "workspace-1",
                    status: "active",
                  },
                },
          ),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        );
      },
    ) as typeof fetch;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("uses the detail endpoint, encodes the id, and preserves the response shape", async () => {
    const controller = new AbortController();
    const response = await getRuntimeTeam("team/one?view=detail", {
      signal: controller.signal,
    });

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toContain(
      "/api/runtime/teams/team%2Fone%3Fview%3Ddetail",
    );
    expect(calls[0].init?.method).toBeUndefined();
    expect(calls[0].init?.headers).toEqual({
      Accept: "application/json",
    });
    expect(calls[0].init?.signal).toBe(controller.signal);
    expect(response).toEqual({
      team: {
        id: "team/one",
        workspace_id: "workspace-1",
        status: "active",
      },
    });
  });

  it("forwards cancellation for the team directory request", async () => {
    const controller = new AbortController();

    await listRuntimeTeams({
      limit: 12,
      teamIds: ["team-1", "team-2"],
      signal: controller.signal,
    });

    expect(calls[0].url).toContain(
      "/api/runtime/teams?limit=12&team_ids=team-1%2Cteam-2",
    );
    expect(calls[0].init?.signal).toBe(controller.signal);
  });

  it("serializes the Team-local cursor through the events facade", async () => {
    await listRuntimeTeamEvents("team-1", { afterSeq: 7, limit: 10 });

    expect(calls[0].url).toContain(
      "/api/runtime/teams/team-1/events?after=7&limit=10",
    );
  });

  it("uses the registered preflight endpoint", async () => {
    await getRuntimeTeamPreflight("team/one");

    expect(calls[0].url).toContain(
      "/api/runtime/teams/team%2Fone/preflight",
    );
  });

  it("uses the independent Team Run preflight endpoint and forwards cancellation", async () => {
    const controller = new AbortController();

    await getRuntimeTeamRunPreflight("run/one", {
      signal: controller.signal,
    });

    expect(calls[0].url).toContain(
      "/api/runtime/team-runs/run%2Fone/preflight",
    );
    expect(calls[0].init?.headers).toEqual({
      Accept: "application/json",
    });
    expect(calls[0].init?.signal).toBe(controller.signal);
  });

  it("uses the registered readiness endpoint and forwards cancellation", async () => {
    const controller = new AbortController();

    await getRuntimeTeamReadiness("team/one", { signal: controller.signal });

    expect(calls[0].url).toContain(
      "/api/runtime/teams/team%2Fone/readiness",
    );
    expect(calls[0].init?.signal).toBe(controller.signal);
  });

  it("strips caller-supplied ownership fields from Definition requests", async () => {
    const request = {
      name: "Review team",
      description: "Two-role review",
      config: { roles: ["reviewer", "tester"] },
      owner_id: "forged-owner",
      workspace_id: "forged-workspace",
    } as Parameters<typeof createRuntimeTeamDefinition>[0];

    await createRuntimeTeamDefinition(request);

    expect(calls[0].url).toContain("/api/runtime/team-definitions");
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      name: "Review team",
      description: "Two-role review",
      config: { roles: ["reviewer", "tester"] },
    });
    expect(calls[0].init?.headers).toEqual({
      Accept: "application/json",
      "Content-Type": "application/json",
    });
  });

  it("sends an idempotency key for Run draft creation and omits owner/workspace", async () => {
    const request = {
      goal: "Review the change",
      config: { roles: ["reviewer"] },
      owner_id: "forged-owner",
      workspace_id: "forged-workspace",
    } as Parameters<typeof createRuntimeTeamRunDraft>[0];

    await createRuntimeTeamRunDraft(request, {
      idempotencyKey: "create-draft-1",
    });

    expect(calls[0].url).toContain("/api/runtime/team-runs");
    expect(calls[0].init?.headers).toEqual({
      Accept: "application/json",
      "Content-Type": "application/json",
      "Idempotency-Key": "create-draft-1",
    });
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      goal: "Review the change",
      config: { roles: ["reviewer"] },
    });
  });

  it("covers Definition/Run list, read, and versioned update routes", async () => {
    const controller = new AbortController();
    await listRuntimeTeamDefinitions({ limit: 20, signal: controller.signal });
    await getRuntimeTeamDefinition("def/one");
    await updateRuntimeTeamDefinition("def/one", {
      expected_version: 4,
      name: "Review team v2",
      description: "updated",
      config: { roles: ["reviewer", "tester"] },
    });
    await listRuntimeTeamRuns({ limit: 10 });
    await getRuntimeTeamRun("run/one");

    expect(calls[0].url).toContain("/api/runtime/team-definitions?limit=20");
    expect(calls[0].init?.signal).toBe(controller.signal);
    expect(calls[1].url).toContain("/api/runtime/team-definitions/def%2Fone");
    expect(calls[2].init?.method).toBe("PATCH");
    expect(JSON.parse(String(calls[2].init?.body))).toEqual({
      expected_version: 4,
      name: "Review team v2",
      description: "updated",
      config: { roles: ["reviewer", "tester"] },
    });
    expect(calls[3].url).toContain("/api/runtime/team-runs?limit=10");
    expect(calls[4].url).toContain("/api/runtime/team-runs/run%2Fone");
  });

  it("starts a Run with an encoded id, expected version, idempotency key, and signal", async () => {
    const controller = new AbortController();
    const response = await startRuntimeTeamRun(
      "run/one?view=detail",
      { expected_version: 1 },
      {
        idempotencyKey: " start-run-1 ",
        signal: controller.signal,
      },
    );

    expect(calls[0].url).toContain(
      "/api/runtime/team-runs/run%2Fone%3Fview%3Ddetail/start",
    );
    expect(calls[0].init?.method).toBe("POST");
    expect(calls[0].init?.headers).toEqual({
      Accept: "application/json",
      "Content-Type": "application/json",
      "Idempotency-Key": "start-run-1",
    });
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      expected_version: 1,
    });
    expect(calls[0].init?.signal).toBe(controller.signal);
    expect(response).toEqual({
      run: {
        id: "run/one",
        status: "active",
      },
      capabilities: { run_start: "supported" },
      request_id: "request-start-1",
    });
  });

  it("requires a non-empty idempotency key before sending a Start request", async () => {
    await expect(
      startRuntimeTeamRun("run/one", { expected_version: 1 }, {
        idempotencyKey: "  ",
      }),
    ).rejects.toThrow("idempotencyKey is required");
    expect(calls).toHaveLength(0);
  });

  it("requires a non-empty idempotency key before sending a draft request", async () => {
    await expect(
      createRuntimeTeamRunDraft({ goal: "Review" }, { idempotencyKey: "  " }),
    ).rejects.toThrow("idempotencyKey is required");
    expect(calls).toHaveLength(0);
  });

  it("exposes Start only when its capability is supported", () => {
    const capabilities = {
      run_start: "supported",
      run_pause: "unsupported",
      run_resume: "unsupported",
      run_cancel: "unsupported",
    };

    expect(
      isRuntimeTeamProductCapabilitySupported(capabilities, "run_start"),
    ).toBe(true);
    expect(
      isRuntimeTeamProductCapabilitySupported(
        { ...capabilities, run_draft_read: "supported" },
        "run_draft_read",
      ),
    ).toBe(true);
    expect(runtimeTeamApi).toHaveProperty("startRuntimeTeamRun");
    expect(runtimeTeamApi).not.toHaveProperty("pauseRuntimeTeamRun");
    expect(runtimeTeamApi).not.toHaveProperty("resumeRuntimeTeamRun");
    expect(runtimeTeamApi).not.toHaveProperty("cancelRuntimeTeamRun");
  });
});
