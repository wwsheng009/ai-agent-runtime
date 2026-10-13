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
  normalizeConnectorLogs,
  normalizeConnectorLogsLimit,
  normalizeConnectorSchema,
  normalizeConnectorView,
  readConnectorLogs,
  readConnectorFieldErrors,
  reloadConnector,
  runConnectorAction,
  sendConnectorTestMessage,
  takeoverConnector,
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

  it("active_owner/owned_by_current_host 是 additive 字段：缺失不报错，存在则透传", () => {
    const absent = normalizeConnectorView({ id: "a", provider: "wechat" });
    expect(absent?.active_owner).toBeUndefined();
    expect(absent?.owned_by_current_host).toBe(false);

    const view = normalizeConnectorView({
      ...CONNECTOR_PAYLOAD,
      active_owner: " host-a ",
      owned_by_current_host: true,
    });
    expect(view?.active_owner).toBe("host-a");
    expect(view?.owned_by_current_host).toBe(true);
  });

  it("runtime 是 additive 字段：缺失不报错；存在则归一化 stats（零值时间→空串）", () => {
    const absent = normalizeConnectorView({ id: "a", provider: "telegram" });
    expect(absent?.runtime).toBeUndefined();

    const view = normalizeConnectorView({
      ...CONNECTOR_PAYLOAD,
      runtime: {
        stats: {
          inbound: 3,
          outbound: 2,
          errors: 1,
          dedup_skipped: 0,
          queue_full: 0,
          last_error: " 投递失败 ",
          last_error_at: "2026-10-13T10:00:00Z",
          last_activity_at: "0001-01-01T00:00:00Z",
        },
      },
    });

    expect(view?.runtime?.stats).toEqual({
      inbound: 3,
      outbound: 2,
      errors: 1,
      dedup_skipped: 0,
      queue_full: 0,
      last_error: "投递失败",
      last_error_at: "2026-10-13T10:00:00Z",
      last_activity_at: "",
    });

    // 结构非法（runtime 无 stats）→ undefined：不把「无数据」当成零计数。
    expect(
      normalizeConnectorView({ ...CONNECTOR_PAYLOAD, runtime: {} })?.runtime,
    ).toBeUndefined();
    // 计数非法值按 0（安全降级），不阻塞视图渲染。
    const degraded = normalizeConnectorView({
      ...CONNECTOR_PAYLOAD,
      runtime: { stats: { inbound: "3", errors: -1 } },
    });
    expect(degraded?.runtime?.stats.inbound).toBe(0);
    expect(degraded?.runtime?.stats.errors).toBe(0);
  });

  it("日志载荷：entries 缺失抛错；单条缺时间只丢该条；stats 缺失降级为零值", () => {
    expect(() => normalizeConnectorLogs({})).toThrow(/entries/);
    expect(() => normalizeConnectorLogs(null)).toThrow(/not an object/);

    const payload = normalizeConnectorLogs({
      entries: [
        {
          time: "2026-10-13T10:00:01Z",
          level: "info",
          direction: "in",
          connector_id: "tg-main",
          chat: "42",
          summary: "处理完成",
        },
        { level: "error", summary: "缺 time 的条目被丢弃" },
        null,
      ],
    });

    expect(payload.entries).toHaveLength(1);
    expect(payload.entries[0]).toMatchObject({
      time: "2026-10-13T10:00:01Z",
      level: "info",
      direction: "in",
      connector_id: "tg-main",
      chat: "42",
      summary: "处理完成",
    });
    // stats 缺失 → 零值（安全降级），不阻塞日志展示。
    expect(payload.stats).toMatchObject({
      inbound: 0,
      outbound: 0,
      errors: 0,
      dedup_skipped: 0,
      queue_full: 0,
      last_error_at: "",
      last_activity_at: "",
    });
    expect(payload.stats.last_error).toBeUndefined();
  });

  it("日志 limit 收敛到 [50,200]：缺省/NaN/过小→50，过大→200，小数取整", () => {
    expect(normalizeConnectorLogsLimit(undefined)).toBe(50);
    expect(normalizeConnectorLogsLimit(Number.NaN)).toBe(50);
    expect(normalizeConnectorLogsLimit(10)).toBe(50);
    expect(normalizeConnectorLogsLimit(80.9)).toBe(80);
    expect(normalizeConnectorLogsLimit(9999)).toBe(200);
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

  it("readConnectorLogs 命中 /logs?limit=…（超限收敛 200、缺省 50）并透传令牌头", async () => {
    respondWith({
      entries: [
        {
          time: "2026-10-13T10:00:01Z",
          level: "info",
          direction: "in",
          connector_id: "tg-main",
          summary: "处理完成",
        },
      ],
      stats: {
        inbound: 1,
        outbound: 0,
        errors: 0,
        dedup_skipped: 0,
        queue_full: 0,
        last_error_at: "",
        last_activity_at: "2026-10-13T10:00:01Z",
      },
    });

    const payload = await readConnectorLogs("tg/main", 500, {
      adminToken: " secret ",
    });

    expect(calls[0].url).toBe(`${CONNECTORS_PATH}/tg%2Fmain/logs?limit=200`);
    expect(header(CONNECTORS_ADMIN_TOKEN_HEADER, calls[0].init)).toBe("secret");
    expect(payload.entries).toHaveLength(1);
    expect(payload.entries[0].summary).toBe("处理完成");
    expect(payload.stats.last_activity_at).toBe("2026-10-13T10:00:01Z");

    respondWith({ entries: [], stats: {} });
    await readConnectorLogs("ghost");
    expect(calls[1].url).toBe(`${CONNECTORS_PATH}/ghost/logs?limit=50`);
  });

  it("readConnectorLogs 404/400 保持 RuntimeApiError 状态码与后端文案", async () => {
    respondWith({ error: "未知连接器" }, 404);
    const notFound = await readConnectorLogs("ghost").catch(
      (error: unknown) => error,
    );
    expect(notFound).toBeInstanceOf(RuntimeApiError);
    expect((notFound as RuntimeApiError).status).toBe(404);
    expect((notFound as RuntimeApiError).message).toContain("未知连接器");

    respondWith({ error: "limit 必须是整数" }, 400);
    const badRequest = await readConnectorLogs("tg-main", 50).catch(
      (error: unknown) => error,
    );
    expect(badRequest).toBeInstanceOf(RuntimeApiError);
    expect((badRequest as RuntimeApiError).status).toBe(400);
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

  it("takeoverConnector POST /takeover 并返回更新后的视图（404/409 复用 RuntimeApiError）", async () => {
    respondWith(
      {
        ...CONNECTOR_PAYLOAD,
        active_owner: "host-a",
        owned_by_current_host: true,
      },
      200,
    );

    const view = await takeoverConnector("wx/1", { adminToken: "admin-secret" });

    expect(calls[0].url).toBe(`${CONNECTORS_PATH}/wx%2F1/takeover`);
    expect(calls[0].init?.method).toBe("POST");
    expect(header(CONNECTORS_ADMIN_TOKEN_HEADER, calls[0].init)).toBe("admin-secret");
    expect(view).toMatchObject({
      id: "tg-main",
      active_owner: "host-a",
      owned_by_current_host: true,
    });

    respondWith({ error: "接管失败：连接器已停用，请先启用" }, 409);
    const caught = await takeoverConnector("wx-1").catch((error: unknown) => error);
    expect(caught).toBeInstanceOf(RuntimeApiError);
    expect((caught as RuntimeApiError).status).toBe(409);
    expect((caught as RuntimeApiError).message).toContain("已停用");
  });

  it("runConnectorAction 命中 actions/{action}；空载荷不带 body 与 Content-Type", async () => {
    respondWith({
      login_id: "login-1",
      status: "wait",
      base_url: "https://ilink.example",
      qr_code: "qr-1",
      qr_code_content: "data:image/png;base64,QQ==",
      expires_at: "2026-01-01T00:05:00Z",
    });

    const payload = await runConnectorAction("wx-1", " start_login ");

    expect(calls[0].url).toBe(`${CONNECTORS_PATH}/wx-1/actions/start_login`);
    expect(calls[0].init?.method).toBe("POST");
    expect(calls[0].init?.body).toBeUndefined();
    expect(header("Content-Type", calls[0].init)).toBeNull();
    expect(payload).toMatchObject({
      login_id: "login-1",
      status: "wait",
      qr_code_content: "data:image/png;base64,QQ==",
    });
  });

  it("runConnectorAction 过滤 undefined 字段；confirmed 载荷归一化 settings/secrets", async () => {
    respondWith({
      login_id: "login-1",
      status: "confirmed",
      account_id: "acc-1",
      user_id: "user-1",
      credential_ready: true,
      settings: {
        base_url: "https://ilink.example",
        account_id: "acc-1",
        owner_user_id: "user-1",
      },
      secrets: { bot_token: "tok-1" },
      future_field: 42,
    });

    const payload = await runConnectorAction(
      "wx-1",
      "login_status",
      { login_id: "login-1", base_url: undefined },
      { adminToken: "tok" },
    );

    expect(JSON.parse(String(calls[0].init?.body))).toEqual({ login_id: "login-1" });
    expect(payload.credential_ready).toBe(true);
    expect(payload.settings).toEqual({
      base_url: "https://ilink.example",
      account_id: "acc-1",
      owner_user_id: "user-1",
    });
    expect(payload.secrets).toEqual({ bot_token: "tok-1" });
    // 未识别字段原样透传，供通用展示/排障。
    expect(payload.raw).toMatchObject({ future_field: 42 });
  });

  it("runConnectorAction 空 action 直接拒绝；非对象载荷抛结构错误", async () => {
    await expect(runConnectorAction("wx-1", "   ")).rejects.toThrow(/action/);

    respondWith([1, 2, 3]);
    await expect(runConnectorAction("wx-1", "start_login")).rejects.toThrow(
      /not an object/,
    );
  });
});
