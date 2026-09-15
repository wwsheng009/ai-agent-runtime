// 批次 18（P2-2 子片 1）：草稿写入前置校验单测。

import { describe, expect, it } from "vitest";

import {
  countConfigIssues,
  formatConfigIssuePath,
  validateRuntimeConfigRawDraft,
  validateRuntimeConfigStructuredDraft,
} from "./runtime-config-validation";

describe("formatConfigIssuePath", () => {
  it("根路径显示为 (root)，其余按点号拼接", () => {
    expect(formatConfigIssuePath([])).toBe("(root)");
    expect(
      formatConfigIssuePath(["providers", "items", "primary", "api_key"]),
    ).toBe("providers.items.primary.api_key");
    expect(formatConfigIssuePath(["servers", 0, "port"])).toBe("servers.0.port");
  });
});

describe("validateRuntimeConfigRawDraft", () => {
  it("空白源码草稿给出错误，非空草稿不报", () => {
    expect(validateRuntimeConfigRawDraft("")).toEqual([
      { messageKey: "rawEmpty", path: [], severity: "error" },
    ]);
    expect(validateRuntimeConfigRawDraft("  \n\t ")).toHaveLength(1);
    expect(validateRuntimeConfigRawDraft("providers: {}\n")).toEqual([]);
  });
});

describe("validateRuntimeConfigStructuredDraft", () => {
  it("非对象根给出错误", () => {
    for (const value of [null, undefined, 42, "providers", [1, 2]]) {
      const issues = validateRuntimeConfigStructuredDraft(value);
      expect(issues).toHaveLength(1);
      expect(issues[0]).toMatchObject({
        messageKey: "rootNotMapping",
        path: [],
        severity: "error",
      });
    }
  });

  it("合法文档不报任何问题", () => {
    expect(
      validateRuntimeConfigStructuredDraft({
        providers: {
          default_provider: "primary",
          items: {
            primary: {
              enabled: true,
              protocol: "openai",
              base_url: "https://example.com",
              api_key: "sk-***",
              supported_models: ["gpt-4o"],
              headers: { "x-trace": "1" },
            },
          },
        },
      }),
    ).toEqual([]);
  });

  it("缺少 providers 段不误报", () => {
    expect(validateRuntimeConfigStructuredDraft({ auth: {} })).toEqual([]);
  });

  it("providers 段不是映射时给出错误并停止深入", () => {
    const issues = validateRuntimeConfigStructuredDraft({ providers: "nope" });
    expect(issues).toEqual([
      { messageKey: "providersNotMapping", path: ["providers"], severity: "error" },
    ]);
  });

  it("providers.items 不是映射时给出错误", () => {
    const issues = validateRuntimeConfigStructuredDraft({
      providers: { items: [] },
    });
    expect(issues).toEqual([
      {
        messageKey: "providersItemsNotMapping",
        path: ["providers", "items"],
        severity: "error",
      },
    ]);
  });

  it("provider 条目不是映射时给出带名字的错误", () => {
    const issues = validateRuntimeConfigStructuredDraft({
      providers: { items: { primary: "nope" } },
    });
    expect(issues).toEqual([
      {
        messageKey: "providerNotMapping",
        params: { name: "primary" },
        path: ["providers", "items", "primary"],
        severity: "error",
      },
    ]);
  });

  it("null 条目按未设置处理，不制造告警", () => {
    expect(
      validateRuntimeConfigStructuredDraft({
        providers: { items: { primary: null } },
      }),
    ).toEqual([]);
  });

  it("provider 标量字段类型不符时给出字段级错误", () => {
    const issues = validateRuntimeConfigStructuredDraft({
      providers: {
        items: {
          primary: { api_key: 123, timeout: { seconds: 30 } },
        },
      },
    });
    expect(
      issues.map((issue) => [formatConfigIssuePath(issue.path), issue.messageKey]),
    ).toEqual([
      ["providers.items.primary.api_key", "providerFieldNotString"],
      ["providers.items.primary.timeout", "providerFieldNotString"],
    ]);
  });

  it("provider 的 null 标量字段跳过，不误报", () => {
    expect(
      validateRuntimeConfigStructuredDraft({
        providers: { items: { primary: { api_key: null, base_url: null } } },
      }),
    ).toEqual([]);
  });

  it("enabled 非布尔时给出错误", () => {
    const issues = validateRuntimeConfigStructuredDraft({
      providers: { items: { primary: { enabled: "yes" } } },
    });
    expect(issues).toEqual([
      {
        messageKey: "providerFieldNotBoolean",
        params: { field: "enabled" },
        path: ["providers", "items", "primary", "enabled"],
        severity: "error",
      },
    ]);
  });

  it("字符串列表字段含非字符串项时给出错误", () => {
    const issues = validateRuntimeConfigStructuredDraft({
      providers: { items: { primary: { supported_models: ["gpt-4o", 1] } } },
    });
    expect(issues).toEqual([
      {
        messageKey: "providerFieldNotStringList",
        params: { field: "supported_models" },
        path: ["providers", "items", "primary", "supported_models"],
        severity: "error",
      },
    ]);
  });

  it("映射字段不是对象时给出错误", () => {
    const issues = validateRuntimeConfigStructuredDraft({
      providers: { items: { primary: { headers: "x-trace: 1" } } },
    });
    expect(issues).toEqual([
      {
        messageKey: "providerFieldNotMapping",
        params: { field: "headers" },
        path: ["providers", "items", "primary", "headers"],
        severity: "error",
      },
    ]);
  });

  it("default_provider 非字符串时给出错误", () => {
    const issues = validateRuntimeConfigStructuredDraft({
      providers: { default_provider: 7, items: {} },
    });
    expect(issues).toEqual([
      {
        messageKey: "defaultProviderNotString",
        path: ["providers", "default_provider"],
        severity: "error",
      },
    ]);
  });

  it("default_provider 指向不存在的 provider 时只提示不阻断", () => {
    const issues = validateRuntimeConfigStructuredDraft({
      providers: { default_provider: "ghost", items: { primary: {} } },
    });
    expect(issues).toEqual([
      {
        messageKey: "defaultProviderUnknown",
        params: { name: "ghost" },
        path: ["providers", "default_provider"],
        severity: "warning",
      },
    ]);
    expect(countConfigIssues(issues, "error")).toBe(0);
    expect(countConfigIssues(issues, "warning")).toBe(1);
  });

  it("default_provider 为空串或 items 缺失时不判定引用", () => {
    expect(
      validateRuntimeConfigStructuredDraft({
        providers: { default_provider: "   ", items: {} },
      }),
    ).toEqual([]);
    expect(
      validateRuntimeConfigStructuredDraft({
        providers: { default_provider: "ghost" },
      }),
    ).toEqual([]);
  });

  it("校验不改写入参", () => {
    const draft = {
      providers: {
        default_provider: "ghost",
        items: { primary: { enabled: "yes", headers: 3 } },
      },
    };
    const snapshot = JSON.stringify(draft);
    validateRuntimeConfigStructuredDraft(draft);
    expect(JSON.stringify(draft)).toBe(snapshot);
  });
});
