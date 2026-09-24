package profile

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseOverridePath(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []string
		wantErr string
	}{
		{name: "dotted", raw: "aicli.chat.stream", want: []string{"aicli", "chat", "stream"}},
		{name: "trimmed segments", raw: " aicli . chat ", want: []string{"aicli", "chat"}},
		{name: "empty", raw: "   ", wantErr: "覆盖键路径为空"},
		{name: "empty segment", raw: "aicli..chat", wantErr: "含空键段"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseOverridePath(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseOverridePath(%q): %v", tt.raw, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseOverridePath(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

// TestValidateOverridesWhitelist 是 D14 白名单的单一权威断言：放行域、禁止域、
// 未知键（默认拒绝）与 providers.items 字段级白名单（Q9）各自的行为都被钉住。
func TestValidateOverridesWhitelist(t *testing.T) {
	tests := []struct {
		name        string
		overrides   map[string]interface{}
		wantIssues  int
		wantMessage string
	}{
		{
			name:      "allowed aicli domains",
			overrides: map[string]interface{}{"aicli": map[string]interface{}{"chat": map[string]interface{}{"stream": true}, "log": map[string]interface{}{"level": "debug"}}},
		},
		{
			// V11 修正：SkillsRuntime 挂在配置根（agentconfig/config.go:42），
			// 路径是根级 skills_runtime.*，不是 aicli.skills_runtime.*。
			name:      "allowed root skills runtime enabled (Q9/V11)",
			overrides: map[string]interface{}{"skills_runtime": map[string]interface{}{"enabled": false}},
		},
		{
			name:      "allowed root skills runtime skill dir (V11)",
			overrides: map[string]interface{}{"skills_runtime": map[string]interface{}{"skill_dir": "/tmp/skills"}},
		},
		{
			name: "allowed root skills runtime exposure keys (V11)",
			overrides: map[string]interface{}{"skills_runtime": map[string]interface{}{
				"aicli_skill_exposure_mode":  "top_k_only",
				"aicli_skill_exposure_top_k": 3,
			}},
		},
		{
			name:      "allowed provider non-secret field (Q9)",
			overrides: map[string]interface{}{"providers": map[string]interface{}{"items": map[string]interface{}{"openai": map[string]interface{}{"default_model": "gpt-5"}}}},
		},
		{
			name:        "denied provider api key",
			overrides:   map[string]interface{}{"providers": map[string]interface{}{"items": map[string]interface{}{"openai": map[string]interface{}{"api_key": "sk-x"}}}},
			wantIssues:  1,
			wantMessage: "安全域键不可被 profile 覆盖",
		},
		{
			name:        "denied provider base url",
			overrides:   map[string]interface{}{"providers": map[string]interface{}{"items": map[string]interface{}{"openai": map[string]interface{}{"base_url": "https://x"}}}},
			wantIssues:  1,
			wantMessage: "安全域键不可被 profile 覆盖",
		},
		{
			name:        "denied root skills admin token",
			overrides:   map[string]interface{}{"skills_runtime": map[string]interface{}{"admin_token": "t"}},
			wantIssues:  1,
			wantMessage: "安全域键不可被 profile 覆盖",
		},
		{
			name:        "denied root skills jwt secret",
			overrides:   map[string]interface{}{"skills_runtime": map[string]interface{}{"jwt_secret": "s"}},
			wantIssues:  1,
			wantMessage: "安全域键不可被 profile 覆盖",
		},
		{
			name:        "denied root skills api key scopes",
			overrides:   map[string]interface{}{"skills_runtime": map[string]interface{}{"api_key_scopes": []interface{}{"a"}}},
			wantIssues:  1,
			wantMessage: "安全域键不可被 profile 覆盖",
		},
		{
			// 反 dormant 断言：Batch 7 曾把路径写成 aicli.skills_runtime.*，该键在
			// schema 中不存在；修正后必须被默认拒绝（而不是静默"配了不生效"）。
			name:        "denied dormant aicli skills runtime path",
			overrides:   map[string]interface{}{"aicli": map[string]interface{}{"skills_runtime": map[string]interface{}{"enabled": false}}},
			wantIssues:  1,
			wantMessage: "不在 profile 覆盖白名单内",
		},
		{
			name:        "denied unknown root skills field",
			overrides:   map[string]interface{}{"skills_runtime": map[string]interface{}{"unknown_field": 1}},
			wantIssues:  1,
			wantMessage: "不在 profile 覆盖白名单内",
		},
		{
			name:        "denied runtime topology",
			overrides:   map[string]interface{}{"runtime": map[string]interface{}{"server_url": "http://x"}},
			wantIssues:  1,
			wantMessage: "安全域键不可被 profile 覆盖",
		},
		{
			name:        "unknown aicli key",
			overrides:   map[string]interface{}{"aicli": map[string]interface{}{"no_such_domain": map[string]interface{}{"x": 1}}},
			wantIssues:  1,
			wantMessage: "不在 profile 覆盖白名单内",
		},
		{
			name:        "unknown provider item field",
			overrides:   map[string]interface{}{"providers": map[string]interface{}{"items": map[string]interface{}{"openai": map[string]interface{}{"site_type": "x"}}}},
			wantIssues:  1,
			wantMessage: "不在 profile 覆盖白名单内",
		},
		{
			name:        "provider item without field",
			overrides:   map[string]interface{}{"providers": map[string]interface{}{"items": map[string]interface{}{"openai": map[string]interface{}{"enabled": true}}}},
			wantIssues:  0,
			wantMessage: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := ValidateOverrides(tt.overrides)
			if len(issues) != tt.wantIssues {
				t.Fatalf("issues = %+v, want %d", issues, tt.wantIssues)
			}
			if tt.wantMessage == "" {
				return
			}
			if got := issues[0].Message; !strings.Contains(got, tt.wantMessage) {
				t.Fatalf("message = %q, want containing %q", got, tt.wantMessage)
			}
			if issues[0].Severity != ProfileSpecIssueError {
				t.Fatalf("severity = %q, want error", issues[0].Severity)
			}
		})
	}
}

// TestValidateOverridesErrorWrapsInvalidProfileSpec 保证运行时解析与
// `profile validate` 看到同一个错误类型（可被上层归类处理）。
func TestValidateOverridesErrorWrapsInvalidProfileSpec(t *testing.T) {
	err := ValidateOverridesError(map[string]interface{}{"providers": map[string]interface{}{"items": map[string]interface{}{"x": map[string]interface{}{"api_key": "k"}}}})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrInvalidProfileSpec) {
		t.Fatalf("error = %v, want ErrInvalidProfileSpec", err)
	}
	if err := ValidateOverridesError(map[string]interface{}{"aicli": map[string]interface{}{"chat": map[string]interface{}{"stream": true}}}); err != nil {
		t.Fatalf("valid overrides rejected: %v", err)
	}
	if err := ValidateOverridesError(nil); err != nil {
		t.Fatalf("nil overrides rejected: %v", err)
	}
}

func TestOverrideKeyListAndOrigins(t *testing.T) {
	overrides := map[string]interface{}{
		"aicli": map[string]interface{}{
			"chat": map[string]interface{}{"stream": true, "reasoning": "high"},
			"log":  map[string]interface{}{},
		},
	}
	wantKeys := []string{"aicli.chat.reasoning", "aicli.chat.stream", "aicli.log"}
	if got := OverrideKeyList(overrides); !reflect.DeepEqual(got, wantKeys) {
		t.Fatalf("OverrideKeyList = %v, want %v", got, wantKeys)
	}
	origins := OverrideOrigins(overrides)
	if len(origins) != len(wantKeys) {
		t.Fatalf("origins = %v, want %d entries", origins, len(wantKeys))
	}
	for _, key := range wantKeys {
		if origins[key] != OverrideOriginProfile {
			t.Fatalf("origins[%s] = %q, want %q", key, origins[key], OverrideOriginProfile)
		}
	}
}

// TestMergeOverridesIntoYAML_ZeroChange 钉住 NFR-1：没有覆盖时合并视图与 base
// 逐字节相同（调用方据此保证"无 profile/无覆盖 = 零变化"）。
func TestMergeOverridesIntoYAML_ZeroChange(t *testing.T) {
	base := []byte("aicli:\n  chat:\n    stream: false\nproviders:\n  default_provider: openai\n")
	for _, overrides := range []map[string]interface{}{nil, {}} {
		merged, err := MergeOverridesIntoYAML(base, overrides)
		if err != nil {
			t.Fatalf("MergeOverridesIntoYAML: %v", err)
		}
		if string(merged) != string(base) {
			t.Fatalf("merged = %q, want byte-identical base %q", merged, base)
		}
	}
}

func TestMergeOverridesIntoYAML_ScalarKeepsUntouchedKeys(t *testing.T) {
	base := []byte("aicli:\n  chat:\n    stream: false\n  log:\n    level: info\nproviders:\n  default_provider: openai\n")
	merged, err := MergeOverridesIntoYAML(base, map[string]interface{}{
		"aicli": map[string]interface{}{"chat": map[string]interface{}{"stream": true}},
	})
	if err != nil {
		t.Fatalf("MergeOverridesIntoYAML: %v", err)
	}
	doc := decodeMergedDocument(t, merged)
	if got := lookupOverrideTestPath(t, doc, "aicli.chat.stream"); got != true {
		t.Fatalf("aicli.chat.stream = %v, want true", got)
	}
	if got := lookupOverrideTestPath(t, doc, "aicli.log.level"); got != "info" {
		t.Fatalf("aicli.log.level = %v, want info (未写键必须回落)", got)
	}
	if got := lookupOverrideTestPath(t, doc, "providers.default_provider"); got != "openai" {
		t.Fatalf("providers.default_provider = %v, want openai", got)
	}
}

// TestMergeOverridesIntoYAML_SliceReplacesWholeList 钉住 slice 语义：整表替换，
// 不是追加（与 MergeConfigYAML 一致）。
func TestMergeOverridesIntoYAML_SliceReplacesWholeList(t *testing.T) {
	base := []byte("providers:\n  items:\n    openai:\n      supported_models:\n        - a\n        - b\n      default_model: gpt-4\n")
	merged, err := MergeOverridesIntoYAML(base, map[string]interface{}{
		"providers": map[string]interface{}{"items": map[string]interface{}{"openai": map[string]interface{}{
			"supported_models": []interface{}{"c"},
		}}},
	})
	if err != nil {
		t.Fatalf("MergeOverridesIntoYAML: %v", err)
	}
	doc := decodeMergedDocument(t, merged)
	got := lookupOverrideTestPath(t, doc, "providers.items.openai.supported_models")
	want := []interface{}{"c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("supported_models = %#v, want %#v", got, want)
	}
	if kept := lookupOverrideTestPath(t, doc, "providers.items.openai.default_model"); kept != "gpt-4" {
		t.Fatalf("default_model = %v, want gpt-4", kept)
	}
}

// TestMergeOverridesIntoYAML_EmptyMapIsNoOp 钉住 `{}` 语义：复用
// MergeConfigYAML 的"空映射 = 无操作"，同时校验器给出 warning，避免用户以为它
// 清空了 base（要屏蔽请用 null）。
func TestMergeOverridesIntoYAML_EmptyMapIsNoOp(t *testing.T) {
	base := []byte("providers:\n  items:\n    openai:\n      model_mappings:\n        a: b\n")
	overrides := map[string]interface{}{
		"providers": map[string]interface{}{"items": map[string]interface{}{"openai": map[string]interface{}{
			"model_mappings": map[string]interface{}{},
		}}},
	}
	merged, err := MergeOverridesIntoYAML(base, overrides)
	if err != nil {
		t.Fatalf("MergeOverridesIntoYAML: %v", err)
	}
	doc := decodeMergedDocument(t, merged)
	got := lookupOverrideTestPath(t, doc, "providers.items.openai.model_mappings")
	want := map[string]interface{}{"a": "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("model_mappings = %#v, want base kept %#v", got, want)
	}
	issues := ValidateOverrides(overrides)
	if len(issues) != 1 || issues[0].Severity != ProfileSpecIssueWarning {
		t.Fatalf("issues = %+v, want 1 warning for empty mapping", issues)
	}
	if !strings.Contains(issues[0].Message, "null") {
		t.Fatalf("message = %q, want pointer to null", issues[0].Message)
	}
}

// TestMergeOverridesIntoYAML_NullMasksBaseValue 钉住 null 语义：显式屏蔽 base 值。
func TestMergeOverridesIntoYAML_NullMasksBaseValue(t *testing.T) {
	base := []byte("aicli:\n  chat:\n    default_model: gpt-4\n")
	merged, err := MergeOverridesIntoYAML(base, map[string]interface{}{
		"aicli": map[string]interface{}{"chat": map[string]interface{}{"default_model": nil}},
	})
	if err != nil {
		t.Fatalf("MergeOverridesIntoYAML: %v", err)
	}
	doc := decodeMergedDocument(t, merged)
	got, ok := lookupOverrideTestPathOK(doc, "aicli.chat.default_model")
	if !ok {
		t.Fatal("default_model key missing; null 必须写成显式 null 以屏蔽 base")
	}
	if got != nil {
		t.Fatalf("default_model = %#v, want nil", got)
	}
}

func TestMergeOverridesIntoYAML_RejectsNonWhitelisted(t *testing.T) {
	_, err := MergeOverridesIntoYAML([]byte("aicli: {}\n"), map[string]interface{}{
		"providers": map[string]interface{}{"items": map[string]interface{}{"openai": map[string]interface{}{"api_key": "sk"}}},
	})
	if err == nil || !errors.Is(err, ErrInvalidProfileSpec) {
		t.Fatalf("error = %v, want ErrInvalidProfileSpec", err)
	}
}

func decodeMergedDocument(t *testing.T, raw []byte) map[string]interface{} {
	t.Helper()
	doc := map[string]interface{}{}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode merged yaml: %v\n%s", err, raw)
	}
	return doc
}

func lookupOverrideTestPath(t *testing.T, doc map[string]interface{}, path string) interface{} {
	t.Helper()
	value, ok := lookupOverrideTestPathOK(doc, path)
	if !ok {
		t.Fatalf("path %s missing in %#v", path, doc)
	}
	return value
}

func lookupOverrideTestPathOK(doc map[string]interface{}, path string) (interface{}, bool) {
	var current interface{} = doc
	for _, segment := range strings.Split(path, ".") {
		mapping, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		value, exists := mapping[segment]
		if !exists {
			return nil, false
		}
		current = value
	}
	return current, true
}
