import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { LspObservabilityPanel } from "./lsp-observability-panel";

/**
 * 回归钉子：数据面读分析库（source="analytics_db"）时，scan.* 恒为 0。
 *
 * UI 过去会把这排成"文件 / 损坏: 0 / 0"和"未使用增量索引"。两条都误导：
 *   - "0 文件"会被读成"日志里没有数据"，实际是"这条路不扫日志"；
 *   - "未使用增量索引"暗示索引失灵，实际是压根没有索引可言。
 *
 * 这些是**展示层**断言：真正的口径在 baseline.Aggregate，这里只保证 UI 不
 * 主动编造一个它没测到的东西。
 */

const baselineMock = vi.fn();

vi.mock("@/api/runtime/lsp", () => ({
  fetchLspObserveFeed: vi.fn().mockResolvedValue({ events: [] }),
  getLspBaseline: (q: unknown) => baselineMock(q),
  LspObserveUnavailableError: class extends Error {},
}));

function baselineResponse(source: "analytics_db" | "chat_logs" | undefined) {
  return {
    schema_version: "3.3",
    generated_at: "2026-10-02T00:00:00Z",
    window: "all",
    ...(source ? { source } : {}),
    // analytics_db 路径下这些恒为 0，且增量索引字段缺席。
    scan: { files: 0, lines: 0, malformed: 0, skipped_old: 0, skipped_files: 0 },
    stats: { requests: 0 },
    rows: [],
    cache: { hit: false, age_seconds: 0, ttl_seconds: 60 },
  };
}

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

async function renderWith(source: "analytics_db" | "chat_logs" | undefined) {
  baselineMock.mockResolvedValue(baselineResponse(source));
  await act(async () => {
    root.render(
      <MemoryRouter>
        <LspObservabilityPanel />
      </MemoryRouter>,
    );
  });
  return document.body.textContent ?? "";
}

describe("LspObservabilityPanel 基线事实源措辞", () => {
  it("source=analytics_db 时说明数据源，且不谎称未使用索引", async () => {
    const text = await renderWith("analytics_db");
    expect(text).toContain("分析库");
    // 真相是"不扫日志"，不是"索引没起作用"。
    expect(text).not.toContain("未使用增量索引");
    // scan.* 恒为 0，不该被渲染成"扫了 0 个文件"。
    expect(text).not.toContain("文件 / 损坏");
  });

  it("source=chat_logs 时才渲染扫描量与索引可观测性", async () => {
    const text = await renderWith("chat_logs");
    expect(text).toContain("文件 / 损坏");
    expect(text).toContain("未使用增量索引");
  });

  it("老后端不返回 source 时退回中性措辞，不臆断事实源", async () => {
    const text = await renderWith(undefined);
    // 未声明 = 不知道。此时按"不扫日志"处理，但绝不能谎称未使用索引。
    expect(text).not.toContain("未使用增量索引");
  });
});
