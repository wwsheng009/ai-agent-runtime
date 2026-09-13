import React from "react";
import ReactDOM from "react-dom/client";

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

bootstrapDocumentSettings();

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <SettingsProvider>
      <App />
    </SettingsProvider>
  </React.StrictMode>,
);
