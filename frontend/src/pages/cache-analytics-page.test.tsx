import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";

import { CacheAnalyticsView } from "./cache-analytics-page";
import { UsageAnalyticsPage } from "./usage-analytics-page";

describe("CacheAnalyticsView", () => {
  it("renders the cache view shell with the usage back link when no session is selected", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/usage/cache"]}>
        <Routes>
          <Route path="/usage/cache" element={<CacheAnalyticsView sessionId={null} />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(markup).toContain("LLM 缓存分析");
    expect(markup).toContain("缓存命中率、读写 token 与请求级明细");
    expect(markup).toContain("请先选择一个会话以查看缓存分析。");
    expect(markup).toContain('aria-label="选择会话"');
    expect(markup).toContain('href="/usage"');
  });

  it("renders the session-scoped cache view for /usage/cache/sessions/:sessionId", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/usage/cache/sessions/sess-1"]}>
        <Routes>
          <Route path="/usage/cache/sessions/:sessionId" element={<CacheAnalyticsView sessionId="sess-1" />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(markup).toContain("缓存总览");
    expect(markup).toContain("缓存状态分布");
    expect(markup).toContain("请求明细");
  });
});

describe("UsageAnalyticsPage cache tab integration", () => {
  it("keeps the usage view on /usage and exposes the 用量/缓存 tab switch", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/usage"]}>
        <Routes>
          <Route path="/usage" element={<UsageAnalyticsPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(markup).toContain("会话与总体用量");
    expect(markup).toContain('aria-label="分析视图"');
    expect(markup).toContain('href="/usage/cache"');
    expect(markup).toContain('aria-selected="true"');
  });

  it("routes /usage/cache to the cache view branch (lazy boundary shows its fallback)", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/usage/cache"]}>
        <Routes>
          <Route path="/usage/cache" element={<UsageAnalyticsPage />} />
        </Routes>
      </MemoryRouter>,
    );

    // renderToStaticMarkup 不解析 lazy import，Suspense 渲染 fallback；
    // fallback 出现即证明路由进入了缓存视图分支（视图内容由上方
    // CacheAnalyticsView 直接渲染测试覆盖）。
    expect(markup).toContain("正在加载缓存分析");
    expect(markup).not.toContain("会话与总体用量");
  });
});
