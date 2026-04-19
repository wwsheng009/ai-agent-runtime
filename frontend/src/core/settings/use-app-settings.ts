import { useContext } from "react";

import { AppSettingsContext } from "@/core/settings/context-store";

export function useAppSettings() {
  const context = useContext(AppSettingsContext);
  if (!context) {
    throw new Error("useAppSettings must be used within a SettingsProvider");
  }

  return context;
}
