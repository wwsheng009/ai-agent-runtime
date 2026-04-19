import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useState,
  type PropsWithChildren,
} from "react";

import {
  AppSettingsContext,
  type UpdateSettingsSection,
} from "@/core/settings/context-store";
import {
  applyDocumentSettings,
  getSystemTheme,
} from "@/core/settings/document";
import {
  APP_SETTINGS_STORAGE_KEY,
  DEFAULT_APP_SETTINGS,
  getStoredAppSettings,
  resolveThemeMode,
  writeStoredAppSettings,
  type AppSettings,
  type ResolvedTheme,
} from "@/core/settings/local";

function getBrowserStorage() {
  if (typeof window === "undefined") {
    return null;
  }

  return window.localStorage;
}

export function SettingsProvider({ children }: PropsWithChildren) {
  const [settings, setSettings] = useState<AppSettings>(() =>
    getStoredAppSettings(getBrowserStorage()),
  );
  const [systemTheme, setSystemTheme] = useState<ResolvedTheme>(getSystemTheme);
  const resolvedTheme = resolveThemeMode(
    settings.appearance.themeMode,
    systemTheme,
  );

  const updateSection = useCallback<UpdateSettingsSection>((key, value) => {
    setSettings((current) => ({
      ...current,
      [key]: {
        ...current[key],
        ...value,
      },
    }));
  }, []);

  const resetSettings = useCallback(() => {
    setSettings(DEFAULT_APP_SETTINGS);
  }, []);

  useLayoutEffect(() => {
    applyDocumentSettings(settings, resolvedTheme);
  }, [resolvedTheme, settings]);

  useEffect(() => {
    const storage = getBrowserStorage();
    writeStoredAppSettings(storage, settings);
  }, [settings]);

  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
      return;
    }

    const mediaQuery = window.matchMedia("(prefers-color-scheme: dark)");
    const handleChange = () => {
      setSystemTheme(mediaQuery.matches ? "dark" : "light");
    };

    handleChange();
    mediaQuery.addEventListener("change", handleChange);
    return () => {
      mediaQuery.removeEventListener("change", handleChange);
    };
  }, []);

  useEffect(() => {
    if (typeof window === "undefined") {
      return;
    }

    const handleStorage = (event: StorageEvent) => {
      if (event.key !== APP_SETTINGS_STORAGE_KEY) {
        return;
      }

      setSettings(getStoredAppSettings(getBrowserStorage()));
    };

    window.addEventListener("storage", handleStorage);
    return () => {
      window.removeEventListener("storage", handleStorage);
    };
  }, []);

  const value = useMemo(
    () => ({
      settings,
      resetSettings,
      resolvedTheme,
      systemTheme,
      updateSection,
    }),
    [resetSettings, resolvedTheme, settings, systemTheme, updateSection],
  );

  return (
    <AppSettingsContext.Provider value={value}>
      {children}
    </AppSettingsContext.Provider>
  );
}
