import { describe, expect, it } from "vitest";

import type { RuntimeJob, RuntimeJobStatus } from "@/types/runtime";

import {
  buildJobsReloadKey,
  formatJobDuration,
  formatJobTimestamp,
  isJobsRelevantRuntimeEvent,
  isLiveJobStatus,
  isTerminalJobStatus,
  jobActionLabelKey,
  jobActionPendingLabelKey,
  jobAvailableActions,
  jobStatusLabelKey,
  jobsRelevantRuntimeEvents,
  liveJobStatuses,
  parseTimestamp,
  resolveJobElapsedMs,
  settledJobStatuses,
  sortJobsByRecency,
  splitRuntimeJobs,
  type JobAction,
} from "@/components/workspace/jobs-panel-shared";

/** 与 `RuntimeJobStatus` 联合类型一一对应；枚举扩展时本列表必须同步（防漏测）。 */
const allJobStatuses: RuntimeJobStatus[] = [
  "pending",
  "paused",
  "running",
  "completed",
  "failed",
  "timed_out",
  "cancelled",
  "orphaned",
  "interrupted",
  "expired",
  "abandoned",
];

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

describe("isLiveJobStatus / isTerminalJobStatus", () => {
  it("paused 归 live；interrupted/expired/abandoned 归终态", () => {
    expect(liveJobStatuses).toEqual(["pending", "running", "paused"]);
    expect(isLiveJobStatus("paused")).toBe(true);
    for (const status of [
      "completed",
      "failed",
      "timed_out",
      "cancelled",
      "orphaned",
      "interrupted",
      "expired",
      "abandoned",
    ] satisfies RuntimeJobStatus[]) {
      expect(isLiveJobStatus(status)).toBe(false);
      expect(isTerminalJobStatus(status)).toBe(true);
    }
  });

  it("每个 RuntimeJobStatus 恰好归入 live 或 settled 之一（枚举扩展防漏）", () => {
    for (const status of allJobStatuses) {
      const inLive = liveJobStatuses.includes(status);
      const inSettled = settledJobStatuses.includes(status);
      expect(inLive).toBe(!inSettled);
      expect(isLiveJobStatus(status)).toBe(inLive);
      expect(isTerminalJobStatus(status)).toBe(inSettled);
    }
    expect(new Set(allJobStatuses).size).toBe(
      liveJobStatuses.length + settledJobStatuses.length,
    );
  });
});

describe("jobAvailableActions", () => {
  it("按状态机矩阵返回动作（与后端端点约束一致）", () => {
    expect(jobAvailableActions("pending")).toEqual(["pause", "cancel", "abandon"]);
    expect(jobAvailableActions("paused")).toEqual(["resume", "cancel", "abandon"]);
    expect(jobAvailableActions("running")).toEqual(["cancel"]);
    for (const status of settledJobStatuses) {
      expect(jobAvailableActions(status)).toEqual(["requeue"]);
    }
  });

  it("全部状态都有动作（新增枚举不遗漏）", () => {
    for (const status of allJobStatuses) {
      expect(jobAvailableActions(status).length).toBeGreaterThan(0);
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

  it("paused 归 live，interrupted/expired/abandoned 归 settled", () => {
    const paused = makeJob({ id: "paused", status: "paused" });
    const interrupted = makeJob({
      id: "interrupted",
      status: "interrupted",
      finishedAt: "2026-09-13T12:00:00.000Z",
    });
    const expired = makeJob({
      id: "expired",
      status: "expired",
      finishedAt: "2026-09-13T13:00:00.000Z",
    });
    const abandoned = makeJob({
      id: "abandoned",
      status: "abandoned",
      finishedAt: "2026-09-13T11:00:00.000Z",
    });

    const { live, settled } = splitRuntimeJobs([
      interrupted,
      paused,
      expired,
      abandoned,
    ]);

    expect(live.map((job) => job.id)).toEqual(["paused"]);
    expect(settled.map((job) => job.id)).toEqual([
      "expired",
      "interrupted",
      "abandoned",
    ]);
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
    expect(jobsRelevantRuntimeEvents).toEqual(
      expect.arrayContaining([
        "job_paused",
        "job_resumed",
        "job_requeued",
        "job_abandoned",
      ]),
    );
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
    expect(buildJobsReloadKey("job_paused", 2)).toBe("job_paused:2");
    expect(buildJobsReloadKey("job_requeued", 5)).toBe("job_requeued:5");
    expect(buildJobsReloadKey("assistant_message", 9)).toBe("");
    expect(buildJobsReloadKey(undefined, 1)).toBe("");
  });
});

describe("jobStatusLabelKey", () => {
  it("输出 i18n 键后缀", () => {
    expect(jobStatusLabelKey("timed_out")).toBe("panels.jobs.status.timed_out");
    expect(jobStatusLabelKey("paused")).toBe("panels.jobs.status.paused");
    expect(jobStatusLabelKey("abandoned")).toBe("panels.jobs.status.abandoned");
  });
});

describe("jobActionLabelKey / jobActionPendingLabelKey", () => {
  it("动作标签与在途标签的 i18n 键（cancel 拼写为 cancelling）", () => {
    expect(jobActionLabelKey("pause")).toBe("panels.jobs.pause");
    expect(jobActionLabelKey("requeue")).toBe("panels.jobs.requeue");
    expect(jobActionPendingLabelKey("cancel")).toBe("panels.jobs.cancelling");
    expect(jobActionPendingLabelKey("resume")).toBe("panels.jobs.resuming");
    expect(jobActionPendingLabelKey("abandon")).toBe("panels.jobs.abandoning");

    for (const action of [
      "pause",
      "resume",
      "cancel",
      "abandon",
      "requeue",
    ] satisfies JobAction[]) {
      expect(jobActionLabelKey(action)).toBe(`panels.jobs.${action}`);
    }
  });
});
