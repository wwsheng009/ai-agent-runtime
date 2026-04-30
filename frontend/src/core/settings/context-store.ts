import { createContext } from "react";

import type { AppSettings, ResolvedTheme } from "@/core/settings/local";
import type { ResolvedLocale } from "@/i18n/locale";

export type UpdateSettingsSection = <K extends keyof AppSettings>(
  key: K,
  value: Partial<AppSettings[K]>,
) => void;

export type AppSettingsContextValue = {
  settings: AppSettings;
  resetSettings: () => void;
  resolvedLocale: ResolvedLocale;
  resolvedTheme: ResolvedTheme;
  systemTheme: ResolvedTheme;
  updateSection: UpdateSettingsSection;
};

export const AppSettingsContext = createContext<AppSettingsContextValue | null>(
  null,
);
