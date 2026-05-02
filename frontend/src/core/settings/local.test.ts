import { describe, expect, it } from "vitest";

import {
  APP_SETTINGS_STORAGE_KEY,
  DEFAULT_APP_SETTINGS,
  getStoredAppSettings,
  mergeAppSettings,
  resolveThemeMode,
  writeStoredAppSettings,
} from "@/core/settings/local";

class MemoryStorage implements Storage {
  private values = new Map<string, string>();

  get length() {
    return this.values.size;
  }

  clear() {
    this.values.clear();
  }

  getItem(key: string) {
    return this.values.get(key) ?? null;
  }

  key(index: number) {
    return [...this.values.keys()][index] ?? null;
  }

  removeItem(key: string) {
    this.values.delete(key);
  }

  setItem(key: string, value: string) {
    this.values.set(key, value);
  }
}

describe("app settings storage helpers", () => {
  it("merges persisted values with defaults", () => {
    expect(
      mergeAppSettings({
        appearance: {
          accentTone: "cyan",
          chatTextSize: 18,
          codeFontFamily: "classic",
          codeTextSize: 12,
          fontFamily: "humanist",
          reducedMotion: true,
          textSize: 17,
          themeMode: "light",
        },
        workspace: {
          density: "compact",
        },
        notification: {
          desktop: true,
        },
        chat: {
          enableReact: false,
          reasoningEffort: "high",
          maxSteps: 14,
        },
      }),
    ).toEqual({
      localization: {
        locale: "system",
      },
      appearance: {
        accentTone: "cyan",
        chatTextSize: 18,
        codeFontFamily: "classic",
        codeTextSize: 12,
        fontFamily: "humanist",
        reducedMotion: true,
        textSize: 17,
        themeMode: "light",
      },
      workspace: {
        density: "compact",
        autoOpenArtifacts: true,
      },
      notification: {
        enabled: true,
        desktop: true,
      },
      chat: {
        enableReact: false,
        reasoningEffort: "high",
        maxSteps: 14,
      },
    });
  });

  it("normalizes invalid values back to safe defaults", () => {
    expect(
      mergeAppSettings({
        appearance: {
          accentTone: "unknown" as never,
          chatTextSize: "xl" as never,
          codeFontFamily: "terminal" as never,
          codeTextSize: "tiny" as never,
          fontFamily: "display" as never,
          themeMode: "neon" as never,
          textSize: "huge" as never,
        },
        workspace: {
          density: "dense" as never,
        },
        chat: {
          enableReact: "yes" as never,
          reasoningEffort: "extreme" as never,
          maxSteps: 200,
        },
      }),
    ).toEqual({
      ...DEFAULT_APP_SETTINGS,
      appearance: {
        accentTone: "gold",
        chatTextSize: 15,
        codeFontFamily: "jetbrains",
        codeTextSize: 13,
        fontFamily: "system",
        reducedMotion: false,
        textSize: 16,
        themeMode: "system",
      },
      chat: {
        enableReact: true,
        reasoningEffort: "",
        maxSteps: 20,
      },
    });
  });

  it("round-trips settings through storage", () => {
    const storage = new MemoryStorage();

    writeStoredAppSettings(storage, {
      ...DEFAULT_APP_SETTINGS,
      appearance: {
        ...DEFAULT_APP_SETTINGS.appearance,
        codeFontFamily: "cascadia",
        codeTextSize: 14,
        fontFamily: "editorial",
        textSize: 15,
        themeMode: "dark",
      },
      notification: {
        enabled: true,
        desktop: true,
      },
    });

    expect(storage.getItem(APP_SETTINGS_STORAGE_KEY)).not.toBeNull();
    expect(getStoredAppSettings(storage)).toEqual({
      ...DEFAULT_APP_SETTINGS,
      appearance: {
        ...DEFAULT_APP_SETTINGS.appearance,
        codeFontFamily: "cascadia",
        codeTextSize: 14,
        fontFamily: "editorial",
        textSize: 15,
        themeMode: "dark",
      },
      notification: {
        enabled: true,
        desktop: true,
      },
    });
  });

  it("migrates legacy font size presets into pixel values", () => {
    expect(
      mergeAppSettings({
        appearance: {
          chatTextSize: "lg" as never,
          codeTextSize: "sm" as never,
          textSize: "md" as never,
        },
      }),
    ).toEqual({
      ...DEFAULT_APP_SETTINGS,
      appearance: {
        ...DEFAULT_APP_SETTINGS.appearance,
        chatTextSize: 16,
        codeTextSize: 12,
        textSize: 16,
      },
    });
  });

  it("resolves system theme against the current OS preference", () => {
    expect(resolveThemeMode("system", "dark")).toBe("dark");
    expect(resolveThemeMode("system", "light")).toBe("light");
    expect(resolveThemeMode("dark", "light")).toBe("dark");
    expect(resolveThemeMode("light", "dark")).toBe("light");
  });
});
