package tools

// 评审修复轮 2 登记项（2026-10-01 收尾）：模型常把数字参数写成字符串
// （"20" / " 12 "），codeParamInt 必须解析而不是静默回落默认值；
// 小数与非数字字符串仍回退（保持既有宽松口径，不引入浮点截断语义）。

import (
	"encoding/json"
	"testing"
)

func TestCodeParamIntAcceptsNumericStrings(t *testing.T) {
	cases := []struct {
		name     string
		params   map[string]interface{}
		key      string
		fallback int
		want     int
	}{
		{"int", map[string]interface{}{"limit": 20}, "limit", 10, 20},
		{"int64", map[string]interface{}{"limit": int64(20)}, "limit", 10, 20},
		{"float64", map[string]interface{}{"limit": float64(20)}, "limit", 10, 20},
		{"json number", map[string]interface{}{"limit": json.Number("20")}, "limit", 10, 20},
		{"numeric string", map[string]interface{}{"limit": "20"}, "limit", 10, 20},
		{"padded string", map[string]interface{}{"limit": " 12 "}, "limit", 10, 12},
		{"negative string", map[string]interface{}{"limit": "-3"}, "limit", 10, -3},
		{"invalid string", map[string]interface{}{"limit": "abc"}, "limit", 10, 10},
		{"decimal string falls back", map[string]interface{}{"limit": "20.5"}, "limit", 10, 10},
		{"empty string falls back", map[string]interface{}{"limit": ""}, "limit", 10, 10},
		{"bool ignored", map[string]interface{}{"limit": true}, "limit", 10, 10},
		{"missing key", map[string]interface{}{}, "limit", 10, 10},
		{"nil params", nil, "limit", 10, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codeParamInt(tc.params, tc.key, tc.fallback); got != tc.want {
				t.Fatalf("codeParamInt(%v, %q, %d) = %d, want %d", tc.params, tc.key, tc.fallback, got, tc.want)
			}
		})
	}
}
