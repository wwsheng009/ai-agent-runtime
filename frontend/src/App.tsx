import { useTranslation } from "react-i18next";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";

import { RouteErrorBoundary } from "@/components/errors/boundaries";
import { RetryableLazyRoute } from "@/components/errors/retryable-lazy-route";
import { StartupReadySignal } from "@/components/startup/startup-ready-signal";
import { type LazySurfaceLoader } from "@/lib/lazy-retry";

// P1-10：路由级 lazy 元素持有模块级稳定 loader 引用，RetryableLazyRoute 才能在
// 手动重试时重建 lazy 组件（受上限 + 递增退避约束），而不是无界重载。
const loadLandingPage: LazySurfaceLoader = () =>
  import("@/pages/landing-page").then((module) => ({
    default: module.LandingPage,
  }));
const loadLogsPage: LazySurfaceLoader = () =>
  import("@/pages/logs-page").then((module) => ({
    default: module.LogsPage,
  }));
const loadRuntimeConfigPage: LazySurfaceLoader = () =>
  import("@/pages/runtime-config-page").then((module) => ({
    default: module.RuntimeConfigPage,
  }));
const loadUsageAnalyticsPage: LazySurfaceLoader = () =>
  import("@/pages/usage-analytics-page").then((module) => ({
    default: module.UsageAnalyticsPage,
  }));
const loadSkillsPage: LazySurfaceLoader = () =>
  import("@/pages/skills-page").then((module) => ({
    default: module.SkillsPage,
  }));
const loadWorkspacePage: LazySurfaceLoader = () =>
  import("@/pages/workspace-page").then((module) => ({
    default: module.WorkspacePage,
  }));

const defaultWorkspaceRoute = "/workspace/chats/new";
// 根路由已改为直达默认工作台，原根路由页面（Landing）迁移到 /about。
const aboutRoute = "/about";

export default function App() {
  return (
    <BrowserRouter>
      <RouteErrorBoundary>
        <Routes>
          <Route
            path="/"
            element={<Navigate to={defaultWorkspaceRoute} replace />}
          />
          <Route
            path={aboutRoute}
            element={
              <RetryableLazyRoute
                fallback={<AppRouteFallback />}
                loader={loadLandingPage}
              />
            }
          />
          <Route
            path="/logs"
            element={
              <RetryableLazyRoute
                fallback={<AppRouteFallback />}
                loader={loadLogsPage}
              />
            }
          />
          <Route
            path="/usage"
            element={
              <RetryableLazyRoute
                fallback={<AppRouteFallback />}
                loader={loadUsageAnalyticsPage}
              />
            }
          />
          <Route
            path="/usage/sessions/:sessionId"
            element={
              <RetryableLazyRoute
                fallback={<AppRouteFallback />}
                loader={loadUsageAnalyticsPage}
              />
            }
          />
          <Route
            path="/analytics"
            element={
              <RetryableLazyRoute
                fallback={<AppRouteFallback />}
                loader={loadUsageAnalyticsPage}
              />
            }
          />
          <Route
            path="/runtime/config"
            element={
              <RetryableLazyRoute
                fallback={<AppRouteFallback />}
                loader={loadRuntimeConfigPage}
              />
            }
          />
          <Route
            path="/runtime/skills"
            element={
              <RetryableLazyRoute
                fallback={<AppRouteFallback />}
                loader={loadSkillsPage}
              />
            }
          />
          <Route
            path="/workspace"
            element={<Navigate to={defaultWorkspaceRoute} replace />}
          />
          <Route
            path="/workspace/chats"
            element={<Navigate to={defaultWorkspaceRoute} replace />}
          />
          <Route
            path="/workspace/sessions"
            element={<Navigate to={defaultWorkspaceRoute} replace />}
          />
          <Route
            path="/workspace/restore"
            element={<Navigate to="/workspace/sessions" replace />}
          />
          <Route
            path="/workspace/restore/:sessionId"
            element={
              <RetryableLazyRoute
                fallback={<AppRouteFallback />}
                loader={loadWorkspacePage}
              />
            }
          />
          <Route
            path="/workspace/sessions/:sessionId"
            element={
              <RetryableLazyRoute
                fallback={<AppRouteFallback />}
                loader={loadWorkspacePage}
              />
            }
          />
          <Route
            path="/workspace/chats/:threadId"
            element={
              <RetryableLazyRoute
                fallback={<AppRouteFallback />}
                loader={loadWorkspacePage}
              />
            }
          />
          <Route
            path="*"
            element={<Navigate to={defaultWorkspaceRoute} replace />}
          />
        </Routes>
      </RouteErrorBoundary>
      <StartupReadySignal />
    </BrowserRouter>
  );
}

function AppRouteFallback() {
  const { t } = useTranslation("common");

  return (
    <div className="flex min-h-screen items-center justify-center [background:var(--workspace-shell-bg)] px-4 text-foreground">
      <div className="rounded-panel border border-border bg-surface-softer px-4 py-3 text-sm text-muted-foreground">
        {t("loading.page")}
      </div>
    </div>
  );
}
