import { describe, expect, it } from "vitest";

import {
  isNonFaultDiagnosticCategory,
  isNonFaultDiagnosticPattern,
} from "./failure-classification";

describe("failure classification", () => {
  it("只把 idle 围栏丢弃识别为非故障诊断", () => {
    expect(
      isNonFaultDiagnosticPattern({
        failure_category: "render_fence_drop_idle",
      }),
    ).toBe(true);
    // failure_category 缺失时回退稳定错误码。
    expect(
      isNonFaultDiagnosticPattern({
        error_code: "RENDER_FENCE_DROPPED_IDLE",
      }),
    ).toBe(true);
    // closed / active_mismatch 是真实围栏信号，不得降级为“非故障”。
    expect(
      isNonFaultDiagnosticPattern({
        failure_category: "render_fence_drop_closed",
      }),
    ).toBe(false);
    expect(
      isNonFaultDiagnosticPattern({
        failure_category: "render_fence_drop_idle",
      }),
    ).toBe(true);
    expect(isNonFaultDiagnosticCategory(" RENDER_FENCE_DROP_IDLE ")).toBe(true);
  });
});
