import { createContext } from "react";

import type { AppSettings, ResolvedTheme } from "@/core/settings/local";

export type UpdateSettingsSection = <K extends keyof AppSettings>(
  key: K,
  value: Partial<AppSettings[K]>,
) => void;

export type AppSettingsContextValue = {
  settings: AppSettings;
  resetSettings: () => void;
  resolvedTheme: ResolvedTheme;
  systemTheme: ResolvedTheme;
  updateSection: UpdateSettingsSection;
};

export const AppSettingsContext = createContext<AppSettingsContextValue | null>(
  null,
);
