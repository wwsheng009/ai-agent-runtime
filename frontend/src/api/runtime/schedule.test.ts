import { afterEach, describe, expect, it, vi } from "vitest";

import { createScheduleTask, fetchScheduleJobRuns } from "./schedule";

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

describe("createScheduleTask", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("serializes the default session target using the backend snake_case contract", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        record: {
          id: "schedule-1",
          kind: "after",
          title: "Reminder",
          prompt: "Do the thing",
          scheduledAt: "2026-11-19T12:30:00Z",
        },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await createScheduleTask({
      sessionId: "session-123",
      title: "Reminder",
      prompt: "Do the thing",
      after_seconds: 1800,
      target_kind: "session",
    });

    expect(fetchMock).toHaveBeenCalledOnce();
    const [url, init] = fetchMock.mock.calls[0] as unknown as [
      string,
      RequestInit,
    ];
    expect(url).toBe("/api/runtime/schedule/create");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({
      session_id: "session-123",
      title: "Reminder",
      prompt: "Do the thing",
      after_seconds: 1800,
      target_kind: "session",
    });
    expect(JSON.parse(String(init.body))).not.toHaveProperty("sessionId");
  });

  it("serializes non-session target fields in snake_case", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ record: {} }));
    vi.stubGlobal("fetch", fetchMock);

    await createScheduleTask({
      sessionId: "",
      title: "Health check",
      prompt: "Run checks",
      target_kind: "headless",
      goal: "Keep the system healthy",
      plan: ["Check disk"],
      every_seconds: 3600,
      target_profile: "scheduled",
    });

    const [, init] = fetchMock.mock.calls[0] as unknown as [
      string,
      RequestInit,
    ];
    expect(JSON.parse(String(init.body))).toEqual({
      session_id: "",
      title: "Health check",
      prompt: "Run checks",
      every_seconds: 3600,
      goal: "Keep the system healthy",
      plan: ["Check disk"],
      target_kind: "headless",
      target_profile: "scheduled",
    });
  });
});

describe("fetchScheduleJobRuns", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("omits the optional session filter when the catalog task has no session binding", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        id: "job-1",
        runs: [],
        count: 0,
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await fetchScheduleJobRuns(undefined, "job-1", 20);

    expect(fetchMock).toHaveBeenCalledOnce();
    const [url] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/runtime/schedule/runs?id=job-1&limit=20");
  });
});
