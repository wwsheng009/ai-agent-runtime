import {
  CODE_FONT_FAMILY_STACKS,
  FONT_FAMILY_STACKS,
  formatFontSizePx,
  getCodeLineNumberFontSize,
  type AppSettings,
  type ResolvedTheme,
} from "@/core/settings/local";
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

  const root = document.documentElement;
  root.lang = resolvedLocale;
  root.dataset.accentTone = settings.appearance.accentTone;
  root.dataset.reducedMotion = settings.appearance.reducedMotion ? "true" : "false";
  root.dataset.theme = resolvedTheme;
  root.dataset.themeMode = settings.appearance.themeMode;
  root.dataset.workspaceDensity = settings.workspace.density;
  root.style.setProperty(
    "--app-root-font-size",
    formatFontSizePx(settings.appearance.textSize),
  );
  root.style.setProperty(
    "--app-chat-font-size",
    formatFontSizePx(settings.appearance.chatTextSize),
  );
  root.style.setProperty(
    "--app-code-font-size",
    formatFontSizePx(settings.appearance.codeTextSize),
  );
  root.style.setProperty(
    "--app-code-line-number-size",
    formatFontSizePx(
      getCodeLineNumberFontSize(settings.appearance.codeTextSize),
    ),
  );
  root.style.setProperty(
    "--font-sans",
    FONT_FAMILY_STACKS[settings.appearance.fontFamily].sans,
  );
  root.style.setProperty(
    "--font-serif",
    FONT_FAMILY_STACKS[settings.appearance.fontFamily].serif,
  );
  root.style.setProperty(
    "--font-mono",
    CODE_FONT_FAMILY_STACKS[settings.appearance.codeFontFamily],
  );
  root.style.colorScheme = resolvedTheme;
}
