// @vitest-environment jsdom

// 批次 7.2：会话观测面板（工具 / 子代理 / 失败模式）渲染与降级分支。
// 空库语义（空数组）→ 「暂无数据」；加载失败 → role="alert"；下钻 → 回调带出过滤条件。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const {
  listAnalyticsToolsMock,
  getAnalyticsSubagentsMock,
  listAnalyticsErrorsMock,
  getAnalyticsRoutingStatsMock,
  listAnalyticsRoutingEventsMock,
} = vi.hoisted(() => ({
  listAnalyticsToolsMock: vi.fn(),
  getAnalyticsSubagentsMock: vi.fn(),
  listAnalyticsErrorsMock: vi.fn(),
  getAnalyticsRoutingStatsMock: vi.fn(),
  listAnalyticsRoutingEventsMock: vi.fn(),
}));

vi.mock("@/api/runtime/analytics", () => ({
  listAnalyticsTools: listAnalyticsToolsMock,
  getAnalyticsSubagents: getAnalyticsSubagentsMock,
  listAnalyticsErrors: listAnalyticsErrorsMock,
  getAnalyticsRoutingStats: getAnalyticsRoutingStatsMock,
  listAnalyticsRoutingEvents: listAnalyticsRoutingEventsMock,
}));

import { ErrorPatternsPanel } from "./error-patterns-panel";
import {
  emptyRouteEventsResponse,
  emptyRouteStatsResponse,
  emptySubagentResponse,
  emptyToolResponse,
  fencePattern,
  routeEventsResponse,
  routeStatsResponse,
  subagentResponse,
  timeoutPattern,
  toolResponse,
  type ReactActEnvironmentGlobal,
} from "./observability-panels.test-fixtures";
import { RoutingObservabilityPanel } from "./routing-observability-panel";
import { SubagentStatsPanel } from "./subagent-stats-panel";
import { ToolStatsPanel } from "./tool-stats-panel";

describe("usage analytics observability panels", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    listAnalyticsToolsMock.mockReset();
    getAnalyticsSubagentsMock.mockReset();
    listAnalyticsErrorsMock.mockReset();
    getAnalyticsRoutingStatsMock.mockReset();
    listAnalyticsRoutingEventsMock.mockReset();
    Element.prototype.scrollIntoView = vi.fn();
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  async function flush() {
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  it("工具面板：空数组渲染「暂无数据」", async () => {
    listAnalyticsToolsMock.mockResolvedValue(emptyToolResponse());
    act(() => {
      root.render(<ToolStatsPanel sessionId="session-1" />);
    });
    await flush();

    expect(container.querySelector('[data-testid="tool-stats-empty"]')).not.toBeNull();
    expect(container.textContent).toContain("暂无数据");
    // 无耗时样本 → 不伪造成 0ms（与 micro web / TUI 的「未上报 → --」同口径）。
    expect(container.textContent).toContain("最小 -- · 最大 --");
    expect(listAnalyticsToolsMock).toHaveBeenCalledWith({
      session: "session-1",
      outcome: undefined,
      adminToken: undefined,
    });
  });

  it("工具面板：渲染失败率与可展开的失败样本", async () => {
    listAnalyticsToolsMock.mockResolvedValue(toolResponse());
    act(() => {
      root.render(<ToolStatsPanel sessionId="session-1" />);
    });
    await flush();

    expect(container.textContent).toContain("shell");
    expect(container.textContent).toContain("25.0%");
    expect(container.textContent).toContain("5.0 s");
    // 页头耗时指标：平均 + 最小/最大区间；行内最小列 300 ms。
    expect(container.textContent).toContain("1.2 s");
    expect(container.textContent).toContain("最小 300 ms · 最大 5.0 s");

    // 限定在表格内：页头的 Select 触发器同样带 aria-expanded。
    const toggle = container.querySelector<HTMLButtonElement>(
      'table button[aria-expanded="false"]',
    );
    expect(toggle).not.toBeNull();
    act(() => {
      toggle?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(container.textContent).toContain("tool_timeout");
    expect(container.textContent).toContain("超时");
  });

  it("工具面板：加载失败渲染 role=alert", async () => {
    listAnalyticsToolsMock.mockRejectedValue(new Error("403 forbidden"));
    act(() => {
      root.render(<ToolStatsPanel sessionId="session-1" />);
    });
    await flush();

    const alert = container.querySelector('[role="alert"]');
    expect(alert?.textContent).toContain("403 forbidden");
    // 鉴权失败不是空库：不得再渲染「暂无数据」把失败伪装成没有数据。
    expect(container.querySelector('[data-testid="tool-stats-empty"]')).toBeNull();
  });

  it("子代理面板：加载失败只渲染 role=alert，不渲染「暂无数据」", async () => {
    getAnalyticsSubagentsMock.mockRejectedValue(new Error("403 forbidden"));
    act(() => {
      root.render(<SubagentStatsPanel sessionId="session-1" />);
    });
    await flush();

    expect(container.querySelector('[role="alert"]')?.textContent).toContain("403 forbidden");
    expect(container.querySelector('[data-testid="subagent-stats-empty"]')).toBeNull();
  });

  it("失败模式面板：加载失败只渲染 role=alert，不渲染「暂无数据」", async () => {
    listAnalyticsErrorsMock.mockRejectedValue(new Error("403 forbidden"));
    act(() => {
      root.render(
        <ErrorPatternsPanel
          sessionId="session-1"
          onDrilldown={() => {}}
        />,
      );
    });
    await flush();

    expect(container.querySelector('[role="alert"]')?.textContent).toContain("403 forbidden");
    expect(container.querySelector('[data-testid="error-patterns-empty"]')).toBeNull();
  });

  it("子代理面板：空数组渲染「暂无数据」并展示零值摘要", async () => {
    getAnalyticsSubagentsMock.mockResolvedValue(emptySubagentResponse());
    act(() => {
      root.render(<SubagentStatsPanel sessionId="session-1" />);
    });
    await flush();

    expect(container.querySelector('[data-testid="subagent-stats-empty"]')).not.toBeNull();
    expect(container.textContent).toContain("暂无数据");
    expect(getAnalyticsSubagentsMock).toHaveBeenCalledWith({
      session: "session-1",
      failed_only: undefined,
      failure_category: undefined,
      limit: 200,
      adminToken: undefined,
    });
  });

  it("子代理面板：渲染完成/失败、失败分类与来源", async () => {
    getAnalyticsSubagentsMock.mockResolvedValue(subagentResponse());
    act(() => {
      root.render(<SubagentStatsPanel sessionId="session-1" />);
    });
    await flush();

    expect(container.textContent).toContain("50.0%");
    expect(container.textContent).toContain("subagent-ok-1");
    expect(container.textContent).toContain("超时");
    expect(container.textContent).toContain("调度器");
    expect(container.textContent).toContain("团队");
    expect(container.textContent).toContain("第 2/2 次");
    // P4：任务类型列（12 类枚举归一为标签）与 task_subject 次行，role 列保留。
    expect(container.textContent).toContain("任务类型");
    expect(container.textContent).toContain("探索");
    expect(container.textContent).toContain("排查路由面板");
    expect(container.textContent).toContain("角色");
  });

  it("失败模式面板：空数组渲染「暂无数据」", async () => {
    listAnalyticsErrorsMock.mockResolvedValue({
      schema_version: "usage.analytics.v2",
      generated_at: "2026-09-17T00:00:00Z",
      patterns: [],
    });
    act(() => {
      root.render(
        <ErrorPatternsPanel
          sessionId="session-1"
          onDrilldown={() => {}}
        />,
      );
    });
    await flush();

    expect(container.querySelector('[data-testid="error-patterns-empty"]')).not.toBeNull();
    expect(container.textContent).toContain("暂无数据");
  });

  it("失败模式面板：点击下钻回传失败分类", async () => {
    listAnalyticsErrorsMock.mockResolvedValue({
      schema_version: "usage.analytics.v2",
      generated_at: "2026-09-17T00:00:00Z",
      patterns: [timeoutPattern],
    });
    const onDrilldown = vi.fn();
    act(() => {
      root.render(
        <ErrorPatternsPanel
          sessionId="session-1"
          onDrilldown={onDrilldown}
        />,
      );
    });
    await flush();

    expect(container.textContent).toContain("deadline_exceeded");
    const drilldown = container.querySelector<HTMLButtonElement>(
      'button[aria-label*="deadline_exceeded"]',
    );
    expect(drilldown).not.toBeNull();
    act(() => {
      drilldown?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onDrilldown).toHaveBeenCalledWith(timeoutPattern);
  });

  // P1-1b：渲染围栏来源（source=fence）必须出现在失败模式面板，分类走 i18n
  // 标签而不是裸字符串 render_fence_drop_closed。
  it("失败模式面板：渲染围栏来源与本地化分类标签", async () => {
    listAnalyticsErrorsMock.mockResolvedValue({
      schema_version: "usage.analytics.v2",
      generated_at: "2026-09-27T00:00:00Z",
      patterns: [fencePattern],
    });
    act(() => {
      root.render(
        <ErrorPatternsPanel
          sessionId="session-1"
          onDrilldown={() => {}}
        />,
      );
    });
    await flush();

    expect(container.textContent).toContain("RENDER_FENCE_DROPPED_CLOSED");
    expect(container.textContent).toContain("围栏丢弃（run 已结束）");
    // 未命中标签表时会渲染成 "RENDER_FENCE_DROPPED_CLOSED · render_fence_drop_closed"。
    expect(container.textContent).not.toContain("· render_fence_drop_closed");
    // 渲染围栏不是 provider/tool 失败：不下钻（诊断 tab 的失败分类过滤对它无意义）。
    expect(
      container.querySelector('button[aria-label*="RENDER_FENCE_DROPPED_CLOSED"]'),
    ).toBeNull();
    expect(container.textContent).toContain("不可下钻");
  });

  it("路由面板：空数组渲染「暂无路由事件」并传会话过滤", async () => {
    getAnalyticsRoutingStatsMock.mockResolvedValue(emptyRouteStatsResponse());
    listAnalyticsRoutingEventsMock.mockResolvedValue(emptyRouteEventsResponse());
    act(() => {
      root.render(<RoutingObservabilityPanel sessionId="session-1" />);
    });
    await flush();

    expect(container.querySelector('[data-testid="routing-observability-empty"]')).not.toBeNull();
    expect(container.textContent).toContain("暂无路由事件");
    expect(getAnalyticsRoutingStatsMock).toHaveBeenCalledWith({
      session: "session-1",
      scope: undefined,
      warnings_only: undefined,
      adminToken: undefined,
    });
    expect(listAnalyticsRoutingEventsMock).toHaveBeenCalledWith({
      session: "session-1",
      scope: undefined,
      warnings_only: undefined,
      adminToken: undefined,
      limit: 50,
      offset: 0,
    });
  });

  it("路由面板：渲染主/子切换指标、分布与三态明细", async () => {
    getAnalyticsRoutingStatsMock.mockResolvedValue(routeStatsResponse());
    listAnalyticsRoutingEventsMock.mockResolvedValue(routeEventsResponse());
    act(() => {
      root.render(<RoutingObservabilityPanel sessionId="session-1" />);
    });
    await flush();

    // 指标卡：主 Agent / 子 Agent / 改道 / 回退。
    expect(container.textContent).toContain("主 Agent");
    expect(container.textContent).toContain("子 Agent");
    expect(container.textContent).toContain("改道");
    expect(container.textContent).toContain("回退");
    // 分布桶标签归一：显式+提升 / 难度档位 hard。
    expect(container.textContent).toContain("显式+提升");
    expect(container.textContent).toContain("hard");
    // 难度来源维度：区分「模型显式声明」与「本地推断」，是本地网是否过火的判据。
    expect(container.textContent).toContain("推断");
    // 明细行：路由组合、三态布尔与护栏告警。
    expect(container.textContent).toContain("subagent-route-1");
    expect(container.textContent).toContain("ds2api / deepseek-v4-flash");
    // P4：by_task_type 分布卡与 by_role 分布卡并存渲染（role 卡兼容保留）。
    expect(container.textContent).toContain("安全");
    expect(container.textContent).toContain("researcher");
    expect(container.textContent).toContain("任务类型");
    // 分布桶仍展示完整标签；明细行只保留摘要，不将长文案堆在表格中。
    expect(container.textContent).toContain("任务类型下限：security");
    expect(container.textContent).toContain("已显示 1 / 1");
    expect(container.textContent).toContain("1 条");
    const table = container.querySelector("table");
    expect(table?.classList.contains("table-fixed")).toBe(true);
    expect(table?.textContent).not.toContain("核对 P4 契约");
    expect(table?.textContent).not.toContain("改一个文件");
    expect(table?.textContent).not.toContain("任务类型下限：security");
    expect(document.querySelector('[role="dialog"]')).toBeNull();

    act(() => {
      table?.querySelector("button")?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog?.getAttribute("aria-modal")).toBe("true");
    expect(dialog?.textContent).toContain("核对 P4 契约");
    expect(dialog?.textContent).toContain("改一个文件");
    expect(dialog?.textContent).toContain("任务类型下限：security");
    expect(dialog?.textContent).toContain("difficulty_floor_by_task_type:security");
    expect(dialog?.textContent).toContain("child-route-1");
    // 使用事件已携带的数据展开，不额外请求或重置表格。
    expect(listAnalyticsRoutingEventsMock).toHaveBeenCalledTimes(1);
    expect(container.querySelector("table")).toBe(table);
  });

  it("路由面板：切换会话时关闭旧事件明细", async () => {
    getAnalyticsRoutingStatsMock.mockResolvedValue(routeStatsResponse());
    listAnalyticsRoutingEventsMock.mockResolvedValue(routeEventsResponse());
    act(() => root.render(<RoutingObservabilityPanel sessionId="session-1" />));
    await flush();
    act(() => {
      container.querySelector("tbody tr")?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();

    act(() => root.render(<RoutingObservabilityPanel sessionId="session-2" />));
    await flush();
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(listAnalyticsRoutingEventsMock).toHaveBeenLastCalledWith(expect.objectContaining({
      session: "session-2",
      offset: 0,
    }));
  });

  it("路由面板：加载失败渲染 role=alert", async () => {
    getAnalyticsRoutingStatsMock.mockRejectedValue(new Error("403 forbidden"));
    listAnalyticsRoutingEventsMock.mockRejectedValue(new Error("403 forbidden"));
    act(() => {
      root.render(<RoutingObservabilityPanel sessionId="session-1" />);
    });
    await flush();

    const alert = container.querySelector('[role="alert"]');
    expect(alert?.textContent).toContain("403 forbidden");
  });
});
