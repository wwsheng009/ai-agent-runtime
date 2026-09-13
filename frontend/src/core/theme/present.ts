import { type ThemeSnapshot } from "./resolve";

// P0-5：DOM 写入唯一入口。
// - 写入集合显式登记，dispose() 只回收「本次写入且当前值仍等于写入值」的属性，
//   避免误删外部（或后续 present）写入；
// - present 幂等：同一 snapshot 重复调用结果一致，不产生累积状态。

export const THEME_DATASET_KEYS = {
  accentTone: "accentTone",
  reducedMotion: "reducedMotion",
  theme: "theme",
  themeMode: "themeMode",
  workspaceDensity: "workspaceDensity",
} as const;

export const THEME_STYLE_PROPERTIES = [
  "--app-root-font-size",
  "--app-chat-font-size",
  "--app-code-font-size",
  "--app-code-line-number-size",
  "--font-sans",
  "--font-serif",
  "--font-mono",
] as const;

export interface ThemePresenter {
  dispose(): void;
  present(snapshot: ThemeSnapshot): void;
}

export function createThemePresenter(
  root: HTMLElement,
): ThemePresenter {
  const datasetWrites = new Map<string, string>();
  const styleWrites = new Map<string, string>();
  let localeWrite: string | null = null;

  const writeDataset = (key: string, value: string) => {
    root.dataset[key] = value;
    datasetWrites.set(key, value);
  };

  const writeStyle = (property: string, value: string) => {
    root.style.setProperty(property, value);
    styleWrites.set(property, value);
  };

  return {
    present(snapshot) {
      writeDataset(THEME_DATASET_KEYS.accentTone, snapshot.accentTone);
      writeDataset(
        THEME_DATASET_KEYS.reducedMotion,
        snapshot.reducedMotion ? "true" : "false",
      );
      writeDataset(THEME_DATASET_KEYS.theme, snapshot.theme);
      writeDataset(THEME_DATASET_KEYS.themeMode, snapshot.themeMode);
      writeDataset(THEME_DATASET_KEYS.workspaceDensity, snapshot.density);

      writeStyle("--app-root-font-size", snapshot.fontSizes.root);
      writeStyle("--app-chat-font-size", snapshot.fontSizes.chat);
      writeStyle("--app-code-font-size", snapshot.fontSizes.code);
      writeStyle("--app-code-line-number-size", snapshot.fontSizes.codeLineNumber);
      writeStyle("--font-sans", snapshot.fontFamilies.sans);
      writeStyle("--font-serif", snapshot.fontFamilies.serif);
      writeStyle("--font-mono", snapshot.fontFamilies.mono);
      writeStyle("color-scheme", snapshot.theme);

      root.lang = snapshot.locale;
      localeWrite = snapshot.locale;
    },

    dispose() {
      for (const [key, value] of datasetWrites) {
        if (root.dataset[key] === value) {
          delete root.dataset[key];
        }
      }
      datasetWrites.clear();

      for (const [property, value] of styleWrites) {
        if (root.style.getPropertyValue(property) === value) {
          root.style.removeProperty(property);
        }
      }
      styleWrites.clear();

      if (localeWrite !== null && root.lang === localeWrite) {
        root.removeAttribute("lang");
      }
      localeWrite = null;
    },
  };
}

let defaultPresenter: ThemePresenter | null = null;

export function presentThemeSnapshot(snapshot: ThemeSnapshot): void {
  if (typeof document === "undefined") {
    return;
  }

  defaultPresenter ??= createThemePresenter(document.documentElement);
  defaultPresenter.present(snapshot);
}
