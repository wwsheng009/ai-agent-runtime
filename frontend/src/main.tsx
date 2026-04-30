import React from "react";
import ReactDOM from "react-dom/client";

import {
  applyDocumentSettings,
  getStoredAppSettings,
  getSystemTheme,
  resolveThemeMode,
  SettingsProvider,
  type AppSettings,
} from "@/core/settings";
import { initI18n } from "@/i18n";
import { resolveLocalePreference } from "@/i18n/locale";

import App from "./App";
import "./styles/globals.css";

function bootstrapDocumentSettings() {
  if (typeof window === "undefined") {
    return;
  }

  const settings = getStoredAppSettings(window.localStorage) satisfies AppSettings;
  const systemTheme = getSystemTheme();
  const resolvedLocale = resolveLocalePreference(
    settings.localization.locale,
  );
  const resolvedTheme = resolveThemeMode(
    settings.appearance.themeMode,
    systemTheme,
  );

  initI18n(resolvedLocale);
  applyDocumentSettings(settings, resolvedTheme, resolvedLocale);
}

bootstrapDocumentSettings();

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <SettingsProvider>
      <App />
    </SettingsProvider>
  </React.StrictMode>,
);
