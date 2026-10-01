// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { overviewMock, summaryMock, errorsMock } = vi.hoisted(() => ({
  overviewMock: vi.fn(),
  summaryMock: vi.fn(),
  errorsMock: vi.fn(),
}));
vi.mock("@/lib/runtime-api", () => ({ getAnalyticsOverview: overviewMock, getAnalyticsSummary: summaryMock }));
vi.mock("@/api/runtime/analytics", () => ({
  getAnalyticsSubagents: vi.fn(async () => ({ summary: null })),
  listAnalyticsErrors: errorsMock,
  getUsageAnalyticsHealth: vi.fn(async () => null),
  getToolEfficiencySnapshot: vi.fn(async () => null),
}));
vi.mock("@/pages/usage-analytics-charts", () => ({ UsageAnalyticsCharts: () => <div data-testid="overview-chart" /> }));
vi.mock("./provider-model-analysis", () => ({
  ProviderModelAnalysis: ({ onSelectProvider }: { onSelectProvider: (value: string) => void }) => (
    <button data-testid="model-chart" onClick={() => onSelectProvider("selected-provider")}>select provider</button>
  ),
}));
vi.mock("./quota", () => ({ UsageQuotaPanel: () => <div data-testid="quota-panel" /> }));
vi.mock("./routing-observability-panel", () => ({ RoutingObservabilityPanel: () => <div data-testid="routing-panel" /> }));
vi.mock("./lsp-observability-panel", () => ({ LspObservabilityPanel: () => <div data-testid="lsp-panel" /> }));

import { UsageOverview } from "./overview";
import { AnalyticsHeader } from "./primitives";
import { emptyCoverage, emptyDimensions, emptyTotals } from "./defaults";
import { overviewViews, resetOverviewFilters, resolveOverviewView, selectOverviewView } from "./overview-navigation";

function LocationProbe() {
  const location = useLocation();
  const navigate = useNavigate();
  return <><output data-testid="location">{location.search}</output><button data-testid="back" onClick={() => navigate(-1)}>back</button></>;
}

describe("usage overview categories", () => {
  let container: HTMLDivElement;
  let root: Root;
  const originalScrollIntoView = Element.prototype.scrollIntoView;

  beforeEach(() => {
    (globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;
    vi.useFakeTimers();
    vi.clearAllMocks();
    window.localStorage.clear();
    overviewMock.mockResolvedValue({
      sessions: { sessions: [], totals: emptyTotals, coverage: emptyCoverage, total: 120, scanned: 120 },
      summary: { groups: [], totals: emptyTotals, coverage: emptyCoverage },
      dimensions: emptyDimensions,
    });
    summaryMock.mockResolvedValue({ groups: [] });
    errorsMock.mockResolvedValue({ patterns: [] });
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    Element.prototype.scrollIntoView = vi.fn();
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    Element.prototype.scrollIntoView = originalScrollIntoView;
    vi.useRealTimers();
    delete (globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT;
  });

  async function mount(entry = "/usage") {
    await act(async () => {
      root.render(<MemoryRouter initialEntries={[entry]}><LocationProbe /><Routes><Route path="/usage" element={<UsageOverview />} /></Routes></MemoryRouter>);
    });
    await act(async () => { await vi.advanceTimersByTimeAsync(300); });
  }

  async function click(element: Element | null) {
    expect(element).not.toBeNull();
    await act(async () => { element?.dispatchEvent(new MouseEvent("click", { bubbles: true })); });
  }

  const params = () => new URLSearchParams(container.querySelector('[data-testid="location"]')?.textContent ?? "");
  const selected = () => container.querySelector('[role="tab"][aria-selected="true"]')?.id;

  it("keeps the shared filters outside all tabs and renders only the selected category", async () => {
    await mount();
    const form = container.querySelector('form[aria-label="分析范围与筛选"]');
    expect(form).not.toBeNull();
    expect(form?.closest('[role="tabpanel"]')).toBeNull();
    expect(container.querySelectorAll('[role="tab"]')).toHaveLength(7);
    expect(container.querySelector('[data-testid="overview-chart"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="quota-panel"]')).toBeNull();
    expect(container.querySelector('[data-testid="routing-panel"]')).toBeNull();
    expect(container.querySelector("#usage-sessions-title")).toBeNull();

    for (const view of overviewViews) {
      await click(container.querySelector(`#usage-view-${view}-tab`));
      expect(container.querySelector("form")).toBe(form);
      expect(container.querySelectorAll('[role="tabpanel"]')).toHaveLength(1);
      const panel = container.querySelector('[role="tabpanel"]');
      expect(panel?.id).toBe(`usage-view-${view}-panel`);
      expect(panel?.getAttribute("aria-labelledby")).toBe(`usage-view-${view}-tab`);
      expect(container.querySelectorAll('[role="tab"][tabindex="0"]')).toHaveLength(1);
      expect(Boolean(container.querySelector('[data-testid="overview-chart"]'))).toBe(view === "overview");
      expect(Boolean(container.querySelector('[data-testid="model-chart"]'))).toBe(view === "models");
      expect(Boolean(container.querySelector("#usage-sessions-title"))).toBe(view === "sessions");
      expect(Boolean(container.querySelector('[data-testid="quota-panel"]'))).toBe(view === "quota");
      expect(Boolean(container.querySelector('[data-testid="routing-panel"]'))).toBe(view === "routing");
      expect(Boolean(container.querySelector('[data-testid="lsp-panel"]'))).toBe(view === "lsp");
    }
    expect(container.querySelector('[role="status"]')?.textContent).toContain("暂时无法读取工件流快照");
    // 分类变化不应成为主分析请求的依赖。
    expect(overviewMock).toHaveBeenCalledTimes(1);
    expect(summaryMock).toHaveBeenCalledTimes(2);
  });

  it("keeps every filter accessible in the compact grid, including grouped dates and the password field", async () => {
    await mount("/usage?view=models&q=needle&from=2026-09-01&to=2026-09-30&group_by=provider");
    const form = container.querySelector("form");
    expect(form?.querySelectorAll('input, button[aria-haspopup="listbox"]')).toHaveLength(10);
    const range = form?.querySelector('[role="group"][aria-label="日期范围"]');
    const dates = range?.querySelectorAll<HTMLInputElement>('input[type="date"]');
    expect(dates).toHaveLength(2);
    expect(dates?.[0]?.value).toBe("2026-09-01");
    expect(dates?.[1]?.value).toBe("2026-09-30");
    expect(dates?.[0]?.closest("label")?.textContent).toBe("开始日期");
    expect(dates?.[1]?.closest("label")?.textContent).toBe("结束日期");
    expect(form?.querySelector('button[aria-label="分组"]')?.textContent).toContain("Provider");
    const token = form?.querySelector('input[type="password"]');
    expect(token?.getAttribute("autocomplete")).toBe("off");
    expect(token?.closest("label")?.textContent).toContain("Admin token");
    expect(token?.closest("label")?.classList.contains("h-8")).toBe(true);
  });

  it("scopes the failure category distribution to the active filters and refetches when they change", async () => {
    await mount("/usage?q=needle&provider=openai&model=gpt-5.4&from=2026-09-01&to=2026-09-30&status=error&directory=E%3A%2Fproj&project=E%3A%2Fproj%2Fapp");
    expect(errorsMock).toHaveBeenCalledWith(expect.objectContaining({
      q: "needle",
      provider: "openai",
      model: "gpt-5.4",
      from: "2026-09-01",
      to: "2026-09-30",
      status: "error",
      directory: "E:/proj",
      project: "E:/proj/app",
      top: 10,
    }));

    // 改搜索词 → 失败分类分布必须按新筛选重取，而不是停留在首次快照。
    const search = container.querySelector<HTMLInputElement>('form input[type="text"]');
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(search, "updated");
      search?.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => { await vi.advanceTimersByTimeAsync(300); });
    expect(errorsMock).toHaveBeenLastCalledWith(expect.objectContaining({ q: "updated", provider: "openai", from: "2026-09-01" }));
  });

  it("preserves filters and pagination across tabs and resets filters without changing the category", async () => {
    await mount("/usage?view=sessions&q=needle&provider=openai&from=2026-09-01&offset=50");
    expect(selected()).toBe("usage-view-sessions-tab");
    const search = container.querySelector<HTMLInputElement>('form input[type="text"]');
    expect(search?.value).toBe("needle");
    await click(container.querySelector("#usage-view-models-tab"));
    expect(params().get("provider")).toBe("openai");
    expect(params().get("from")).toBe("2026-09-01");
    expect(params().get("offset")).toBe("50");
    expect(search?.value).toBe("needle");
    expect(overviewMock).toHaveBeenCalledTimes(1);

    const reset = Array.from(container.querySelectorAll("form button")).find((button) => button.textContent?.includes("重置筛选"));
    await click(reset ?? null);
    expect(params().toString()).toBe("view=models");
    expect(search?.value).toBe("");
    expect(selected()).toBe("usage-view-models-tab");
    expect(overviewMock).toHaveBeenLastCalledWith(expect.objectContaining({ provider: undefined, q: undefined, from: undefined, offset: 0 }));

    await click(container.querySelector('[data-testid="model-chart"]'));
    expect(params().get("view")).toBe("models");
    expect(params().get("provider")).toBe("selected-provider");
    expect(overviewMock).toHaveBeenLastCalledWith(expect.objectContaining({ provider: "selected-provider" }));
  });

  it("restores the selected category from browser history without discarding the search scope", async () => {
    await mount("/usage?q=kept&offset=50");
    await click(container.querySelector("#usage-view-models-tab"));
    await click(container.querySelector("#usage-view-sessions-tab"));
    await click(container.querySelector('[data-testid="back"]'));
    expect(selected()).toBe("usage-view-models-tab");
    expect(params().get("q")).toBe("kept");
    expect(params().get("offset")).toBe("50");
    expect(overviewMock).toHaveBeenCalledTimes(1);
  });

  it("supports arrow keys, Home and End with a single focusable selected tab", async () => {
    await mount();
    const transitions = [
      ["ArrowLeft", "artifacts"],
      ["Home", "overview"],
      ["End", "artifacts"],
      ["ArrowRight", "overview"],
    ];
    for (const [key, view] of transitions) {
      const active = container.querySelector<HTMLButtonElement>('[role="tab"][aria-selected="true"]');
      await act(async () => {
        active?.focus();
        active?.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
      });
      expect(selected()).toBe(`usage-view-${view}-tab`);
      expect(document.activeElement?.id).toBe(`usage-view-${view}-tab`);
    }
    expect(overviewMock).toHaveBeenCalledTimes(1);
  });

  it("falls back safely for an unknown view and ignores session-detail tab values", async () => {
    await mount("/usage?view=unknown&tab=routing&model=test-model");
    expect(selected()).toBe("usage-view-overview-tab");
    expect(container.querySelector('[data-testid="routing-panel"]')).toBeNull();
    expect(overviewMock).toHaveBeenCalledWith(expect.objectContaining({ model: "test-model" }));
  });

  it.each([
    "/usage/sessions/test?tab=routing&provider=openai&offset=50",
    "/usage/sessions/test?view=sessions&tab=diagnostics&error_category=timeout&provider=openai&offset=50",
  ])("returns to the session-list category without leaking detail-tab filters: %s", async (entry) => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={[entry]}>
          <Routes><Route path="/usage/sessions/:sessionId" element={<AnalyticsHeader onRefresh={vi.fn()} refreshing={false} />} /></Routes>
        </MemoryRouter>,
      );
    });
    const href = container.querySelector('[aria-label="返回会话列表"]')?.getAttribute("href");
    const target = new URL(href ?? "", "http://localhost");
    expect(target.pathname).toBe("/usage");
    expect(target.searchParams.get("view")).toBe("sessions");
    expect(target.searchParams.get("provider")).toBe("openai");
    expect(target.searchParams.get("offset")).toBe("50");
    expect(target.searchParams.has("tab")).toBe(false);
    expect(target.searchParams.has("error_category")).toBe(false);
  });

  it("keeps URL transformations immutable and resets only the category-independent state", () => {
    const original = new URLSearchParams("q=kept&offset=50&view=sessions");
    expect(selectOverviewView(original, "models").get("offset")).toBe("50");
    expect(original.get("view")).toBe("sessions");
    expect(selectOverviewView(original, "overview").has("view")).toBe(false);
    expect(resetOverviewFilters(original).toString()).toBe("view=sessions");
    expect(resolveOverviewView(null)).toBe("overview");
    expect(resolveOverviewView("invalid")).toBe("overview");
  });
});
