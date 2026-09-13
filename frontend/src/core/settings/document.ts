import {
  type AppSettings,
  type ResolvedTheme,
} from "@/core/settings/local";
import { presentThemeSnapshot } from "@/core/theme/present";
import { resolveThemeSnapshot } from "@/core/theme/resolve";
import { type ResolvedLocale } from "@/i18n/locale";

export function getSystemTheme(): ResolvedTheme {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
    return "dark";
  }

  return window.matchMedia("(prefers-color-scheme: dark)").matches
    ? "dark"
    : "light";
}

export function applyDocumentSettings(
  settings: AppSettings,
  resolvedTheme: ResolvedTheme,
  resolvedLocale: ResolvedLocale,
) {
  if (typeof document === "undefined") {
    return;
  }

  // P0-5：DOM 写入统一收敛到 core/theme（resolve → snapshot → present）。
  // 保留本函数作为兼容入口；写入的属性集合与顺序不变，等价性见
  // core/theme/present.test.ts 与 core/theme/boot-script.test.ts。
  presentThemeSnapshot(
    resolveThemeSnapshot(settings, resolvedTheme, resolvedLocale),
  );
}
