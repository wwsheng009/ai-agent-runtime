import { describe, expect, it, vi } from "vitest";

import {
  ingestJobOutputEvent,
  jobOutputSignal,
  resetJobOutputStreams,
  subscribeJobOutput,
} from "./store";

describe("job output stream store", () => {
  it("broadcasts job_output increments and ignores stale/other events", () => {
    resetJobOutputStreams();
    const listener = vi.fn();
    const unsubscribe = subscribeJobOutput("job-1", listener);

    // Non job_output events are ignored.
    ingestJobOutputEvent({ type: "job_started", payload: { job_id: "job-1" } });
    expect(listener).not.toHaveBeenCalled();

    // A new increment notifies and records the offset.
    ingestJobOutputEvent({
      type: "job_output",
      payload: { job_id: "job-1", offset: 0, next_offset: 5, size: 5 },
    });
    expect(listener).toHaveBeenCalledTimes(1);
    expect(jobOutputSignal("job-1")?.nextOffset).toBe(5);

    // Stale (next_offset does not advance) is dropped.
    ingestJobOutputEvent({
      type: "job_output",
      payload: { job_id: "job-1", offset: 0, next_offset: 5, size: 5 },
    });
    expect(listener).toHaveBeenCalledTimes(1);

    // A different job does not notify this listener.
    ingestJobOutputEvent({
      type: "job_output",
      payload: { job_id: "job-2", offset: 0, next_offset: 3, size: 3 },
    });
    expect(listener).toHaveBeenCalledTimes(1);

    // After unsubscribe no further notifications arrive.
    unsubscribe();
    ingestJobOutputEvent({
      type: "job_output",
      payload: { job_id: "job-1", offset: 5, next_offset: 9, size: 4 },
    });
    expect(listener).toHaveBeenCalledTimes(1);

    resetJobOutputStreams();
    expect(jobOutputSignal("job-1")).toBeUndefined();
  });
});
