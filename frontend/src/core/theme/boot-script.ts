import {
  APP_SETTINGS_STORAGE_KEY,
  CODE_FONT_FAMILY_STACKS,
  CODE_LINE_NUMBER_FONT_SIZE_MIN,
  DEFAULT_APP_SETTINGS,
  FONT_FAMILY_STACKS,
  FONT_SIZE_LIMITS,
  LEGACY_FONT_SIZE_PRESETS,
} from "../settings/local";

// P0-5：首屏（HTML 解析期）主题启动脚本。
// - 写入与 core/theme/present.ts 完全相同的 dataset / CSS 变量 / lang / color-scheme；
// - 由 vite.config.ts 注入 index.html 的 <head>，早于样式表与模块脚本执行，
//   消除「先按默认主题绘制、再被 React 纠正」的闪白/闪黑；
// - 全部常量取自 core/settings/local.ts（此处用相对路径导入，保证 vite.config.ts
//   在 Node 侧打包配置时也能解析，无需 `@` 别名）；
// - 逻辑等价性由 boot-script.test.ts 以矩阵方式与 resolve + present 输出逐项对比。
//
// 注：历史预设查表统一使用 hasOwnProperty（local.ts 同步加固），避免原型链键
// （如 "constructor"）被当作预设值，从而保证两条路径对所有输入完全等价。

export const THEME_BOOT_PLACEHOLDER = "<!--theme-boot-script-->";
export const THEME_BOOT_ATTRIBUTE = "data-theme-boot";

const BOOT_CONFIG = {
  storageKey: APP_SETTINGS_STORAGE_KEY,
  defaults: {
    accentTone: DEFAULT_APP_SETTINGS.appearance.accentTone,
    chatTextSize: DEFAULT_APP_SETTINGS.appearance.chatTextSize,
    codeFontFamily: DEFAULT_APP_SETTINGS.appearance.codeFontFamily,
    codeTextSize: DEFAULT_APP_SETTINGS.appearance.codeTextSize,
    density: DEFAULT_APP_SETTINGS.workspace.density,
    fontFamily: DEFAULT_APP_SETTINGS.appearance.fontFamily,
    locale: DEFAULT_APP_SETTINGS.localization.locale,
    reducedMotion: DEFAULT_APP_SETTINGS.appearance.reducedMotion,
    textSize: DEFAULT_APP_SETTINGS.appearance.textSize,
    themeMode: DEFAULT_APP_SETTINGS.appearance.themeMode,
  },
  codeFontStacks: CODE_FONT_FAMILY_STACKS,
  fontStacks: FONT_FAMILY_STACKS,
  legacyPresets: LEGACY_FONT_SIZE_PRESETS,
  limits: FONT_SIZE_LIMITS,
  lineNumberMin: CODE_LINE_NUMBER_FONT_SIZE_MIN,
} as const;

export function buildThemeBootScript(): string {
  return `(function () {
  var root = document.documentElement;
  var config = ${JSON.stringify(BOOT_CONFIG)};
  var appearance = {};
  var localization = {};
  var workspace = {};

  try {
    var raw = window.localStorage.getItem(config.storageKey);
    var stored = raw ? JSON.parse(raw) : null;
    if (stored && typeof stored === "object") {
      if (stored.appearance && typeof stored.appearance === "object") {
        appearance = stored.appearance;
      }
      if (stored.localization && typeof stored.localization === "object") {
        localization = stored.localization;
      }
      if (stored.workspace && typeof stored.workspace === "object") {
        workspace = stored.workspace;
      }
    }
  } catch (error) {
    appearance = {};
    localization = {};
    workspace = {};
  }

  function clampFontSize(value) {
    return Math.min(config.limits.max, Math.max(config.limits.min, Math.round(value)));
  }

  function normalizeFontSize(value, fallback, presets) {
    if (typeof value === "number" && isFinite(value)) {
      return clampFontSize(value);
    }
    if (typeof value === "string") {
      var trimmed = value.trim().toLowerCase();
      if (Object.prototype.hasOwnProperty.call(presets, trimmed)) {
        return presets[trimmed];
      }
      var parsed = parseFloat(trimmed);
      if (isFinite(parsed)) {
        return clampFontSize(parsed);
      }
    }
    return clampFontSize(fallback);
  }

  function pick(value, allowed, fallback) {
    return allowed.indexOf(value) === -1 ? fallback : value;
  }

  function px(value) {
    return Math.round(value) + "px";
  }

  var themeMode = pick(appearance.themeMode, ["light", "dark"], config.defaults.themeMode);
  var systemTheme = "dark";
  if (typeof window.matchMedia === "function") {
    systemTheme = window.matchMedia("(prefers-color-scheme: dark)").matches
      ? "dark"
      : "light";
  }
  var theme = themeMode === "system" ? systemTheme : themeMode;

  var localePreference = pick(
    localization.locale,
    ["zh-CN", "en-US"],
    config.defaults.locale
  );
  var locale = localePreference;
  if (localePreference === "system") {
    if (typeof navigator === "undefined") {
      locale = "zh-CN";
    } else {
      var language =
        typeof navigator.language === "string"
          ? navigator.language.trim().toLowerCase()
          : "";
      locale = language.indexOf("zh") === 0 ? "zh-CN" : "en-US";
    }
  }

  var accentTone = pick(
    appearance.accentTone,
    ["cyan", "violet"],
    config.defaults.accentTone
  );
  var density = pick(
    workspace.density,
    ["comfortable", "compact"],
    config.defaults.density
  );
  var reducedMotion =
    typeof appearance.reducedMotion === "boolean"
      ? appearance.reducedMotion
      : config.defaults.reducedMotion;
  var fontFamily = pick(
    appearance.fontFamily,
    ["humanist", "editorial"],
    config.defaults.fontFamily
  );
  var codeFontFamily = pick(
    appearance.codeFontFamily,
    ["cascadia", "classic"],
    config.defaults.codeFontFamily
  );

  var textSize = normalizeFontSize(
    appearance.textSize,
    config.defaults.textSize,
    config.legacyPresets.text
  );
  var chatTextSize = normalizeFontSize(
    appearance.chatTextSize,
    config.defaults.chatTextSize,
    config.legacyPresets.chat
  );
  var codeTextSize = normalizeFontSize(
    appearance.codeTextSize,
    config.defaults.codeTextSize,
    config.legacyPresets.code
  );
  var lineNumberSize = Math.max(config.lineNumberMin, codeTextSize - 1);

  root.dataset.accentTone = accentTone;
  root.dataset.reducedMotion = reducedMotion ? "true" : "false";
  root.dataset.theme = theme;
  root.dataset.themeMode = themeMode;
  root.dataset.workspaceDensity = density;
  root.style.setProperty("--app-root-font-size", px(textSize));
  root.style.setProperty("--app-chat-font-size", px(chatTextSize));
  root.style.setProperty("--app-code-font-size", px(codeTextSize));
  root.style.setProperty("--app-code-line-number-size", px(lineNumberSize));
  root.style.setProperty("--font-sans", config.fontStacks[fontFamily].sans);
  root.style.setProperty("--font-serif", config.fontStacks[fontFamily].serif);
  root.style.setProperty("--font-mono", config.codeFontStacks[codeFontFamily]);
  root.style.colorScheme = theme;
  root.lang = locale;
})();`;
}
