// MCP 表单纯逻辑契约测试：草稿归一化 / env 拆分与合并 / 本地校验 / 请求组包 / 往返。
//
// env 与 headers 已升级为结构化行数组：id 只用于 React key，任何情况下都不能
// 出现在请求体里；URL 传输的请求头行合并回 env 的 HEADER_<Name>，不发 headers。

import { describe, expect, it } from "vitest";

import type { RuntimeMcpConfig } from "@/types/runtime";

import {
  buildMcpUpsertRequest,
  createMcpDraft,
  createMcpKeyValueRow,
  parseLineList,
  type McpKeyValueRow,
} from "./mcp-form";

function rows(...pairs: Array<[string, string]>): McpKeyValueRow[] {
  return pairs.map(([key, value]) => createMcpKeyValueRow(key, value));
}

function baseDraft(overrides: Partial<ReturnType<typeof createMcpDraft>> = {}) {
  return { ...createMcpDraft(), name: "chrome-mcp", ...overrides };
}

describe("createMcpDraft", () => {
  it("空配置给出可编辑的默认草稿（streamable + 启用 + 空行数组）", () => {
    const draft = createMcpDraft();
    expect(draft.name).toBe("");
    expect(draft.type).toBe("streamable");
    expect(draft.enabled).toBe(true);
    expect(draft.trustLevel).toBe("");
    expect(draft.env).toEqual([]);
    expect(draft.headers).toEqual([]);
  });

  it("回显 stdio 配置：env 全部进 envRows（含 HEADER_ 前缀），disabled 优先于 enabled", () => {
    const config: RuntimeMcpConfig = {
      name: "local-mcp",
      type: "stdio",
      command: "npx",
      args: ["-y", "some-mcp"],
      env: { A: "1", HEADER_X: "keep" },
      enabled: true,
      disabled: true,
      trustLevel: "local",
      timeout: "45s",
      maxParallelCalls: 3,
    };

    const draft = createMcpDraft(config);
    expect(draft.type).toBe("stdio");
    expect(draft.enabled).toBe(false);
    expect(draft.command).toBe("npx");
    expect(draft.args).toBe("-y\nsome-mcp");
    expect(draft.env).toEqual([
      { id: expect.any(String), key: "A", value: "1" },
      { id: expect.any(String), key: "HEADER_X", value: "keep" },
    ]);
    expect(draft.headers).toEqual([]);
    expect(draft.trustLevel).toBe("local");
    expect(draft.timeoutSeconds).toBe("45");
    expect(draft.maxParallelCalls).toBe("3");
  });

  it("URL 配置把 HEADER_ 前缀拆成请求头行（去前缀），其余留在环境变量行", () => {
    const draft = createMcpDraft({
      name: "remote",
      type: "sse",
      url: "http://127.0.0.1:1234/sse",
      env: { TOKEN: "t", HEADER_Authorization: "Bearer x" },
    });

    expect(draft.env).toEqual([
      { id: expect.any(String), key: "TOKEN", value: "t" },
    ]);
    expect(draft.headers).toEqual([
      { id: expect.any(String), key: "Authorization", value: "Bearer x" },
    ]);
  });

  it("前缀不完整或仅同名前缀不拆分：HEADER_ 与 XHEADER_A 留在 env", () => {
    const draft = createMcpDraft({
      name: "remote",
      type: "streamable",
      env: { HEADER_: "raw", XHEADER_A: "raw2" },
    });

    expect(draft.headers).toEqual([]);
    expect(draft.env.map((row) => row.key)).toEqual(["HEADER_", "XHEADER_A"]);
  });

  it("未知传输类型回落 streamable，未知 trustLevel 回落空（由后端推导）", () => {
    const draft = createMcpDraft({
      name: "remote",
      type: "streamableHttp",
      trustLevel: "whatever",
    });
    expect(draft.type).toBe("streamable");
    expect(draft.trustLevel).toBe("");
  });

  it("行 id 互不相同（每次生成都是新 React key）", () => {
    const first = createMcpKeyValueRow("A", "1");
    const second = createMcpKeyValueRow("A", "1");
    expect(first.id).not.toBe(second.id);
  });
});

describe("buildMcpUpsertRequest 本地校验", () => {
  it("缺少名称 → name 校验错误", () => {
    expect(buildMcpUpsertRequest(baseDraft({ name: "  " }))).toEqual({
      validationError: "name",
    });
  });

  it("stdio 缺少 command → command 校验错误", () => {
    expect(
      buildMcpUpsertRequest(baseDraft({ type: "stdio", command: " " })),
    ).toEqual({ validationError: "command" });
  });

  it("URL 传输缺少 url → url 校验错误", () => {
    expect(
      buildMcpUpsertRequest(baseDraft({ type: "streamable", url: "" })),
    ).toEqual({ validationError: "url" });
  });

  it("timeout / maxParallelCalls 非正整数 → 对应校验错误", () => {
    expect(
      buildMcpUpsertRequest(baseDraft({ timeoutSeconds: "0" })),
    ).toEqual({ validationError: "timeoutSeconds" });
    expect(
      buildMcpUpsertRequest(baseDraft({ maxParallelCalls: "abc" })),
    ).toEqual({ validationError: "maxParallelCalls" });
  });

  it("env 行键名重复 → duplicateKey", () => {
    expect(
      buildMcpUpsertRequest(
        baseDraft({
          type: "stdio",
          command: "npx",
          env: rows(["A", "1"], ["A", "2"]),
        }),
      ),
    ).toEqual({ validationError: "duplicateKey" });
  });

  it("header 行键名重复 → duplicateKey", () => {
    expect(
      buildMcpUpsertRequest(
        baseDraft({
          type: "sse",
          url: "http://127.0.0.1:1/sse",
          headers: rows(["X", "1"], ["X", "2"]),
        }),
      ),
    ).toEqual({ validationError: "duplicateKey" });
  });

  it("env 的 HEADER_X 与 header 的 X 合并后撞键 → duplicateKey", () => {
    expect(
      buildMcpUpsertRequest(
        baseDraft({
          type: "sse",
          url: "http://127.0.0.1:1/sse",
          env: rows(["HEADER_X", "1"]),
          headers: rows(["X", "2"]),
        }),
      ),
    ).toEqual({ validationError: "duplicateKey" });
  });
});

describe("buildMcpUpsertRequest 组包", () => {
  it("stdio：命令 / 参数 / env 行，空键与空行忽略，id 不进请求体", () => {
    const result = buildMcpUpsertRequest(
      baseDraft({
        type: "stdio",
        command: " npx ",
        args: " -y\nsome-mcp \n",
        env: rows(["TOKEN", "abc"], ["", "ignored"], ["", ""], ["   ", "x"]),
      }),
    );

    expect(result).toEqual({
      request: {
        name: "chrome-mcp",
        type: "stdio",
        description: "",
        enabled: true,
        command: "npx",
        args: ["-y", "some-mcp"],
        env: { TOKEN: "abc" },
      },
    });
    expect(JSON.stringify(result)).not.toContain("kv-row-");
  });

  it("stdio：无 env 行也显式下发空对象（删光行才能清空既有配置）", () => {
    const result = buildMcpUpsertRequest(
      baseDraft({ type: "stdio", command: "npx" }),
    );

    expect(result).toEqual({
      request: {
        name: "chrome-mcp",
        type: "stdio",
        description: "",
        enabled: true,
        command: "npx",
        args: [],
        env: {},
      },
    });
  });

  it("URL：header 行合并为 HEADER_<Name> 写进 env，不发送 headers 字段", () => {
    const result = buildMcpUpsertRequest(
      baseDraft({
        type: "sse",
        url: " http://127.0.0.1:1234/sse ",
        env: rows(["TOKEN", "t"], ["", "ignored"]),
        headers: rows(["Authorization", "Bearer x"], ["", "ignored"]),
        trustLevel: "trusted_remote",
        timeoutSeconds: "20",
        maxParallelCalls: "2",
      }),
    );

    expect(result).toEqual({
      request: {
        name: "chrome-mcp",
        type: "sse",
        description: "",
        enabled: true,
        trustLevel: "trusted_remote",
        timeoutSeconds: 20,
        maxParallelCalls: 2,
        url: "http://127.0.0.1:1234/sse",
        env: { TOKEN: "t", HEADER_Authorization: "Bearer x" },
      },
    });
    expect(result).not.toHaveProperty("request.headers");
  });

  it("stdio 草稿残留的 header 行不参与组包也不参与查重", () => {
    const result = buildMcpUpsertRequest(
      baseDraft({
        type: "stdio",
        command: "npx",
        headers: rows(["X", "1"], ["X", "2"]),
      }),
    );

    expect(result).toEqual({
      request: expect.objectContaining({ env: {} }),
    });
    expect(result).not.toHaveProperty("request.headers");
  });

  it("键/值两端空白裁剪（与旧文本行解析语义一致）", () => {
    const result = buildMcpUpsertRequest(
      baseDraft({
        type: "stdio",
        command: "npx",
        env: rows([" A ", " 1 "], ["B", ""]),
      }),
    );

    expect(result).toEqual({
      request: expect.objectContaining({ env: { A: "1", B: "" } }),
    });
  });
});

describe("env / headers 往返", () => {
  it("URL 配置 → 草稿 → 请求：HEADER_* 与其他 env 原样往返", () => {
    const config: RuntimeMcpConfig = {
      name: "remote",
      type: "sse",
      url: "http://127.0.0.1:1234/sse",
      env: { TOKEN: "t", HEADER_Authorization: "Bearer x" },
    };

    const result = buildMcpUpsertRequest(createMcpDraft(config));

    expect(result).toEqual({
      request: expect.objectContaining({
        env: { TOKEN: "t", HEADER_Authorization: "Bearer x" },
      }),
    });
    expect(result).not.toHaveProperty("request.headers");
  });

  it("stdio 配置 → 草稿 → 请求：env 原样往返", () => {
    const config: RuntimeMcpConfig = {
      name: "local",
      type: "stdio",
      command: "npx",
      env: { A: "1", B: "x:y" },
    };

    const result = buildMcpUpsertRequest(createMcpDraft(config));

    expect(result).toEqual({
      request: expect.objectContaining({ env: { A: "1", B: "x:y" } }),
    });
  });

  it("parseLineList 去空行与空白", () => {
    expect(parseLineList(" a \n\n  b  \n")).toEqual(["a", "b"]);
  });
});
