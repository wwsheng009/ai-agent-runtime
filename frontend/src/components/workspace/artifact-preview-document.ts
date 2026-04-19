import { formatFontSizePx } from "@/core/settings/local";

export type ArtifactPreviewDocumentSettings = {
  codeTextSize: number;
  textSize: number;
};

const PREVIEW_SETTINGS_STYLE_MARKER = "data-app-preview-settings";
const PREVIEW_SETTINGS_STYLE_PATTERN =
  /<style[^>]*data-app-preview-settings[^>]*>[\s\S]*?<\/style>/i;

function buildPreviewSettingsStyleTag(
  settings: ArtifactPreviewDocumentSettings,
) {
  return [
    `<style ${PREVIEW_SETTINGS_STYLE_MARKER}>`,
    ":root {",
    `  --app-preview-root-font-size: ${formatFontSizePx(settings.textSize)};`,
    `  --app-preview-code-font-size: ${formatFontSizePx(settings.codeTextSize)};`,
    "}",
    "html {",
    "  font-size: var(--app-preview-root-font-size) !important;",
    "}",
    "body {",
    "  font-size: 1rem !important;",
    "}",
    "pre, code, kbd, samp {",
    "  font-size: var(--app-preview-code-font-size) !important;",
    "}",
    "button, input, textarea, select {",
    "  font: inherit;",
    "}",
    "</style>",
  ].join("\n");
}

export function injectPreviewDocumentSettings(
  previewHtml: string,
  settings: ArtifactPreviewDocumentSettings,
) {
  const previewSettingsStyleTag = buildPreviewSettingsStyleTag(settings);

  if (PREVIEW_SETTINGS_STYLE_PATTERN.test(previewHtml)) {
    return previewHtml.replace(
      PREVIEW_SETTINGS_STYLE_PATTERN,
      previewSettingsStyleTag,
    );
  }

  if (/<\/head>/i.test(previewHtml)) {
    return previewHtml.replace(
      /<\/head>/i,
      `${previewSettingsStyleTag}\n</head>`,
    );
  }

  if (/<html\b[^>]*>/i.test(previewHtml)) {
    return previewHtml.replace(
      /<html\b[^>]*>/i,
      (match) => `${match}\n<head>\n${previewSettingsStyleTag}\n</head>`,
    );
  }

  return `${previewSettingsStyleTag}\n${previewHtml}`;
}
