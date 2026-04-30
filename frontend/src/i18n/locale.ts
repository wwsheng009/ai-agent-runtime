export type LocalePreference = "system" | "zh-CN" | "en-US";
export type ResolvedLocale = "zh-CN" | "en-US";

export function resolveSystemLocale(language?: string): ResolvedLocale {
  const normalized = language?.trim().toLowerCase() ?? "";
  return normalized.startsWith("zh") ? "zh-CN" : "en-US";
}

export function resolveLocalePreference(
  preference: LocalePreference,
  systemLanguage?: string,
): ResolvedLocale {
  if (preference === "zh-CN" || preference === "en-US") {
    return preference;
  }

  if (systemLanguage) {
    return resolveSystemLocale(systemLanguage);
  }

  if (typeof navigator !== "undefined") {
    return resolveSystemLocale(navigator.language);
  }

  return "zh-CN";
}
