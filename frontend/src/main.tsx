import React from "react";
import ReactDOM from "react-dom/client";

import {
  applyDocumentSettings,
  getStoredAppSettings,
  getSystemTheme,
  resolveThemeMode,
  SettingsProvider,
} from "@/core/settings";

import App from "./App";
import "./styles/globals.css";

function bootstrapDocumentSettings() {
  if (typeof window === "undefined") {
    return;
  }

  const settings = getStoredAppSettings(window.localStorage);
  const systemTheme = getSystemTheme();
  const resolvedTheme = resolveThemeMode(
    settings.appearance.themeMode,
    systemTheme,
  );

  applyDocumentSettings(settings, resolvedTheme);
}

bootstrapDocumentSettings();

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <SettingsProvider>
      <App />
    </SettingsProvider>
  </React.StrictMode>,
);
