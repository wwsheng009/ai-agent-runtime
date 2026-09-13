import { afterEach, describe, expect, it } from "vitest";

import { applyDocumentSettings } from "@/core/settings/document";
import { mergeAppSettings } from "@/core/settings/local";
import {
  createThemePresenter,
  resolveThemeSnapshot,
  THEME_DATASET_KEYS,
  THEME_STYLE_PROPERTIES,
} from "@/core/theme";

const READ_STYLE_PROPERTIES = [
  ...THEME_STYLE_PROPERTIES,
  "color-scheme",
] as const;

function readState(root: HTMLElement) {
  return {
    dataset: { ...root.dataset },
    lang: root.getAttribute("lang"),
    style: Object.fromEntries(
      READ_STYLE_PROPERTIES.map((property) => [
        property,
        root.style.getPropertyValue(property) || null,
      ]),
    ),
  };
}

function resetRoot(root: HTMLElement) {
  for (const key of Object.values(THEME_DATASET_KEYS)) {
    delete root.dataset[key];
  }
  root.removeAttribute("lang");
  for (const property of READ_STYLE_PROPERTIES) {
    root.style.removeProperty(property);
  }
}

function createSnapshot(overrides: Parameters<typeof mergeAppSettings>[0] = {}) {
  return resolveThemeSnapshot(
    mergeAppSettings(overrides),
    "dark",
    "zh-CN",
  );
}

afterEach(() => {
  resetRoot(document.documentElement);
});

describe("createThemePresenter", () => {
  it("writes the full theme surface onto the root element", () => {
    const root = document.createElement("div");
    const snapshot = createSnapshot({
      appearance: {
        accentTone: "cyan",
        chatTextSize: 16,
        codeFontFamily: "classic",
        codeTextSize: 12,
        fontFamily: "humanist",
        reducedMotion: true,
        textSize: 18,
        themeMode: "dark",
      },
      workspace: { density: "comfortable" },
    });

    createThemePresenter(root).present(snapshot);

    expect(root.dataset.accentTone).toBe("cyan");
    expect(root.dataset.reducedMotion).toBe("true");
    expect(root.dataset.theme).toBe("dark");
    expect(root.dataset.themeMode).toBe("dark");
    expect(root.dataset.workspaceDensity).toBe("comfortable");
    expect(root.lang).toBe("zh-CN");
    expect(root.style.getPropertyValue("--app-root-font-size")).toBe("18px");
    expect(root.style.getPropertyValue("--app-chat-font-size")).toBe("16px");
    expect(root.style.getPropertyValue("--app-code-font-size")).toBe("12px");
    expect(root.style.getPropertyValue("--app-code-line-number-size")).toBe(
      "11px",
    );
    expect(root.style.getPropertyValue("--font-sans")).toBe(
      snapshot.fontFamilies.sans,
    );
    expect(root.style.getPropertyValue("--font-serif")).toBe(
      snapshot.fontFamilies.serif,
    );
    expect(root.style.getPropertyValue("--font-mono")).toBe(
      snapshot.fontFamilies.mono,
    );
    expect(root.style.getPropertyValue("color-scheme")).toBe("dark");
  });

  it("is idempotent across repeated presentations", () => {
    const root = document.createElement("div");
    const presenter = createThemePresenter(root);
    const snapshot = createSnapshot({ appearance: { themeMode: "light" } });

    presenter.present(snapshot);
    const first = readState(root);
    presenter.present(snapshot);
    presenter.present(snapshot);

    expect(readState(root)).toEqual(first);
  });

  it("overwrites previous values when the snapshot changes", () => {
    const root = document.createElement("div");
    const presenter = createThemePresenter(root);

    presenter.present(createSnapshot({ appearance: { themeMode: "dark" } }));
    presenter.present(
      createSnapshot({
        appearance: { accentTone: "violet", textSize: 21, themeMode: "light" },
        workspace: { density: "comfortable" },
      }),
    );

    expect(root.dataset.theme).toBe("light");
    expect(root.dataset.accentTone).toBe("violet");
    expect(root.dataset.workspaceDensity).toBe("comfortable");
    expect(root.style.getPropertyValue("--app-root-font-size")).toBe("21px");
  });

  it("retracts only its own writes on dispose", () => {
    const root = document.createElement("div");
    const presenter = createThemePresenter(root);

    presenter.present(createSnapshot({ appearance: { themeMode: "dark" } }));
    // 外部写入：dispose 必须保留这些值，避免误删他人状态。
    root.dataset.theme = "light";
    root.style.setProperty("--font-mono", "external-mono");

    presenter.dispose();

    expect(root.dataset.theme).toBe("light");
    expect(root.style.getPropertyValue("--font-mono")).toBe("external-mono");

    expect(root.dataset.accentTone).toBeUndefined();
    expect(root.dataset.reducedMotion).toBeUndefined();
    expect(root.dataset.themeMode).toBeUndefined();
    expect(root.dataset.workspaceDensity).toBeUndefined();
    expect(root.getAttribute("lang")).toBeNull();
    expect(root.style.getPropertyValue("--app-root-font-size")).toBe("");
    expect(root.style.getPropertyValue("--app-chat-font-size")).toBe("");
    expect(root.style.getPropertyValue("--app-code-font-size")).toBe("");
    expect(root.style.getPropertyValue("--app-code-line-number-size")).toBe("");
    expect(root.style.getPropertyValue("--font-sans")).toBe("");
    expect(root.style.getPropertyValue("--font-serif")).toBe("");
    expect(root.style.getPropertyValue("color-scheme")).toBe("");
  });

  it("can present again after dispose", () => {
    const root = document.createElement("div");
    const presenter = createThemePresenter(root);

    presenter.present(createSnapshot());
    presenter.dispose();
    presenter.present(createSnapshot({ appearance: { themeMode: "light" } }));

    expect(root.dataset.theme).toBe("light");
    expect(root.style.getPropertyValue("--app-root-font-size")).toBe("16px");
  });
});

describe("applyDocumentSettings compatibility entry", () => {
  it("writes the same state as resolve + present", () => {
    const settings = mergeAppSettings({
      appearance: { accentTone: "violet", themeMode: "system", textSize: 19 },
      localization: { locale: "en-US" },
      workspace: { density: "comfortable" },
    });

    applyDocumentSettings(settings, "dark", "en-US");
    const viaCompatibilityEntry = readState(document.documentElement);

    const referenceRoot = document.createElement("div");
    createThemePresenter(referenceRoot).present(
      resolveThemeSnapshot(settings, "dark", "en-US"),
    );

    expect(viaCompatibilityEntry).toEqual(readState(referenceRoot));
  });
});
