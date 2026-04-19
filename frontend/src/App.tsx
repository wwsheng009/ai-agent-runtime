import { lazy, Suspense } from "react";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";

const LandingPage = lazy(() =>
  import("@/pages/landing-page").then((module) => ({
    default: module.LandingPage,
  })),
);
const LogsPage = lazy(() =>
  import("@/pages/logs-page").then((module) => ({
    default: module.LogsPage,
  })),
);
const RuntimeConfigPage = lazy(() =>
  import("@/pages/runtime-config-page").then((module) => ({
    default: module.RuntimeConfigPage,
  })),
);
const WorkspacePage = lazy(() =>
  import("@/pages/workspace-page").then((module) => ({
    default: module.WorkspacePage,
  })),
);

export default function App() {
  const defaultWorkspaceRoute = "/workspace/chats/new";

  return (
    <BrowserRouter>
      <Suspense fallback={<AppRouteFallback />}>
        <Routes>
          <Route path="/" element={<LandingPage />} />
          <Route path="/logs" element={<LogsPage />} />
          <Route path="/runtime/config" element={<RuntimeConfigPage />} />
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
          <Route path="/workspace/restore/:sessionId" element={<WorkspacePage />} />
          <Route path="/workspace/sessions/:sessionId" element={<WorkspacePage />} />
          <Route path="/workspace/chats/:threadId" element={<WorkspacePage />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </Suspense>
    </BrowserRouter>
  );
}

function AppRouteFallback() {
  return (
    <div className="flex min-h-screen items-center justify-center [background:var(--workspace-shell-bg)] px-4 text-[var(--foreground)]">
      <div className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] px-4 py-3 text-sm text-[var(--muted-foreground)]">
        正在加载页面…
      </div>
    </div>
  );
}
