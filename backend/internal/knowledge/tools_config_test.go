package knowledge

import "testing"

// ADR-0004 §4.4：knowledge.tools.* 逃生舱的解析、默认值与校验。

func TestParseStaleReader(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", StaleReaderOn, false},
		{"on", StaleReaderOn, false},
		{"ON", StaleReaderOn, false},
		{"enabled", StaleReaderOn, false},
		{"off", StaleReaderOff, false},
		{"Off", StaleReaderOff, false},
		{"false", StaleReaderOff, false},
		{"disabled", StaleReaderOff, false},
		{"sometimes", "", true},
	}
	for _, tc := range cases {
		got, err := ParseStaleReader(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("ParseStaleReader(%q) 应报错", tc.in)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("ParseStaleReader(%q) = %q/%v, want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestToolsConfigDefaults(t *testing.T) {
	var zero ToolsConfig
	if !zero.ToolsEnabled() {
		t.Fatal("缺省 enabled=true（工具面不因新增配置改变）")
	}
	if !zero.StaleReaderGradingEnabled() {
		t.Fatal("缺省 stale_reader=on（reader 按陈旧度分级）")
	}

	disabled := false
	if (ToolsConfig{Enabled: &disabled}).ToolsEnabled() {
		t.Fatal("enabled=false 必须生效")
	}
	enabled := true
	if !(ToolsConfig{Enabled: &enabled}).ToolsEnabled() {
		t.Fatal("enabled=true 必须生效")
	}

	if (ToolsConfig{StaleReader: StaleReaderOff}).StaleReaderGradingEnabled() {
		t.Fatal("stale_reader=off 必须关闭分级（逃生舱）")
	}
	if !(ToolsConfig{StaleReader: "bogus"}).StaleReaderGradingEnabled() {
		t.Fatal("非法值必须 fail closed 到分级生效（安全一侧）")
	}
}

func TestConfigToolsNormalizeAndValidate(t *testing.T) {
	cfg := DefaultConfig().WithWorkspace("/tmp/ws")
	cfg.Mode = ModeOn
	cfg.Tools.StaleReader = "OFF"

	normalized := cfg.Normalize()
	if normalized.Tools.StaleReader != StaleReaderOff {
		t.Fatalf("Normalize stale_reader = %q, want off", normalized.Tools.StaleReader)
	}
	if err := normalized.Validate(); err != nil {
		t.Fatalf("合法配置 Validate: %v", err)
	}

	bad := cfg
	bad.Tools.StaleReader = "sometimes"
	if err := bad.Validate(); err == nil {
		t.Fatal("非法 stale_reader 必须被 Validate 拒绝")
	}
}
