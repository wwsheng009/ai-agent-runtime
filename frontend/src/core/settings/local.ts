import type { LocalePreference } from "@/i18n/locale";
// P0-2：右栏宽度常量必须走**相对路径**导入 —— core/theme/boot-script.ts 会把本文件
// 带进 vite.config.ts 的 esbuild 打包图，而该图不解析 `@` 别名（boot-script.ts 同理）。
import {
  RAIL_WIDTH_CONTENT_PX,
  RAIL_WIDTH_MAX_PX,
  type RailWidthMode,
} from "../../lib/layout/rail-width";

export const APP_SETTINGS_STORAGE_KEY = "ai-agent-runtime.workspace.settings";

export type AccentTone = "gold" | "cyan" | "violet";
export type CodeFontPreset = "jetbrains" | "cascadia" | "classic";
export type FontFamilyPreset = "system" | "humanist" | "editorial";
export type ThemeMode = "system" | "light" | "dark";
export type ResolvedTheme = "light" | "dark";
export type WorkspaceDensity = "comfortable" | "compact";
export type ReasoningEffort = "" | "minimal" | "low" | "medium" | "high";
/** P2-6：侧栏会话排序模式偏好（与 `lib/workspace/session-order` 的模式集合对齐）。 */
export type SessionOrderPreference = "updated" | "manual";
/** P2-6 子片 2：侧栏会话分组视图偏好（工作区分组 / 平铺）。 */
export type SessionGroupingPreference = "directory" | "flat";

export const FONT_FAMILY_STACKS: Record<
  FontFamilyPreset,
  { sans: string; serif: string }
> = {
  system: {
    sans: '"Segoe UI", "Helvetica Neue", ui-sans-serif, system-ui, sans-serif',
    serif: '"Georgia", "Times New Roman", ui-serif, serif',
  },
  humanist: {
    sans:
      '"Trebuchet MS", Verdana, "Segoe UI", "Helvetica Neue", ui-sans-serif, system-ui, sans-serif',
    serif:
      '"Palatino Linotype", Palatino, "Book Antiqua", Georgia, ui-serif, serif',
  },
  editorial: {
    sans:
      '"Aptos", "Segoe UI", "Helvetica Neue", Arial, ui-sans-serif, system-ui, sans-serif',
    serif: '"Cambria", "Georgia", "Times New Roman", ui-serif, serif',
  },
};

export const CODE_FONT_FAMILY_STACKS: Record<CodeFontPreset, string> = {
  jetbrains:
    '"JetBrains Mono", "Cascadia Code", "SFMono-Regular", "Consolas", monospace',
  cascadia:
    '"Cascadia Code", "JetBrains Mono", "SFMono-Regular", "Consolas", monospace',
  classic: '"Consolas", "SFMono-Regular", Menlo, Monaco, monospace',
};

export const FONT_SIZE_LIMITS = {
  max: 24,
  min: 11,
  step: 1,
} as const;

export const APP_FONT_SIZE_DEFAULT = 16;
export const CHAT_FONT_SIZE_DEFAULT = 15;
export const CODE_FONT_SIZE_DEFAULT = 13;
export const CODE_LINE_NUMBER_FONT_SIZE_MIN = 10;

const LEGACY_APP_FONT_SIZE_PRESETS = {
  lg: 17,
  md: APP_FONT_SIZE_DEFAULT,
  sm: 15,
} as const;

const LEGACY_CHAT_FONT_SIZE_PRESETS = {
  lg: 16,
  md: CHAT_FONT_SIZE_DEFAULT,
  sm: 14,
} as const;

const LEGACY_CODE_FONT_SIZE_PRESETS = {
  lg: 14,
  md: CODE_FONT_SIZE_DEFAULT,
  sm: 12,
} as const;

// P0-5：首屏启动脚本（core/theme/boot-script.ts）复用同一份历史预设表，避免常量漂移。
export const LEGACY_FONT_SIZE_PRESETS = {
  chat: LEGACY_CHAT_FONT_SIZE_PRESETS,
  code: LEGACY_CODE_FONT_SIZE_PRESETS,
  text: LEGACY_APP_FONT_SIZE_PRESETS,
} as const;

export interface AppSettings {
  localization: {
    locale: LocalePreference;
  };
  appearance: {
    accentTone: AccentTone;
    chatTextSize: number;
    codeFontFamily: CodeFontPreset;
    codeTextSize: number;
    fontFamily: FontFamilyPreset;
    reducedMotion: boolean;
    textSize: number;
    themeMode: ThemeMode;
  };
  workspace: {
    density: WorkspaceDensity;
    autoOpenArtifacts: boolean;
    sessionOrder: SessionOrderPreference;
    sessionGrouping: SessionGroupingPreference;
    /** P0-2：右栏宽度模式（auto = 按面自适应；manual = 用用户拖拽/输入的值）。 */
    rightRailWidthMode: RailWidthMode;
    /** P0-2：manual 模式的宽度意图（px）；auto 下保留但不用，便于一键切回 manual。 */
    rightRailWidthPx: number;
  };
  notification: {
    enabled: boolean;
    desktop: boolean;
  };
  chat: {
    enableReact: boolean;
    reasoningEffort: ReasoningEffort;
    maxSteps: number;
  };
}

export type PartialAppSettings = {
  localization?: Partial<AppSettings["localization"]>;
  appearance?: Partial<AppSettings["appearance"]>;
  workspace?: Partial<AppSettings["workspace"]>;
  notification?: Partial<AppSettings["notification"]>;
  chat?: Partial<AppSettings["chat"]>;
};

export const DEFAULT_APP_SETTINGS: AppSettings = {
  localization: {
    locale: "system",
  },
  appearance: {
    accentTone: "gold",
    chatTextSize: CHAT_FONT_SIZE_DEFAULT,
    codeFontFamily: "jetbrains",
    codeTextSize: CODE_FONT_SIZE_DEFAULT,
    fontFamily: "system",
    reducedMotion: false,
    textSize: APP_FONT_SIZE_DEFAULT,
    themeMode: "system",
  },
  workspace: {
    density: "compact",
    autoOpenArtifacts: true,
    sessionOrder: "updated",
    sessionGrouping: "directory",
    rightRailWidthMode: "auto",
    rightRailWidthPx: RAIL_WIDTH_CONTENT_PX,
  },
  notification: {
    enabled: true,
    desktop: false,
  },
  chat: {
    enableReact: true,
    reasoningEffort: "",
    maxSteps: 0,
  },
};

function normalizeAccentTone(value: unknown): AccentTone {
  return value === "cyan" || value === "violet" ? value : "gold";
}

function normalizeLocalePreference(value: unknown): LocalePreference {
  return value === "zh-CN" || value === "en-US" ? value : "system";
}

function normalizeCodeFontPreset(value: unknown): CodeFontPreset {
  return value === "cascadia" || value === "classic" ? value : "jetbrains";
}

function normalizeFontFamilyPreset(value: unknown): FontFamilyPreset {
  return value === "humanist" || value === "editorial" ? value : "system";
}

function clampFontSize(value: number) {
  return Math.min(
    FONT_SIZE_LIMITS.max,
    Math.max(FONT_SIZE_LIMITS.min, Math.round(value)),
  );
}

function normalizeFontSize(
  value: unknown,
  fallback: number,
  legacyPresets: Record<string, number>,
) {
  if (typeof value === "number" && Number.isFinite(value)) {
    return clampFontSize(value);
  }

  if (typeof value === "string") {
    const trimmed = value.trim().toLowerCase();
    // P0-5：与首屏启动脚本保持一致——只用自有键，避免原型链键（"constructor" 等）
    // 被当成预设值返回非数值。
    if (Object.prototype.hasOwnProperty.call(legacyPresets, trimmed)) {
      return legacyPresets[trimmed];
    }

    const parsed = Number.parseFloat(trimmed);
    if (Number.isFinite(parsed)) {
      return clampFontSize(parsed);
    }
  }

  return clampFontSize(fallback);
}

export function normalizeThemeMode(value: unknown): ThemeMode {
  return value === "light" || value === "dark" ? value : "system";
}

function normalizeWorkspaceDensity(value: unknown): WorkspaceDensity {
  return value === "comfortable" || value === "compact"
    ? value
    : DEFAULT_APP_SETTINGS.workspace.density;
}

/** P0-2：只认两种右栏宽度模式，未知值回落到 auto（不做隐式映射）。 */
function normalizeRightRailWidthMode(value: unknown): RailWidthMode {
  return value === "manual" || value === "auto"
    ? value
    : DEFAULT_APP_SETTINGS.workspace.rightRailWidthMode;
}

/**
 * P0-2：持久化宽度只做「与视口无关」的合法性收敛（下界用内容型面宽度，
 * 因为 auto 默认值 288 必须保持合法）；视口相关的上界收窄统一由 `clampRailWidth()` 在渲染期完成。
 */
function normalizeRightRailWidthPx(value: unknown): number {
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) {
    return DEFAULT_APP_SETTINGS.workspace.rightRailWidthPx;
  }

  return Math.min(
    RAIL_WIDTH_MAX_PX,
    Math.max(RAIL_WIDTH_CONTENT_PX, Math.round(parsed)),
  );
}

/** 只认两种排序模式，未知值回落到默认（不做隐式映射）。 */
function normalizeSessionOrderPreference(
  value: unknown,
): SessionOrderPreference {
  return value === "manual" || value === "updated"
    ? value
    : DEFAULT_APP_SETTINGS.workspace.sessionOrder;
}

/** 只认两种分组视图，未知值回落到默认（不做隐式映射）。 */
function normalizeSessionGroupingPreference(
  value: unknown,
): SessionGroupingPreference {
  return value === "flat" || value === "directory"
    ? value
    : DEFAULT_APP_SETTINGS.workspace.sessionGrouping;
}

function normalizeReasoningEffort(value: unknown): ReasoningEffort {
  return value === "minimal" ||
    value === "low" ||
    value === "medium" ||
    value === "high"
    ? value
    : "";
}

function normalizeMaxSteps(value: unknown) {
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) {
    return DEFAULT_APP_SETTINGS.chat.maxSteps;
  }

  return Math.min(20, Math.max(0, Math.round(parsed)));
}

function normalizeEnableReact(value: unknown) {
  return toBoolean(value, DEFAULT_APP_SETTINGS.chat.enableReact);
}

function toBoolean(value: unknown, fallback: boolean) {
  return typeof value === "boolean" ? value : fallback;
}

export function mergeAppSettings(
  value: PartialAppSettings | null | undefined,
): AppSettings {
  return {
    localization: {
      locale: normalizeLocalePreference(value?.localization?.locale),
    },
    appearance: {
      accentTone: normalizeAccentTone(value?.appearance?.accentTone),
      chatTextSize: normalizeFontSize(
        value?.appearance?.chatTextSize,
        DEFAULT_APP_SETTINGS.appearance.chatTextSize,
        LEGACY_CHAT_FONT_SIZE_PRESETS,
      ),
      codeFontFamily: normalizeCodeFontPreset(
        value?.appearance?.codeFontFamily,
      ),
      codeTextSize: normalizeFontSize(
        value?.appearance?.codeTextSize,
        DEFAULT_APP_SETTINGS.appearance.codeTextSize,
        LEGACY_CODE_FONT_SIZE_PRESETS,
      ),
      fontFamily: normalizeFontFamilyPreset(value?.appearance?.fontFamily),
      reducedMotion: toBoolean(
        value?.appearance?.reducedMotion,
        DEFAULT_APP_SETTINGS.appearance.reducedMotion,
      ),
      textSize: normalizeFontSize(
        value?.appearance?.textSize,
        DEFAULT_APP_SETTINGS.appearance.textSize,
        LEGACY_APP_FONT_SIZE_PRESETS,
      ),
      themeMode: normalizeThemeMode(value?.appearance?.themeMode),
    },
    workspace: {
      density: normalizeWorkspaceDensity(value?.workspace?.density),
      autoOpenArtifacts: toBoolean(
        value?.workspace?.autoOpenArtifacts,
        DEFAULT_APP_SETTINGS.workspace.autoOpenArtifacts,
      ),
      rightRailWidthMode: normalizeRightRailWidthMode(
        value?.workspace?.rightRailWidthMode,
      ),
      rightRailWidthPx: normalizeRightRailWidthPx(
        value?.workspace?.rightRailWidthPx,
      ),
      sessionOrder: normalizeSessionOrderPreference(
        value?.workspace?.sessionOrder,
      ),
      sessionGrouping: normalizeSessionGroupingPreference(
        value?.workspace?.sessionGrouping,
      ),
    },
    notification: {
      enabled: toBoolean(
        value?.notification?.enabled,
        DEFAULT_APP_SETTINGS.notification.enabled,
      ),
      desktop: toBoolean(
        value?.notification?.desktop,
        DEFAULT_APP_SETTINGS.notification.desktop,
      ),
    },
    chat: {
      enableReact: normalizeEnableReact(value?.chat?.enableReact),
      reasoningEffort: normalizeReasoningEffort(value?.chat?.reasoningEffort),
      maxSteps: normalizeMaxSteps(value?.chat?.maxSteps),
    },
  };
}

export function resolveThemeMode(
  themeMode: ThemeMode,
  systemTheme: ResolvedTheme,
): ResolvedTheme {
  return themeMode === "system" ? systemTheme : themeMode;
}

export function formatFontSizePx(value: number) {
  return `${Math.round(value)}px`;
}

export function getCodeLineNumberFontSize(value: number) {
  return Math.max(
    CODE_LINE_NUMBER_FONT_SIZE_MIN,
    clampFontSize(value) - 1,
  );
}

export function getStoredAppSettings(
  storage: Storage | null | undefined,
): AppSettings {
  if (!storage) {
    return DEFAULT_APP_SETTINGS;
  }

  try {
    const raw = storage.getItem(APP_SETTINGS_STORAGE_KEY);
    if (!raw) {
      return DEFAULT_APP_SETTINGS;
    }

    return mergeAppSettings(JSON.parse(raw) as PartialAppSettings);
  } catch {
    return DEFAULT_APP_SETTINGS;
  }
}

export function writeStoredAppSettings(
  storage: Storage | null | undefined,
  settings: AppSettings,
) {
  if (!storage) {
    return;
  }

  storage.setItem(APP_SETTINGS_STORAGE_KEY, JSON.stringify(settings));
}
