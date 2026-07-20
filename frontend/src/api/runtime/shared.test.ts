import { describe, expect, it } from "vitest";

import {
  buildErrorMessage,
  isRuntimeApiErrorCode,
  RuntimeApiError,
} from "@/api/runtime/shared";

describe("runtime shared helpers", () => {
  it("appends the request id to backend errors", () => {
    expect(
      buildErrorMessage(503, {
        error: 'HTTP 503: {"error":{"message":"Service temporarily unavailable","type":"api_error"}}',
        request_id: "trace_123",
      }),
    ).toBe(
      'HTTP 503: {"error":{"message":"Service temporarily unavailable","type":"api_error"}} (request_id: trace_123)',
    );
  });

  it("uses the request id even when the backend omits an explicit error message", () => {
    expect(
      buildErrorMessage(500, {
        request_id: "trace_456",
      }),
    ).toBe("runtime request failed with status 500 (request_id: trace_456)");
  });

  it("explains which runtime owns a session lease and how to recover", () => {
    expect(
      buildErrorMessage(409, {
        error: "[SESSION_LEASE_CONFLICT] session runtime lease conflict",
        code: "SESSION_LEASE_CONFLICT",
        context: {
          lease: {
            owner_id: "aicli-actor:host-a:22344:session-1",
            owner_kind: "aicli-actor",
            pid: 22344,
            hostname: "host-a",
            expires_at: "2026-07-16T11:06:49Z",
          },
          suggested_action:
            "continue in the owning aicli process, exit it before retrying here, or launch aicli with --runtime-server auto",
        },
        request_id: "trace_lease",
      }),
    ).toBe(
      "This session is currently active in a local aicli process (PID 22344, host host-a). Continue in the owning aicli process, exit it before retrying here, or launch aicli with --runtime-server auto. Current lease expiry: 2026-07-16T11:06:49Z. (request_id: trace_lease)",
    );
  });

  it("preserves a structured runtime error code for callers", () => {
    const error = new RuntimeApiError(409, {
      code: "SESSION_LEASE_CONFLICT",
      error: "lease conflict",
    });

    expect(error.status).toBe(409);
    expect(isRuntimeApiErrorCode(error, "SESSION_LEASE_CONFLICT")).toBe(true);
    expect(isRuntimeApiErrorCode(error, "VALIDATION_FAILED")).toBe(false);
  });
});
