import React from "react";
import ReactDOM from "react-dom/client";

import { GlobalErrorBoundary } from "@/components/errors/boundaries";
import {
  DEFAULT_STARTUP_READY_TIMEOUT_MS,
  hasVisibleFailureSurface,
  renderStartupFailure,
  resolveRootElement,
  waitForStartupReadiness,
} from "@/core/bootstrap";
import { logger } from "@/core/logger";
import {
  getStoredAppSettings,
  getSystemTheme,
  SettingsProvider,
  type AppSettings,
} from "@/core/settings";
import { presentThemeSnapshot, resolveThemeSnapshot } from "@/core/theme";
import { initI18n } from "@/i18n";
import { resolveLocalePreference } from "@/i18n/locale";

import App from "./App";
import "./styles/globals.css";

function bootstrapDocumentSettings() {
  if (typeof window === "undefined") {
    return;
  }

  const settings = getStoredAppSettings(window.localStorage) satisfies AppSettings;
  const resolvedLocale = resolveLocalePreference(
    settings.localization.locale,
  );

  initI18n(resolvedLocale);
  // P0-5：resolve（设置 + 系统主题 + 语言 → snapshot）与 present（snapshot → DOM）分离；
  // index.html 的启动脚本用同一套映射，保证首屏不闪。
  presentThemeSnapshot(
    resolveThemeSnapshot(settings, getSystemTheme(), resolvedLocale),
  );
}

// P1-10：启动路径上的任何失败都要落到可见错误面（非白屏）。
// 若 React 错误边界已经渲染出 `role="alert"`，则不覆盖它，只补一条日志。
function reportStartupFailure(error: unknown, description?: string): void {
  if (hasVisibleFailureSurface()) {
    logger.error("startup failure already surfaced by an error boundary", error);
    return;
  }
  renderStartupFailure({
    error,
    ...(description === undefined ? {} : { description }),
  });
}

function createAppRoot(): ReturnType<typeof ReactDOM.createRoot> | null {
  try {
    return ReactDOM.createRoot(resolveRootElement(document));
  } catch (error) {
    logger.error("failed to create the React root", error);
    reportStartupFailure(error);
    return null;
  }
}

function renderApp(root: ReturnType<typeof ReactDOM.createRoot>): boolean {
  try {
    root.render(
      <React.StrictMode>
        <GlobalErrorBoundary>
          <SettingsProvider>
            <App />
          </SettingsProvider>
        </GlobalErrorBoundary>
      </React.StrictMode>,
    );
    return true;
  } catch (error) {
    logger.error("failed to render the application", error);
    reportStartupFailure(error);
    return false;
  }
}

function startApp(): void {
  if (typeof window === "undefined" || typeof document === "undefined") {
    return;
  }

  try {
    bootstrapDocumentSettings();
  } catch (error) {
    logger.error("failed to bootstrap document settings", error);
    reportStartupFailure(error);
    return;
  }

  const root = createAppRoot();
  if (!root || !renderApp(root)) {
    return;
  }

  // P1-10：启动完整性检查——挂载后确认根节点有内容、应用壳（provider + 路由）
  // 已就绪、i18n 已初始化；超时未就绪则进入可见错误面而非半加载状态。
  void waitForStartupReadiness({
    timeoutMs: DEFAULT_STARTUP_READY_TIMEOUT_MS,
  }).catch((error: unknown) => {
    reportStartupFailure(error);
  });
}

startApp();
