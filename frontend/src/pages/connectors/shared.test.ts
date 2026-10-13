// 连接器表单纯逻辑单测：密钥「留空=不改 / 清除=__clear__」两条纪律 + 预校验。
// 服务端另有兜底校验（restapi 走 api.Validate/MergeSubmit），这里防的是前端误提交。

import { describe, expect, it } from "vitest";

import type { ConnectorProviderSchema, ConnectorView } from "@/types/runtime";

import {
  buildSubmitPayload,
  createEmptyForm,
  findProviderSchema,
  formFromConnector,
  hasModeField,
  switchProvider,
  validateConnectorForm,
} from "./shared";

const TELEGRAM_SCHEMA: ConnectorProviderSchema = {
  provider: "telegram",
  label: "Telegram",
  modes: ["polling", "webhook"],
  fields: [
    { key: "bot_token", label: "Bot Token", type: "secret", required: true },
    { key: "proxy_url", label: "Proxy", type: "url" },
    { key: "mode", label: "Mode", type: "enum", options: [] },
  ],
};

const EXISTING: ConnectorView = {
  id: "tg-main",
  provider: "telegram",
  name: "Telegram @my_bot",
  enabled: true,
  mode: "polling",
  settings: { proxy_url: "http://127.0.0.1:7890" },
  secret_keys: ["bot_token"],
  policy: {},
};

describe("连接器表单状态", () => {
  it("编辑既有连接器：settings 回填，密钥字段保持空串（留空=不改）", () => {
    const form = formFromConnector(EXISTING);

    expect(form.values.proxy_url).toBe("http://127.0.0.1:7890");
    expect(form.values.bot_token ?? "").toBe("");
    expect(form.clearedSecrets).toEqual({});
  });

  it("schema 含 mode 字段且 settings 缺 mode：用视图生效 mode 回填（单一真源）", () => {
    const form = formFromConnector(EXISTING, TELEGRAM_SCHEMA);

    expect(form.values.mode).toBe("polling");
    expect(hasModeField(TELEGRAM_SCHEMA)).toBe(true);
    expect(hasModeField({ provider: "qq", label: "QQ", fields: [] })).toBe(false);
  });

  it("新建表单用 provider 默认值；切换通道会重建字段区", () => {
    const schema = {
      version: "1",
      providers: [
        TELEGRAM_SCHEMA,
        { provider: "qq", label: "QQ", modes: ["gateway"], fields: [] },
      ],
    };
    const form = createEmptyForm(schema);
    expect(form.provider).toBe("telegram");
    expect(form.mode).toBe("polling");

    const switched = switchProvider(form, schema, "qq");
    expect(switched.provider).toBe("qq");
    expect(switched.mode).toBe("gateway");
    expect(switched.values).toEqual({});
    expect(findProviderSchema(schema, "qq")?.provider).toBe("qq");
  });
});

describe("提交体构造（密钥语义）", () => {
  it("密钥留空不提交；填写则提交；勾选清除提交 __clear__", () => {
    const form = formFromConnector(EXISTING, TELEGRAM_SCHEMA);
    const payload = buildSubmitPayload(form, TELEGRAM_SCHEMA, { isCreate: false });

    expect(payload.secrets).toEqual({});
    expect(payload.provider).toBeUndefined();
    expect(payload.id).toBeUndefined();
    expect(payload.settings).toEqual({
      proxy_url: "http://127.0.0.1:7890",
      mode: "polling",
    });
    // telegram 真实形状：payload.mode 与 settings.mode 取同一值。
    expect(payload.mode).toBe("polling");

    const filled = buildSubmitPayload(
      { ...form, values: { ...form.values, bot_token: " 123:abc " } },
      TELEGRAM_SCHEMA,
      { isCreate: false },
    );
    expect(filled.secrets).toEqual({ bot_token: "123:abc" });

    const cleared = buildSubmitPayload(
      { ...form, clearedSecrets: { bot_token: true } },
      TELEGRAM_SCHEMA,
      { isCreate: false },
    );
    expect(cleared.secrets).toEqual({ bot_token: "__clear__" });
  });

  it("telegram 形状：mode 字段是唯一真源，顶层下拉不参与提交", () => {
    const base = formFromConnector(EXISTING, TELEGRAM_SCHEMA);
    const form = {
      ...base,
      // 顶层「模式」下拉即使残留旧值，也不得影响提交（对话框已不渲染它）。
      mode: "polling",
      values: { ...base.values, mode: "webhook" },
    };
    const payload = buildSubmitPayload(form, TELEGRAM_SCHEMA, { isCreate: false });

    expect(payload.settings?.mode).toBe("webhook");
    expect(payload.mode).toBe("webhook");
    expect(payload.mode).toBe(payload.settings?.mode);
  });

  it("无 mode 字段：保持顶层「模式」下拉提交", () => {
    const provider = { provider: "qq", label: "QQ", modes: ["gateway"], fields: [] };
    const payload = buildSubmitPayload(
      { ...createEmptyForm(null), mode: "gateway" },
      provider,
      { isCreate: true },
    );

    expect(payload.mode).toBe("gateway");
    expect(payload.settings).toEqual({});
  });

  it("新建提交 id/provider/enabled；name 缺省回落到 id", () => {
    const form = { ...createEmptyForm(null), id: " tg-1 ", name: "" };
    const payload = buildSubmitPayload(form, TELEGRAM_SCHEMA, { isCreate: true });

    expect(payload).toMatchObject({
      id: "tg-1",
      provider: "telegram",
      name: "tg-1",
      enabled: true,
    });
  });
});

describe("表单预校验", () => {
  it("新建必填密钥缺失报错；编辑时已配置密钥留空可通过", () => {
    const empty = createEmptyForm(null);
    const createErrors = validateConnectorForm(empty, TELEGRAM_SCHEMA, {
      requireID: true,
      existing: null,
    });
    expect(createErrors).toEqual([
      { key: "id", code: "required" },
      { key: "bot_token", code: "required" },
    ]);

    const editForm = formFromConnector(EXISTING);
    expect(
      validateConnectorForm(editForm, TELEGRAM_SCHEMA, {
        requireID: false,
        existing: EXISTING,
      }),
    ).toEqual([]);

    const clearedForm = { ...editForm, clearedSecrets: { bot_token: true } };
    expect(
      validateConnectorForm(clearedForm, TELEGRAM_SCHEMA, {
        requireID: false,
        existing: EXISTING,
      }),
    ).toEqual([]);
  });

  it("id 非法（含空格/斜杠）拒绝", () => {
    const form = { ...createEmptyForm(null), id: "bad id/1", values: { bot_token: "123:abc" } };
    expect(
      validateConnectorForm(form, TELEGRAM_SCHEMA, { requireID: true, existing: null }),
    ).toEqual([{ key: "id", code: "invalid_id" }]);
  });
});
