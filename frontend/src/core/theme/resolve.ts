import {
  CODE_FONT_FAMILY_STACKS,
  FONT_FAMILY_STACKS,
  formatFontSizePx,
  getCodeLineNumberFontSize,
  resolveThemeMode,
  type AccentTone,
  type AppSettings,
  type ResolvedTheme,
  type ThemeMode,
  type WorkspaceDensity,
} from "@/core/settings/local";
import { type ResolvedLocale } from "@/i18n/locale";

// P0-5：主题「解析」与「应用」分离。
// resolve 只把设置 + 系统主题 + 语言换算成不可变 snapshot（纯函数、无 DOM）；
// present（见 ./present.ts）只负责把 snapshot 写进 document。

export interface ThemeSnapshot {
  accentTone: AccentTone;
  density: WorkspaceDensity;
  fontFamilies: {
    mono: string;
    sans: string;
    serif: string;
  };
  fontSizes: {
    chat: string;
    code: string;
    codeLineNumber: string;
    root: string;
  };
  locale: ResolvedLocale;
  reducedMotion: boolean;
  theme: ResolvedTheme;
  themeMode: ThemeMode;
}

export function resolveThemeSnapshot(
  settings: AppSettings,
  systemTheme: ResolvedTheme,
  resolvedLocale: ResolvedLocale,
): ThemeSnapshot {
  const { appearance } = settings;

  return {
    accentTone: appearance.accentTone,
    density: settings.workspace.density,
    fontFamilies: {
      mono: CODE_FONT_FAMILY_STACKS[appearance.codeFontFamily],
      sans: FONT_FAMILY_STACKS[appearance.fontFamily].sans,
      serif: FONT_FAMILY_STACKS[appearance.fontFamily].serif,
    },
    fontSizes: {
      chat: formatFontSizePx(appearance.chatTextSize),
      code: formatFontSizePx(appearance.codeTextSize),
      codeLineNumber: formatFontSizePx(
        getCodeLineNumberFontSize(appearance.codeTextSize),
      ),
      root: formatFontSizePx(appearance.textSize),
    },
    locale: resolvedLocale,
    reducedMotion: appearance.reducedMotion,
    theme: resolveThemeMode(appearance.themeMode, systemTheme),
    themeMode: appearance.themeMode,
  };
}
