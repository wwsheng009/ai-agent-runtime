// P1-10：启动完整性标记。本组件位于 SettingsProvider 与 BrowserRouter 内部：
// 能挂载即证明关键 provider 与路由已就绪；effect 再写入 data-app-ready，
// 供 main.tsx 的 waitForStartupReadiness 读取。

import { useEffect } from "react";

import { markStartupReady } from "@/core/bootstrap";
import { logger } from "@/core/logger";
import { useAppSettings } from "@/core/settings";

export function StartupReadySignal() {
  const { resolvedLocale, settings } = useAppSettings();

  useEffect(() => {
    markStartupReady();
    logger.debug("app shell ready", {
      locale: resolvedLocale,
      localePreference: settings.localization.locale,
    });
  }, [resolvedLocale, settings.localization.locale]);

  return null;
}
