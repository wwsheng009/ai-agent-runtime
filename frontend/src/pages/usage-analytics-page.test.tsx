import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";

import { buildAnalyticsGroupSelectionSearchParams } from "./usage-analytics-filters";
import { UsageAnalyticsPage } from "./usage-analytics-page";

describe("buildAnalyticsGroupSelectionSearchParams", () => {
  it("maps a day bucket to an exact date range instead of a log directory", () => {
    const current = new URLSearchParams("provider=openai&offset=50");

    const next = buildAnalyticsGroupSelectionSearchParams(
      current,
      "day",
      "2026-07-27",
    );

    expect(next.get("from")).toBe("2026-07-27");
    expect(next.get("to")).toBe("2026-07-27");
    expect(next.get("directory")).toBeNull();
    expect(next.get("provider")).toBe("openai");
    expect(next.get("offset")).toBeNull();
  });

  it("clears unknown dimension filters while preserving the remaining scope", () => {
    const current = new URLSearchParams("provider=openai&q=failed&offset=50");

    const next = buildAnalyticsGroupSelectionSearchParams(
      current,
      "provider",
      "(unknown)",
    );

    expect(next.get("provider")).toBeNull();
    expect(next.get("q")).toBe("failed");
    expect(next.get("offset")).toBeNull();
  });
});

describe("UsageAnalyticsPage", () => {
  it("renders the responsive analytics shell and accessible navigation", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/usage"]}>
        <Routes>
          <Route path="/usage" element={<UsageAnalyticsPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(markup).toContain("会话与总体用量");
    expect(markup).toContain("分析范围与筛选");
    expect(markup).toContain('aria-label="分析页面导航"');
    expect(markup).toContain('aria-label="日志"');
    expect(markup).toContain('aria-label="刷新"');
    expect(markup).toContain('href="/workspace/chats/new"');
    expect(markup).toContain("surface-panel");
  });
});
