import { describe, expect, it } from "vitest";

import { injectPreviewDocumentSettings } from "./artifact-preview-document";

describe("artifact preview document helpers", () => {
  it("injects configured font sizes into documents with a head tag", () => {
    const html = [
      "<!doctype html>",
      "<html>",
      "<head>",
      '  <meta charset="UTF-8" />',
      "</head>",
      "<body><p>Preview</p><pre>code</pre></body>",
      "</html>",
    ].join("\n");

    const result = injectPreviewDocumentSettings(html, {
      codeTextSize: 13,
      textSize: 18,
    });

    expect(result).toContain("--app-preview-root-font-size: 18px;");
    expect(result).toContain("--app-preview-code-font-size: 13px;");
    expect(result).toMatch(
      /<style data-app-preview-settings>[\s\S]*<\/style>\s*<\/head>/i,
    );
  });

  it("adds a head block when the document has html but no head", () => {
    const html = "<html><body><p>Preview</p></body></html>";

    const result = injectPreviewDocumentSettings(html, {
      codeTextSize: 14,
      textSize: 17,
    });

    expect(result).toMatch(
      /<html>\s*<head>\s*<style data-app-preview-settings>[\s\S]*<\/style>\s*<\/head>\s*<body>/i,
    );
    expect(result).toContain("--app-preview-root-font-size: 17px;");
  });

  it("prepends the style block for HTML fragments", () => {
    const html = "<main><p>Fragment</p></main>";

    const result = injectPreviewDocumentSettings(html, {
      codeTextSize: 12,
      textSize: 16,
    });

    expect(result.startsWith("<style data-app-preview-settings>")).toBe(true);
    expect(result).toContain(html);
  });

  it("replaces an existing injected style block instead of appending duplicates", () => {
    const firstPass = injectPreviewDocumentSettings(
      "<html><head></head><body><p>Preview</p></body></html>",
      {
        codeTextSize: 13,
        textSize: 16,
      },
    );

    const secondPass = injectPreviewDocumentSettings(firstPass, {
      codeTextSize: 15,
      textSize: 19,
    });

    expect(
      secondPass.match(/<style data-app-preview-settings>/g)?.length ?? 0,
    ).toBe(1);
    expect(secondPass).toContain("--app-preview-root-font-size: 19px;");
    expect(secondPass).toContain("--app-preview-code-font-size: 15px;");
  });
});
