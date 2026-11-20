import { afterEach, describe, expect, it, vi } from "vitest";

import {
  createScheduleTemplate,
  createScheduleTask,
  deleteScheduleTemplate,
  fetchScheduleTemplates,
  fetchScheduleJobRuns,
  normalizeScheduleRecord,
  normalizeScheduleTemplate,
  updateScheduleTask,
  updateScheduleTemplate,
} from "./schedule";

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
      provider: "gateway",
      model: "model-a",
      reasoning_effort: "high",
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
      target_provider: "gateway",
      target_model: "model-a",
      target_reasoning_effort: "high",
    });
  });

  it("serializes route overrides for update using target_* keys", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ id: "schedule-1", updated: true }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await updateScheduleTask({
      sessionId: "",
      id: "schedule-1",
      expected: {
        id: "schedule-1",
        kind: "every",
        title: "Health check",
        prompt: "Run checks",
        scheduledAt: "2026-11-19T12:30:00Z",
      },
      target_kind: "headless",
      provider: "gateway",
      model: "model-a",
      reasoning_effort: "high",
    });

    const [, init] = fetchMock.mock.calls[0] as unknown as [
      string,
      RequestInit,
    ];
    const body = JSON.parse(String(init.body)) as Record<string, unknown>;
    expect(body).toMatchObject({
      target_provider: "gateway",
      target_model: "model-a",
      target_reasoning_effort: "high",
    });
    expect(body).not.toHaveProperty("provider");
    expect(body).not.toHaveProperty("model");
    expect(body).not.toHaveProperty("reasoning_effort");
  });
});

describe("normalizeScheduleRecord route overrides", () => {
  it("accepts target, PascalCase, and legacy route field names", () => {
    const cases = [
      {
        raw: {
          target_provider: "target-provider",
          target_model: "target-model",
          target_reasoning_effort: "high",
        },
        want: {
          provider: "target-provider",
          model: "target-model",
          reasoningEffort: "high",
        },
      },
      {
        raw: {
          TargetProvider: "pascal-provider",
          TargetModel: "pascal-model",
          TargetReasoningEffort: "medium",
        },
        want: {
          provider: "pascal-provider",
          model: "pascal-model",
          reasoningEffort: "medium",
        },
      },
      {
        raw: {
          provider: "legacy-provider",
          model: "legacy-model",
          reasoningEffort: "low",
        },
        want: {
          provider: "legacy-provider",
          model: "legacy-model",
          reasoningEffort: "low",
        },
      },
      {
        raw: {
          provider_name: "named-provider",
          model: "named-model",
          reasoning_effort: "legacy-snake-effort",
        },
        want: {
          provider: "named-provider",
          model: "named-model",
          reasoningEffort: "legacy-snake-effort",
        },
      },
    ];

    for (const { raw, want } of cases) {
      expect(normalizeScheduleRecord(raw)).toMatchObject(want);
    }
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

describe("schedule template API", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  const templateRequest = {
    id: "weekly-review",
    title: "Weekly review",
    description: "Review the week",
    taskTitle: "Review",
    category: "reporting",
    prompt: "Review recent work",
    kind: "weekly" as const,
    amount: "",
    atTime: "",
    clockTime: "17:00",
    timeZone: "Asia/Shanghai",
    weekdays: [5],
    cron: "",
    goal: "Create a useful review",
    plan: "Summarize work\nList next actions",
    targetKind: "headless" as const,
    targetWorkspace: "",
    targetProfile: "",
    provider: "",
    model: "",
    reasoningEffort: "",
  };

  it("normalizes camelCase, PascalCase, and snake_case fields", () => {
    expect(
      normalizeScheduleTemplate({
        ID: "one",
        Title: "Pascal template",
        TaskTitle: "Task",
        Kind: "weekly",
        Weekdays: "[1,5]",
        Plan: ["First", "Second"],
        TargetKind: "workspace",
        TargetWorkspace: "E:/work",
        TargetProvider: "provider-a",
        TargetModel: "model-a",
        TargetReasoningEffort: "high",
      }),
    ).toMatchObject({
      id: "one",
      title: "Pascal template",
      taskTitle: "Task",
      kind: "weekly",
      weekdays: [1, 5],
      plan: "First\nSecond",
      targetKind: "workspace",
      targetWorkspace: "E:/work",
      provider: "provider-a",
      model: "model-a",
      reasoningEffort: "high",
    });

    expect(
      normalizeScheduleTemplate({
        id: "two",
        task_title: "Snake task",
        time_zone: "UTC",
        weekdays_json: "[2,4]",
        target_kind: "headless",
        target_workspace: "/workspace",
        target_profile: "nightly",
        target_provider: "provider-b",
        target_model: "model-b",
        target_reasoning_effort: "medium",
        created_at: "2026-11-20T00:00:00Z",
      }),
    ).toMatchObject({
      id: "two",
      taskTitle: "Snake task",
      timeZone: "UTC",
      weekdays: [2, 4],
      targetKind: "headless",
      targetWorkspace: "/workspace",
      targetProfile: "nightly",
      provider: "provider-b",
      model: "model-b",
      reasoningEffort: "medium",
      createdAt: "2026-11-20T00:00:00Z",
    });
  });

  it("fetches and normalizes persisted templates", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) =>
      jsonResponse({
        templates: [{ ...templateRequest, createdAt: "2026-11-20T00:00:00Z" }],
        count: 1,
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const response = await fetchScheduleTemplates();

    expect(fetchMock).toHaveBeenCalledOnce();
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "/api/runtime/schedule/templates",
    );
    expect(response.count).toBe(1);
    expect(response.templates[0]).toMatchObject({
      id: "weekly-review",
      title: "Weekly review",
      weekdays: [5],
    });
  });

  it("sends create and update requests to the template endpoints", async () => {
    const fetchMock = vi.fn(async (_url: string, init?: RequestInit) => {
      const request = JSON.parse(String(init?.body));
      return jsonResponse({ template: request });
    });
    vi.stubGlobal("fetch", fetchMock);

    await createScheduleTemplate(templateRequest);
    await updateScheduleTemplate("template/with space", templateRequest);

    expect(fetchMock).toHaveBeenCalledTimes(2);
    const createCall = fetchMock.mock.calls[0] as unknown as [
      string,
      RequestInit,
    ];
    expect(createCall[0]).toBe("/api/runtime/schedule/templates");
    expect(createCall[1].method).toBe("POST");
    expect(JSON.parse(String(createCall[1].body))).toEqual(templateRequest);

    const updateCall = fetchMock.mock.calls[1] as unknown as [
      string,
      RequestInit,
    ];
    expect(updateCall[0]).toBe(
      "/api/runtime/schedule/templates/template%2Fwith%20space",
    );
    expect(updateCall[1].method).toBe("PUT");
    expect(JSON.parse(String(updateCall[1].body))).toEqual(templateRequest);
  });

  it("deletes a template and consumes the JSON deletion response", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) =>
      jsonResponse({ id: "weekly-review", deleted: true }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(deleteScheduleTemplate("weekly-review")).resolves.toEqual({
      id: "weekly-review",
      deleted: true,
    });
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "/api/runtime/schedule/templates/weekly-review",
    );
    expect((fetchMock.mock.calls[0]?.[1] as RequestInit).method).toBe("DELETE");
  });
});
