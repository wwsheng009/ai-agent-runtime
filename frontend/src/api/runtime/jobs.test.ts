import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  cancelRuntimeJob,
  getRuntimeJob,
  getRuntimeJobOutput,
  listRuntimeJobEvents,
  listRuntimeJobs,
  normalizeRuntimeJob,
  normalizeRuntimeJobEvent,
  normalizeRuntimeJobOutput,
  normalizeRuntimeJobStatus,
} from "@/api/runtime/jobs";

describe("normalizeRuntimeJobStatus", () => {
  it("已知状态直接透传（大小写/空白不敏感）", () => {
    expect(normalizeRuntimeJobStatus("RUNNING")).toBe("running");
    expect(normalizeRuntimeJobStatus(" timed_out ")).toBe("timed_out");
    expect(normalizeRuntimeJobStatus("orphaned")).toBe("orphaned");
  });

  it("未知或非字符串回落 pending", () => {
    expect(normalizeRuntimeJobStatus("weird")).toBe("pending");
    expect(normalizeRuntimeJobStatus(undefined)).toBe("pending");
    expect(normalizeRuntimeJobStatus(7)).toBe("pending");
  });
});

describe("runtime job normalize", () => {
  it("PascalCase（后端 Go 字段名）可解析", () => {
    expect(
      normalizeRuntimeJob({
        ID: "job-9",
        SessionID: "sess-2",
        Kind: "shell",
        Command: "go test ./...",
        Cwd: "E:/repo",
        Priority: 3,
        RestartPolicy: "on_failure",
        Status: "FAILED",
        CreatedAt: "2026-09-13T10:00:00Z",
        StartedAt: "2026-09-13T10:00:01Z",
        FinishedAt: "2026-09-13T10:00:09Z",
        ExitCode: 2,
        LogPath: "E:/logs/job-9.log",
      }),
    ).toMatchObject({
      id: "job-9",
      sessionId: "sess-2",
      status: "failed",
      priority: 3,
      exitCode: 2,
      logPath: "E:/logs/job-9.log",
    });
  });
});

describe("listRuntimeJobs", () => {
  const originalFetch = globalThis.fetch;
  let calls: Array<{ url: string; init?: RequestInit }> = [];

  function respondWith(body: unknown, status = 200) {
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: String(input), init });
      return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    }) as typeof fetch;
  }

  beforeEach(() => {
    calls = [];
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("拼装 session_id/status/limit/offset 并归一化 jobs/count", async () => {
    respondWith({
      jobs: [
        { ID: "job-1", Status: "running", Command: "sleep 1" },
        { id: "job-2", status: "completed", exit_code: 0 },
      ],
      count: 2,
    });

    const result = await listRuntimeJobs({
      sessionId: "sess 1",
      status: "pending,running",
      limit: 50,
      offset: 10,
    });

    expect(calls).toHaveLength(1);
    const url = new URL(calls[0].url, "http://runtime.test");
    expect(url.pathname).toBe("/api/runtime/background/jobs");
    expect(url.searchParams.get("session_id")).toBe("sess 1");
    expect(url.searchParams.get("status")).toBe("pending,running");
    expect(url.searchParams.get("limit")).toBe("50");
    expect(url.searchParams.get("offset")).toBe("10");

    expect(result.count).toBe(2);
    expect(result.jobs.map((job) => job.id)).toEqual(["job-1", "job-2"]);
    expect(result.jobs[1]).toMatchObject({ status: "completed", exitCode: 0 });
  });

  it("缺省查询不带空参数，异常响应结构回落空列表", async () => {
    respondWith({});

    const result = await listRuntimeJobs();

    const url = new URL(calls[0].url, "http://runtime.test");
    expect(url.search).toBe("");
    expect(result).toEqual({ jobs: [], count: 0 });
  });

  it("非 2xx 抛出含后端 message 的错误", async () => {
    respondWith({ error: "jobs backend unavailable" }, 503);

    await expect(listRuntimeJobs()).rejects.toThrow(/jobs backend unavailable/);
  });
});

describe("runtime job detail/cancel/events/output", () => {
  const originalFetch = globalThis.fetch;
  let calls: Array<{ url: string; init?: RequestInit }> = [];

  function respondWith(body: unknown, status = 200) {
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: String(input), init });
      return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    }) as typeof fetch;
  }

  beforeEach(() => {
    calls = [];
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("getRuntimeJob 解包 {job} 并转义 id", async () => {
    respondWith({ job: { ID: "job/1", Status: "running" } });

    const job = await getRuntimeJob("job/1");

    expect(calls[0].url).toContain("/api/runtime/background/jobs/job%2F1");
    expect(job.id).toBe("job/1");
  });

  it("cancelRuntimeJob 用 POST 并回传最新 job", async () => {
    respondWith({ job: { ID: "job-1", Status: "CANCELLED" } });

    const job = await cancelRuntimeJob("job-1");

    expect(calls[0].url).toContain("/api/runtime/background/jobs/job-1/cancel");
    expect(calls[0].init?.method).toBe("POST");
    expect(job.status).toBe("cancelled");
  });

  it("listRuntimeJobEvents 归一化事件，count 缺失时回落 events.length", async () => {
    respondWith({
      events: [
        {
          seq: 4,
          job_id: "job-1",
          type: "job_output",
          payload: { chunk: "hello" },
          created_at: "2026-09-13T10:00:04Z",
        },
      ],
    });

    const result = await listRuntimeJobEvents("job-1", { after: 3, limit: 20 });

    const url = new URL(calls[0].url, "http://runtime.test");
    expect(url.searchParams.get("after")).toBe("3");
    expect(url.searchParams.get("limit")).toBe("20");
    expect(result.count).toBe(1);
    expect(result.events[0]).toEqual({
      seq: 4,
      jobId: "job-1",
      type: "job_output",
      payload: { chunk: "hello" },
      createdAt: "2026-09-13T10:00:04Z",
    });
  });

  it("getRuntimeJobOutput 解包 {output} 并带上 offset/limit", async () => {
    respondWith({
      output: {
        JobID: "job-1",
        Status: "running",
        Output: "line-1\n",
        NextOffset: 7,
        ExitCode: null,
      },
    });

    const output = await getRuntimeJobOutput("job-1", { offset: 0, limit: 8192 });

    const url = new URL(calls[0].url, "http://runtime.test");
    expect(url.pathname).toBe("/api/runtime/background/jobs/job-1/output");
    expect(url.searchParams.get("limit")).toBe("8192");
    expect(output).toMatchObject({
      jobId: "job-1",
      output: "line-1\n",
      nextOffset: 7,
      exitCode: null,
    });
  });
});

describe("normalizeRuntimeJobEvent / normalizeRuntimeJobOutput 容错", () => {
  it("非对象输入回落空结构", () => {
    expect(normalizeRuntimeJobEvent(null)).toEqual({
      seq: 0,
      jobId: "",
      type: "",
      payload: null,
      createdAt: "",
    });
    expect(normalizeRuntimeJobOutput("nope")).toEqual({
      jobId: "",
      status: "",
      output: "",
      nextOffset: 0,
      exitCode: null,
      message: "",
      errorCode: "",
    });
  });
});
