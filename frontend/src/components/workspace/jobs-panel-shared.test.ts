import { describe, expect, it } from "vitest";

import type { RuntimeJob, RuntimeJobStatus } from "@/types/runtime";

import {
  buildJobsReloadKey,
  formatJobDuration,
  formatJobTimestamp,
  isJobsRelevantRuntimeEvent,
  isLiveJobStatus,
  jobStatusLabelKey,
  jobsRelevantRuntimeEvents,
  liveJobStatuses,
  parseTimestamp,
  resolveJobElapsedMs,
  sortJobsByRecency,
  splitRuntimeJobs,
} from "@/components/workspace/jobs-panel-shared";

function makeJob(overrides: Partial<RuntimeJob> = {}): RuntimeJob {
  return {
    id: "job-1",
    sessionId: "sess-1",
    kind: "shell",
    command: "pnpm test",
    cwd: "E:/repo",
    priority: 0,
    restartPolicy: "never",
    status: "running",
    message: "",
    createdAt: "2026-09-13T10:00:00.000Z",
    startedAt: "2026-09-13T10:00:01.000Z",
    finishedAt: "",
    exitCode: null,
    logPath: "",
    ...overrides,
  };
}

describe("isLiveJobStatus / liveJobStatuses", () => {
  it("仅把 pending、running 视作进行中", () => {
    expect(liveJobStatuses).toEqual(["pending", "running"]);
    for (const status of ["pending", "running"] satisfies RuntimeJobStatus[]) {
      expect(isLiveJobStatus(status)).toBe(true);
    }
    for (const status of [
      "completed",
      "failed",
      "timed_out",
      "cancelled",
      "orphaned",
    ] satisfies RuntimeJobStatus[]) {
      expect(isLiveJobStatus(status)).toBe(false);
    }
  });
});

describe("sortJobsByRecency", () => {
  it("live 按创建时间升序（最早排队在前），settled 按结束时间倒序", () => {
    const liveLate = makeJob({
      id: "live-late",
      status: "running",
      createdAt: "2026-09-13T10:05:00.000Z",
    });
    const liveEarly = makeJob({
      id: "live-early",
      status: "pending",
      createdAt: "2026-09-13T10:01:00.000Z",
    });
    const settledOld = makeJob({
      id: "settled-old",
      status: "completed",
      finishedAt: "2026-09-13T10:02:00.000Z",
    });
    const settledNew = makeJob({
      id: "settled-new",
      status: "failed",
      finishedAt: "2026-09-13T10:09:00.000Z",
    });

    const sorted = sortJobsByRecency([
      settledNew,
      liveLate,
      settledOld,
      liveEarly,
    ]);

    expect(sorted.map((job) => job.id)).toEqual([
      "live-early",
      "live-late",
      "settled-new",
      "settled-old",
    ]);
  });

  it("时间缺失或非法时按 0 处理，同刻按 id 稳定排序", () => {
    const first = makeJob({
      id: "job-a",
      status: "completed",
      finishedAt: "",
      createdAt: "not-a-date",
    });
    const second = makeJob({
      id: "job-b",
      status: "completed",
      finishedAt: "",
      createdAt: "",
    });

    expect(sortJobsByRecency([second, first]).map((job) => job.id)).toEqual([
      "job-a",
      "job-b",
    ]);
  });

  it("不修改入参数组", () => {
    const jobs = [makeJob({ id: "job-b" }), makeJob({ id: "job-a" })];
    const snapshot = jobs.map((job) => job.id);

    sortJobsByRecency(jobs);

    expect(jobs.map((job) => job.id)).toEqual(snapshot);
  });
});

describe("splitRuntimeJobs", () => {
  it("按 live/settled 分区，并各自排序", () => {
    const running = makeJob({ id: "running", status: "running" });
    const completed = makeJob({
      id: "completed",
      status: "completed",
      finishedAt: "2026-09-13T11:00:00.000Z",
    });
    const failed = makeJob({
      id: "failed",
      status: "failed",
      finishedAt: "2026-09-13T12:00:00.000Z",
    });

    const { live, settled } = splitRuntimeJobs([completed, running, failed]);

    expect(live.map((job) => job.id)).toEqual(["running"]);
    expect(settled.map((job) => job.id)).toEqual(["failed", "completed"]);
  });
});

describe("resolveJobElapsedMs", () => {
  const now = Date.parse("2026-09-13T10:00:10.000Z");

  it("live 用 startedAt 到 now 计算", () => {
    const job = makeJob({
      status: "running",
      startedAt: "2026-09-13T10:00:05.000Z",
    });
    expect(resolveJobElapsedMs(job, now)).toBe(5_000);
  });

  it("live 缺少 startedAt 时回落 createdAt", () => {
    const job = makeJob({
      status: "pending",
      startedAt: "",
      createdAt: "2026-09-13T10:00:00.000Z",
    });
    expect(resolveJobElapsedMs(job, now)).toBe(10_000);
  });

  it("settled 用 finishedAt 结算，缺时间戳时回落 now 但不为负", () => {
    const finished = makeJob({
      status: "completed",
      finishedAt: "2026-09-13T10:00:07.000Z",
    });
    expect(resolveJobElapsedMs(finished, now)).toBe(6_000);

    const missing = makeJob({ status: "failed", finishedAt: "" });
    expect(resolveJobElapsedMs(missing, now)).toBe(9_000);

    const skewed = makeJob({
      status: "completed",
      startedAt: "2026-09-13T10:00:30.000Z",
      finishedAt: "2026-09-13T10:00:20.000Z",
    });
    expect(resolveJobElapsedMs(skewed, now)).toBe(0);
  });

  it("起始时间戳缺失/非法时返回 null", () => {
    expect(
      resolveJobElapsedMs(makeJob({ startedAt: "", createdAt: "" }), now),
    ).toBeNull();
    expect(
      resolveJobElapsedMs(
        makeJob({ startedAt: "", createdAt: "garbage" }),
        now,
      ),
    ).toBeNull();
  });
});

describe("parseTimestamp", () => {
  it("空串与非法值返回 null，合法 ISO 返回毫秒", () => {
    expect(parseTimestamp("")).toBeNull();
    expect(parseTimestamp("not-a-date")).toBeNull();
    expect(parseTimestamp("2026-09-13T10:00:00.000Z")).toBe(
      Date.parse("2026-09-13T10:00:00.000Z"),
    );
  });
});

describe("formatJobDuration", () => {
  it("按秒/分/时格式化，null 显示占位符", () => {
    expect(formatJobDuration(null)).toBe("--");
    expect(formatJobDuration(0)).toBe("0s");
    expect(formatJobDuration(59_999)).toBe("59s");
    expect(formatJobDuration(60_000)).toBe("1m 00s");
    expect(formatJobDuration(65_000)).toBe("1m 05s");
    expect(formatJobDuration(3_600_000)).toBe("1h 00m");
    expect(formatJobDuration(3_725_000)).toBe("1h 02m");
  });
});

describe("formatJobTimestamp", () => {
  it("合法时间戳本地化展示，非法值显示占位符", () => {
    const iso = "2026-09-13T10:00:00.000Z";
    expect(formatJobTimestamp(iso)).toBe(new Date(Date.parse(iso)).toLocaleString());
    expect(formatJobTimestamp("")).toBe("--");
    expect(formatJobTimestamp("nope")).toBe("--");
  });
});

describe("isJobsRelevantRuntimeEvent", () => {
  it("大小写与空白不敏感地识别 job 事件", () => {
    for (const type of jobsRelevantRuntimeEvents) {
      expect(isJobsRelevantRuntimeEvent(type)).toBe(true);
    }
    expect(isJobsRelevantRuntimeEvent("  JOB_STARTED ")).toBe(true);
    expect(isJobsRelevantRuntimeEvent("message_delta")).toBe(false);
    expect(isJobsRelevantRuntimeEvent("")).toBe(false);
    expect(isJobsRelevantRuntimeEvent(undefined)).toBe(false);
  });
});

describe("buildJobsReloadKey", () => {
  it("仅相关事件生成键，count 参与去重", () => {
    expect(buildJobsReloadKey("job_started", 3)).toBe("job_started:3");
    expect(buildJobsReloadKey("job_started", undefined)).toBe("job_started:0");
    expect(buildJobsReloadKey("job_finished", 0)).toBe("job_finished:0");
    expect(buildJobsReloadKey("assistant_message", 9)).toBe("");
    expect(buildJobsReloadKey(undefined, 1)).toBe("");
  });
});

describe("jobStatusLabelKey", () => {
  it("输出 i18n 键后缀", () => {
    expect(jobStatusLabelKey("timed_out")).toBe("panels.jobs.status.timed_out");
  });
});
