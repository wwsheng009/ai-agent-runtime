// @vitest-environment jsdom

// F-4d：Artifact Flow 观测面板测试。
// 覆盖：normalize 表驱动（缺块/缺字段折叠、非零过滤、null 拒绝）+
// 面板渲染（降级矩阵：null 不渲染、全零空态、flags 警告、gap 警告、正常计数）。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import {
  normalizeArtifactFlow,
  normalizeToolEfficiencySnapshot,
} from "@/api/runtime/analytics";
import type { AnalyticsToolEfficiencySnapshot } from "@/types/runtime";

import { ArtifactFlowPanel } from "./artifact-flow-panel";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function snapshotFixture(): AnalyticsToolEfficiencySnapshot {
  return {
    captured_at: "2026-09-19T00:00:00Z",
    preflight: {
      total: 10,
      allow: 9,
      deny: 1,
      allow_rate: 0.9,
      by_reason: {},
      by_decision: { allow: 9, deny: 1 },
    },
    outcomes: {
      total: 10,
      by_outcome: { success: 8, error: 2 },
      by_error_code: { tool_timeout: 2 },
      success_rate: 0.8,
      non_fail_rate: 0.9,
    },
    disposition_replays: {
      total: 1,
      by_outcome: { replayed: 1 },
      by_repeat: { once: 1 },
    },
    artifact_flow: {
      archives: {
        total: 6,
        by_layer: { l2_tool_result: 4, l3_gateway_archive: 2 },
        by_disposition: { archived: 5, pointer: 1 },
      },
      truncations: {
        total: 4,
        by_layer: { l4_render: 2, l1_model_envelope: 2 },
        by_truncated_by: { model_context: 3, render_cap: 1 },
      },
      pointer_notice: { archived: 1 },
      deref: {
        total: 3,
        followup_ratio: 0.6667,
        miss_by_reason: { not_found: 1 },
      },
      l1_l4_gap_ratio: 0.5,
    },
    fail_categories: { timeout: 2 },
    inefficiency_flags: ["artifact_l1_l4_gap_high"],
  };
}

describe("normalizeToolEfficiencySnapshot 表驱动", () => {
  it("null/undefined/非对象/数组 → null", () => {
    expect(normalizeToolEfficiencySnapshot(null)).toBeNull();
    expect(normalizeToolEfficiencySnapshot(undefined)).toBeNull();
    expect(normalizeToolEfficiencySnapshot("x")).toBeNull();
    expect(normalizeToolEfficiencySnapshot([])).toBeNull();
  });

  it("缺 captured_at 或 artifact_flow → null（不伪造空快照）", () => {
    expect(normalizeToolEfficiencySnapshot({})).toBeNull();
    expect(
      normalizeToolEfficiencySnapshot({ captured_at: "2026-09-19T00:00:00Z" }),
    ).toBeNull();
  });

  it("缺子块折叠为零值而不是拒绝", () => {
    const snap = normalizeToolEfficiencySnapshot({
      captured_at: "2026-09-19T00:00:00Z",
      artifact_flow: {},
    });
    expect(snap).not.toBeNull();
    expect(snap!.artifact_flow.archives.total).toBe(0);
    expect(snap!.artifact_flow.truncations.by_layer).toEqual({});
    expect(snap!.artifact_flow.deref.followup_ratio).toBe(0);
    expect(snap!.inefficiency_flags).toEqual([]);
  });

  it("normalizeArtifactFlow：非零过滤 + 缺块容忍", () => {
    const flow = normalizeArtifactFlow({
      archives: { total: 3, by_layer: { l2_tool_result: 3 }, junk: "x" },
      pointer_notice: { archived: 2, dropped: 0 },
    });
    expect(flow).not.toBeNull();
    expect(flow!.archives.total).toBe(3);
    expect(flow!.archives.by_layer).toEqual({ l2_tool_result: 3 });
    expect(flow!.pointer_notice).toEqual({ archived: 2 });
    expect(flow!.truncations.total).toBe(0);
    expect(flow!.l1_l4_gap_ratio).toBe(0);
  });

  it("normalizeArtifactFlow：非对象 → null", () => {
    expect(normalizeArtifactFlow(null)).toBeNull();
    expect(normalizeArtifactFlow("flow")).toBeNull();
    expect(normalizeArtifactFlow(42)).toBeNull();
  });
});

describe("ArtifactFlowPanel 降级矩阵", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function render(ui: React.ReactElement) {
    act(() => {
      root.render(ui);
    });
  }

  it("快照 null → 整块不渲染（不伪造零计数）", () => {
    render(<ArtifactFlowPanel snapshot={null} loading={false} />);
    expect(container.querySelector('[data-testid="artifact-flow-panel"]')).toBeNull();
    expect(container.textContent).toBe("");
  });

  it("loading 且无快照 → loading 占位", () => {
    render(<ArtifactFlowPanel snapshot={null} loading={true} />);
    expect(container.querySelector('[data-testid="artifact-flow-loading"]')).not.toBeNull();
  });

  it("全零快照 → 空态", () => {
    const empty = normalizeToolEfficiencySnapshot({
      captured_at: "2026-09-19T00:00:00Z",
      artifact_flow: {},
    })!;
    render(<ArtifactFlowPanel snapshot={empty} loading={false} />);
    expect(container.querySelector('[data-testid="artifact-flow-empty"]')).not.toBeNull();
    expect(container.textContent).toContain("暂无数据");
  });

  it("正常快照：四块计数 + flags 警告 + gap 警告脚注", () => {
    render(<ArtifactFlowPanel snapshot={snapshotFixture()} loading={false} />);
    expect(container.querySelector('[data-testid="artifact-flow-panel"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="artifact-flow-archives"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="artifact-flow-truncations"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="artifact-flow-pointer"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="artifact-flow-deref"]')).not.toBeNull();

    // 计数渲染（tabular 数字）。
    expect(container.textContent).toContain("Artifact 链路观测");
    expect(container.textContent).toContain("6");
    expect(container.textContent).toContain("4");
    expect(container.textContent).toContain("3");

    // inefficiency_flags 非空 → 警告条。
    const flags = container.querySelector('[data-testid="artifact-flow-flags"]');
    expect(flags).not.toBeNull();
    expect(flags!.textContent).toContain("artifact_l1_l4_gap_high");

    // l1_l4_gap_ratio = 0.5 ≥ 0.5 → 截断卡警告脚注（L1/L4 竞争比例）。
    expect(container.textContent).toContain("L1/L4 竞争比例 50.0%");
  });

  it("无 flags 无 gap → 不渲染警告条", () => {
    const calm = snapshotFixture();
    calm.inefficiency_flags = [];
    calm.artifact_flow.l1_l4_gap_ratio = 0;
    render(<ArtifactFlowPanel snapshot={calm} loading={false} />);
    expect(container.querySelector('[data-testid="artifact-flow-flags"]')).toBeNull();
    expect(container.textContent).not.toContain("L1/L4 竞争比例");
  });
});
