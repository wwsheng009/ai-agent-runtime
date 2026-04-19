import { describe, expect, it } from "vitest";

import { buildErrorMessage } from "@/api/runtime/shared";

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
});
