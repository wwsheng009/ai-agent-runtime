export const APP_SETTINGS_STORAGE_KEY = "ai-agent-runtime.workspace.settings";

export type AccentTone = "gold" | "cyan" | "violet";
export type CodeFontPreset = "jetbrains" | "cascadia" | "classic";
export type FontFamilyPreset = "system" | "humanist" | "editorial";
export type ThemeMode = "system" | "light" | "dark";
export type ResolvedTheme = "light" | "dark";
export type WorkspaceDensity = "comfortable" | "compact";
export type ReasoningEffort = "" | "minimal" | "low" | "medium" | "high";

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

export interface AppSettings {
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
  appearance?: Partial<AppSettings["appearance"]>;
  workspace?: Partial<AppSettings["workspace"]>;
  notification?: Partial<AppSettings["notification"]>;
  chat?: Partial<AppSettings["chat"]>;
};

export const DEFAULT_APP_SETTINGS: AppSettings = {
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
  },
  notification: {
    enabled: true,
    desktop: false,
  },
  chat: {
    enableReact: true,
    reasoningEffort: "",
    maxSteps: 10,
  },
};

function normalizeAccentTone(value: unknown): AccentTone {
  return value === "cyan" || value === "violet" ? value : "gold";
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
    const legacyValue = legacyPresets[trimmed];
    if (legacyValue !== undefined) {
      return legacyValue;
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

  return Math.min(20, Math.max(1, Math.round(parsed)));
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
