import { describe, expect, it } from "vitest";

import {
  buildErrorMessage,
  getSessionLeaseConflictTitle,
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

  it("explains a CLI (aicli) lease owner and how to recover", () => {
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
      "This session is currently active in CLI (aicli) (PID 22344, host host-a). Continue in the owning aicli process, exit it before retrying here, or launch aicli with --runtime-server auto. Current lease expiry: 2026-07-16T11:06:49Z. (request_id: trace_lease)",
    );
  });

  it("labels a web (agent chat) owner and includes the remaining lease time", () => {
    const future = new Date(Date.now() + 60_000).toISOString();
    const message = buildErrorMessage(409, {
      error: "[SESSION_LEASE_CONFLICT] session runtime lease conflict",
      code: "SESSION_LEASE_CONFLICT",
      context: {
        lease: {
          owner_id: "runtime-server-agent-chat:host-a:30008:req-1",
          owner_kind: "runtime-server-agent-chat",
          pid: 30008,
          hostname: "host-a",
          expires_at: future,
        },
        suggested_action:
          "wait for the current session owner to release the lease, then retry",
      },
    });
    expect(message).toContain("web (agent chat) (PID 30008, host host-a)");
    expect(message).toMatch(/Current lease expires in about \d+s \(at .+\)\./);
  });

  it("derives short owner titles for lease conflicts", () => {
    const cliError = new RuntimeApiError(409, {
      code: "SESSION_LEASE_CONFLICT",
      context: { lease: { owner_kind: "aicli-actor" } },
    });
    const webError = new RuntimeApiError(409, {
      code: "SESSION_LEASE_CONFLICT",
      context: { lease: { owner_kind: "runtime-server-agent-chat" } },
    });
    const runtimeError = new RuntimeApiError(409, {
      code: "SESSION_LEASE_CONFLICT",
      context: { lease: { owner_kind: "runtime-server-actor" } },
    });
    const otherError = new Error("unrelated");

    expect(getSessionLeaseConflictTitle(cliError)).toBe("This session is in use by a CLI session");
    expect(getSessionLeaseConflictTitle(webError)).toBe("This session is in use by the web app");
    expect(getSessionLeaseConflictTitle(runtimeError)).toBe("This session is in use by the runtime");
    expect(getSessionLeaseConflictTitle(otherError)).toBeUndefined();
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
