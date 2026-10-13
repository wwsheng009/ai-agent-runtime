package api

import (
	"encoding/json"
	"reflect"
	"testing"
)

func telegramSchema() ProviderSchema {
	return ProviderSchema{
		Provider: "telegram",
		Label:    "Telegram",
		Modes:    []string{"polling", "webhook"},
		Fields: []FieldSchema{
			{Key: "bot_token", Label: "Bot Token", Type: FieldSecret, Required: true},
			{Key: "mode", Label: "接入模式", Type: FieldEnum, Options: []Option{{Value: "polling"}, {Value: "webhook"}}, Default: "polling"},
			{Key: "proxy_url", Label: "代理", Type: FieldURL},
		},
	}
}

func TestSchemaJSONContract(t *testing.T) {
	payload, err := json.Marshal(Schema{Version: "v1", Providers: []ProviderSchema{telegramSchema()}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	providers, ok := decoded["providers"].([]any)
	if !ok || len(providers) != 1 {
		t.Fatalf("providers = %#v", decoded["providers"])
	}
	first := providers[0].(map[string]any)
	if first["provider"] != "telegram" || first["label"] != "Telegram" {
		t.Fatalf("provider identity = %#v", first)
	}
	fields := first["fields"].([]any)
	secret := fields[0].(map[string]any)
	if secret["key"] != "bot_token" || secret["type"] != "secret" || secret["required"] != true {
		t.Fatalf("secret field = %#v", secret)
	}
	// 前端契约稳定性：新增内部字段不得泄漏到 JSON。
	allKeys := map[string]struct{}{}
	for _, f := range fields {
		for k := range f.(map[string]any) {
			allKeys[k] = struct{}{}
		}
	}
	if _, leaked := allKeys["SecretKey"]; leaked {
		t.Fatalf("internal field leaked into JSON: %v", allKeys)
	}
}

func TestValidateRequiredAndEnum(t *testing.T) {
	schema := telegramSchema()

	// 必填缺失。
	errs := Validate(schema, map[string]string{}, nil)
	if len(errs) != 1 || errs[0].Key != "bot_token" {
		t.Fatalf("missing required = %+v", errs)
	}

	// 已配置 + 本次留空 = 保持原值，不报错。
	errs = Validate(schema, map[string]string{"mode": "polling"}, map[string]string{"bot_token": "x"})
	if len(errs) != 0 {
		t.Fatalf("leave-blank keep-value should pass: %+v", errs)
	}

	// enum 非法取值。
	errs = Validate(schema, map[string]string{"bot_token": "x", "mode": "carrier_pigeon"}, nil)
	if len(errs) != 1 || errs[0].Key != "mode" {
		t.Fatalf("invalid enum = %+v", errs)
	}
}

func TestSortedSecretKeysHidesValues(t *testing.T) {
	schema := telegramSchema()
	keys := SortedSecretKeys(schema, map[string]string{"bot_token": "123:abc", "mode": "polling", "empty": ""})
	if !reflect.DeepEqual(keys, []string{"bot_token"}) {
		t.Fatalf("secret keys = %v, want [bot_token]", keys)
	}
	if keys := SortedSecretKeys(schema, nil); keys != nil {
		t.Fatalf("no secrets -> nil, got %v", keys)
	}
}

func TestMergeSubmitSecretSemantics(t *testing.T) {
	schema := telegramSchema()
	existing := map[string]string{"bot_token": "old", "mode": "polling", "proxy_url": "http://p"}

	// 留空 = 保持；非密钥以提交为准（空串=清空）。
	merged := MergeSubmit(schema, existing, map[string]string{"bot_token": "", "proxy_url": ""})
	if merged["bot_token"] != "old" {
		t.Fatalf("blank secret should keep old value: %v", merged)
	}
	if merged["proxy_url"] != "" {
		t.Fatalf("blank non-secret should clear: %v", merged)
	}

	// 新值替换。
	merged = MergeSubmit(schema, existing, map[string]string{"bot_token": "new"})
	if merged["bot_token"] != "new" {
		t.Fatalf("secret should be replaced: %v", merged)
	}

	// 显式清除。
	merged = MergeSubmit(schema, existing, map[string]string{"bot_token": ClearSecret})
	if _, ok := merged["bot_token"]; ok {
		t.Fatalf("ClearSecret should remove the value: %v", merged)
	}

	// 不修改入参。
	if existing["bot_token"] != "old" {
		t.Fatalf("MergeSubmit must not mutate existing: %v", existing)
	}
}
