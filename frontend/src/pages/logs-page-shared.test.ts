import { describe, expect, it } from "vitest";

import {
  buildRuntimeLogsActiveChips,
  buildRuntimeLogIdentifierRows,
  buildRuntimeLogLevelStats,
  buildRuntimeLogsShareState,
  isRuntimeLogIdentifierQueryActive,
  normalizeRuntimeLogLevelFilter,
  normalizeRuntimeLogLevel,
  readRuntimeLogsUrlState,
  writeRuntimeLogsUrlState,
} from "@/pages/logs-page-shared";
import type { RuntimeLogEntry } from "@/types/runtime";

describe("logs page shared helpers", () => {
  it("normalizes known and unknown log levels", () => {
    expect(normalizeRuntimeLogLevel("error")).toBe("error");
    expect(normalizeRuntimeLogLevel(" WARN ")).toBe("warn");
    expect(normalizeRuntimeLogLevel("trace")).toBe("other");
    expect(normalizeRuntimeLogLevel(undefined)).toBe("other");
    expect(normalizeRuntimeLogLevelFilter("debug")).toBe("debug");
    expect(normalizeRuntimeLogLevelFilter("other")).toBe("");
  });

  it("builds visible level stats for the current entry set", () => {
    const entries = [
      { cursor: 1, raw_text: "a", level: "error" },
      { cursor: 2, raw_text: "b", level: "error" },
      { cursor: 3, raw_text: "c", level: "warn" },
      { cursor: 4, raw_text: "d", level: "info" },
      { cursor: 5, raw_text: "e", level: "trace" },
    ] satisfies RuntimeLogEntry[];

    expect(buildRuntimeLogLevelStats(entries)).toEqual([
      { key: "error", label: "Error", shortLabel: "ERR", count: 2 },
      { key: "warn", label: "Warn", shortLabel: "WRN", count: 1 },
      { key: "info", label: "Info", shortLabel: "INF", count: 1 },
      { key: "debug", label: "Debug", shortLabel: "DBG", count: 0 },
      { key: "other", label: "Other", shortLabel: "LOG", count: 1 },
    ]);
  });

  it("extracts available identifiers in request-trace-session order", () => {
    expect(
      buildRuntimeLogIdentifierRows({
        cursor: 7,
        raw_text: "{}",
        request_id: "req_123",
        trace_id: "trace_456",
        session_id: "session_789",
      }),
    ).toEqual([
      { key: "request_id", label: "Request ID", value: "req_123" },
      { key: "trace_id", label: "Trace ID", value: "trace_456" },
      { key: "session_id", label: "Session ID", value: "session_789" },
    ]);
  });

  it("treats identifier filters as exact trimmed matches", () => {
    expect(isRuntimeLogIdentifierQueryActive("trace_123", "trace_123")).toBe(
      true,
    );
    expect(
      isRuntimeLogIdentifierQueryActive(" trace_123 ", "trace_123"),
    ).toBe(true);
    expect(isRuntimeLogIdentifierQueryActive("trace_123", "trace_999")).toBe(
      false,
    );
  });

  it("reads url state from runtime log search params", () => {
    const searchParams = new URLSearchParams(
      "query=trace_123&level=warn&follow=false&cursor=42",
    );

    expect(readRuntimeLogsUrlState(searchParams)).toEqual({
      query: "trace_123",
      level: "warn",
      follow: false,
      cursor: 42,
    });
  });

  it("writes url state while preserving unknown params and omitting defaults", () => {
    const currentSearchParams = new URLSearchParams("tab=live&follow=false");

    expect(
      writeRuntimeLogsUrlState(currentSearchParams, {
        query: " req_9 ",
        level: "error",
        follow: true,
        cursor: 128,
      }).toString(),
    ).toBe("tab=live&query=req_9&level=error&cursor=128");
  });

  it("builds active chips for non-default filters and pinned cursors", () => {
    expect(
      buildRuntimeLogsActiveChips(
        {
          query: "trace_2",
          level: "error",
          follow: false,
          cursor: 128,
        },
        256,
      ),
    ).toEqual([
      { key: "query", label: "搜索", value: "trace_2" },
      { key: "level", label: "级别", value: "error" },
      { key: "follow", label: "Follow", value: "off" },
      { key: "cursor", label: "Cursor", value: "128" },
    ]);
  });

  it("drops the latest cursor from shared links when follow is already on", () => {
    expect(
      buildRuntimeLogsShareState(
        {
          query: "req_1",
          level: "",
          follow: true,
          cursor: 512,
        },
        512,
      ),
    ).toEqual({
      query: "req_1",
      level: "",
      follow: true,
      cursor: null,
    });
  });
});
