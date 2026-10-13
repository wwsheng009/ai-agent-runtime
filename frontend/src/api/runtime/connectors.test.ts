// IM 连接器管理面客户端的归一化与请求形状单测。
//
// 覆盖纪律（与 connectors.ts 注释一一对应）：
//   * `items` / `providers` 结构缺失必须抛错，不降级成「空列表」；
//   * 单条缺 id / provider 只丢该条；`enabled` 缺失按 false（安全默认）；
//   * 密钥只透传 secret_keys 字段名，不回明文；
//   * DELETE 204 无响应体不得因 JSON 解析失败而误报。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  CONNECTORS_ADMIN_TOKEN_HEADER,
  CONNECTORS_PATH,
  createConnector,
  deleteConnector,
  listConnectors,
  normalizeConnectorList,
  normalizeConnectorSchema,
  normalizeConnectorView,
  readConnectorFieldErrors,
  reloadConnector,
  sendConnectorTestMessage,
  updateConnector,
} from "@/api/runtime/connectors";
import { RuntimeApiError } from "@/api/runtime/shared";

const CONNECTOR_PAYLOAD = {
  id: "tg-main",
  provider: "telegram",
  name: "Telegram @my_bot",
  enabled: true,
  mode: "polling",
  settings: { proxy_url: "http://127.0.0.1:7890" },
  secret_keys: ["bot_token"],
  policy: { command_prefix: "/", allowed_users: ["u-1"], runtime_owner: "aicli" },
};

describe("连接器归一化", () => {
  it("列表缺 `items` 抛出结构错误（不当作空列表）", () => {
    expect(() => normalizeConnectorList({})).toThrow(/items/);
    expect(() => normalizeConnectorList(null)).toThrow(/not an object/);
  });

  it("单条缺 id / provider 只丢该条，其余保留", () => {
    const views = normalizeConnectorList({
      items: [
        CONNECTOR_PAYLOAD,
        { provider: "telegram" },
        { id: "broken" },
        null,
      ],
    });

    expect(views).toHaveLength(1);
    expect(views[0]).toMatchObject({
      id: "tg-main",
      provider: "telegram",
      name: "Telegram @my_bot",
      enabled: true,
      mode: "polling",
      secret_keys: ["bot_token"],
    });
  });

  it("enabled 缺失按 false（安全默认），policy 缺失按空对象", () => {
    const view = normalizeConnectorView({
      id: "qq-1",
      provider: "qq",
      name: "",
    });

    expect(view).toMatchObject({ id: "qq-1", name: "qq-1", enabled: false, mode: "" });
    expect(view?.policy).toEqual({});
    expect(view?.settings).toBeUndefined();
    expect(view?.secret_keys).toBeUndefined();
  });

  it("schema 缺 `providers` 抛错；未知字段类型原样保留", () => {
    expect(() => normalizeConnectorSchema({})).toThrow(/providers/);

    const schema = normalizeConnectorSchema({
      version: "1",
      providers: [
        {
          provider: "telegram",
          label: "Telegram",
          modes: ["polling", "webhook"],
          fields: [
            {
              key: "bot_token",
              label: "Bot Token",
              type: "secret",
              required: true,
            },
            { key: "future_field", type: "unknown-widget" },
            { label: "no key" },
          ],
        },
        { label: "missing provider" },
      ],
    });

    expect(schema.version).toBe("1");
    expect(schema.providers).toHaveLength(1);
    expect(schema.providers[0].fields.map((field) => field.key)).toEqual([
      "bot_token",
      "future_field",
    ]);
    // 未知类型保留原始字符串：渲染器负责降级为文本框并展示原始 key。
    expect(schema.providers[0].fields[1].type).toBe("unknown-widget");
    expect(schema.providers[0].fields[1].label).toBe("future_field");
  });
});

describe("连接器 API 调用", () => {
  const originalFetch = globalThis.fetch;
  let calls: Array<{ url: string; init?: RequestInit }> = [];

  function respondWith(body: unknown, status = 200) {
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: String(input), init });
      return new Response(status === 204 ? null : JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    }) as typeof fetch;
  }

  function header(name: string, init?: RequestInit) {
    return new Headers(init?.headers).get(name);
  }

  beforeEach(() => {
    calls = [];
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("listConnectors 命中 /api/connectors 并透传 admin token", async () => {
    respondWith({ items: [CONNECTOR_PAYLOAD] });

    const items = await listConnectors({ adminToken: " secret " });

    expect(calls[0].url).toBe(CONNECTORS_PATH);
    expect(header(CONNECTORS_ADMIN_TOKEN_HEADER, calls[0].init)).toBe("secret");
    expect(items).toHaveLength(1);
  });

  it("createConnector POST JSON 体（幂等键在提交体里，密钥字段进 secrets）", async () => {
    respondWith(CONNECTOR_PAYLOAD, 201);

    await createConnector(
      {
        id: "tg-main",
        provider: "telegram",
        name: "Telegram @my_bot",
        mode: "polling",
        settings: {},
        secrets: { bot_token: "123:abc" },
      },
      { adminToken: "admin-secret" },
    );

    expect(calls[0].url).toBe(CONNECTORS_PATH);
    expect(calls[0].init?.method).toBe("POST");
    expect(header("Content-Type", calls[0].init)).toContain("application/json");
    expect(JSON.parse(String(calls[0].init?.body))).toMatchObject({
      id: "tg-main",
      secrets: { bot_token: "123:abc" },
    });
  });

  it("updateConnector 走 PATCH 且 id 经过 URL 编码", async () => {
    respondWith(CONNECTOR_PAYLOAD);

    await updateConnector("tg/main", { name: "renamed" });

    expect(calls[0].url).toBe(`${CONNECTORS_PATH}/tg%2Fmain`);
    expect(calls[0].init?.method).toBe("PATCH");
  });

  it("deleteConnector 接受 204 空响应体（不因 JSON 解析失败误报）", async () => {
    respondWith(null, 204);

    await expect(deleteConnector("tg-main")).resolves.toBeUndefined();
    expect(calls[0].init?.method).toBe("DELETE");
  });

  it("reloadConnector 接受 202 与 {ok:true}", async () => {
    respondWith({ ok: true }, 202);

    await expect(reloadConnector("tg-main")).resolves.toBeUndefined();
    expect(calls[0].url).toBe(`${CONNECTORS_PATH}/tg-main/reload`);
  });

  it("测试消息要求 chat；提交体只带非空 text", async () => {
    respondWith({ ok: true });

    await expect(
      sendConnectorTestMessage("tg-main", { chat: "  " }),
    ).rejects.toThrow(/chat/);

    await sendConnectorTestMessage("tg-main", { chat: " 42 ", text: " hi " });

    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      chat: "42",
      text: "hi",
    });
  });

  it("400 字段级校验错误可被 readConnectorFieldErrors 读出", async () => {
    respondWith(
      { error: "配置校验失败", fields: [{ key: "bot_token", message: "Bot Token 为必填项" }] },
      400,
    );

    const caught = await createConnector({ id: "x", provider: "telegram" }).catch(
      (error: unknown) => error,
    );

    expect(caught).toBeInstanceOf(RuntimeApiError);
    expect((caught as RuntimeApiError).status).toBe(400);
    expect(readConnectorFieldErrors(caught)).toEqual([
      { key: "bot_token", message: "Bot Token 为必填项" },
    ]);
    // 非 RuntimeApiError / 非该形状 → 空数组（供 UI 兜底整体错误文案）。
    expect(readConnectorFieldErrors(new Error("boom"))).toEqual([]);
  });
});
