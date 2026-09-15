// 批次 18（P2-2 子片 1）：校验面板契约测试（错误阻断提示 / 提示不阻断 / 无问题不渲染）。

import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { type ConfigValidationIssue } from "../../runtime-config-validation";
import { type ConfigEditorCore } from "../use-config-core";
import { ConfigDraftValidationPanel } from "./validation-panel";

function buildCore(issues: ConfigValidationIssue[]) {
  return {
    draftIssues: issues,
    t: (key: string, params?: Record<string, unknown>) =>
      params ? `${key} ${JSON.stringify(params)}` : key,
  } as unknown as ConfigEditorCore;
}

describe("ConfigDraftValidationPanel", () => {
  it("没有问题时完全不渲染", () => {
    expect(renderToStaticMarkup(<ConfigDraftValidationPanel core={buildCore([])} />)).toBe("");
  });

  it("错误级问题渲染字段路径、文案与阻断提示", () => {
    const markup = renderToStaticMarkup(
      <ConfigDraftValidationPanel
        core={buildCore([
          {
            messageKey: "providerFieldNotString",
            params: { field: "api_key" },
            path: ["providers", "items", "primary", "api_key"],
            severity: "error",
          },
        ])}
      />,
    );

    expect(markup).toContain('data-testid="config-draft-validation"');
    expect(markup).toContain('role="alert"');
    expect(markup).toContain("providers.items.primary.api_key");
    expect(markup).toContain(
      "editor.draftValidation.providerFieldNotString {&quot;field&quot;:&quot;api_key&quot;}",
    );
    expect(markup).toContain("editor.draftValidation.errorBadge");
    expect(markup).toContain("editor.draftValidation.blockedHint");
  });

  it("仅提示级问题时给 status 语义且不出现阻断提示", () => {
    const markup = renderToStaticMarkup(
      <ConfigDraftValidationPanel
        core={buildCore([
          {
            messageKey: "defaultProviderUnknown",
            params: { name: "ghost" },
            path: ["providers", "default_provider"],
            severity: "warning",
          },
        ])}
      />,
    );

    expect(markup).toContain('role="status"');
    expect(markup).toContain("providers.default_provider");
    expect(markup).toContain("editor.draftValidation.warningBadge");
    expect(markup).not.toContain("editor.draftValidation.blockedHint");
    expect(markup).not.toContain("editor.draftValidation.errorBadge");
  });

  it("根路径问题显示为 (root)", () => {
    const markup = renderToStaticMarkup(
      <ConfigDraftValidationPanel
        core={buildCore([
          { messageKey: "rootNotMapping", path: [], severity: "error" },
        ])}
      />,
    );

    expect(markup).toContain("(root)");
    expect(markup).toContain("editor.draftValidation.rootNotMapping");
  });
});
